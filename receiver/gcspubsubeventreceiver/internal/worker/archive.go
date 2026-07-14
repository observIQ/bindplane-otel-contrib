// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/gabriel-vasile/mimetype"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/metadata"
)

// recordProducer yields decoded log records from an object and reports a
// resumable position. It unifies the non-archive path (a single parser over the
// whole object) and the archive path (many entries, each parsed independently),
// so the worker drives both through one loop.
type recordProducer interface {
	// records yields the object's log records starting at the given resume
	// position. Per-record errors are yielded inline (the worker skips them);
	// fatal conditions (for example an archive-bomb limit) are yielded as an
	// error that satisfies isDLQConditionError so the worker fails the object.
	records(ctx context.Context, start Offset) (iter.Seq2[any, error], error)

	// appendLogBody appends a record's body to lr. It must be called while the
	// producing entry is current (immediately after the record is yielded), so
	// the archive path can delegate to the entry's own parser.
	appendLogBody(ctx context.Context, lr plog.LogRecord, record any) error

	// position returns the current resumable position.
	position() Offset
}

// newRecordProducer selects a producer from the object's decompressed content.
// A recognized archive (currently tar; zip/7z/rar are added by later backends)
// becomes an archiveProducer; everything else falls back to the single-parser
// path (Avro / JSON / line / unsupported) via newParser.
//
// Archive detection only runs when TryDecoding is set. The !TryDecoding path is
// the worker's line-parse retry, which is only reached when the first pass
// returned ErrNotArrayOrKnownObject; an archive never returns that error to the
// worker (per-entry parse errors are handled inside the archive), so the retry
// pass never needs to re-detect an archive.
func newRecordProducer(ctx context.Context, stream LogStream, reader BufferedReader, metrics *metadata.TelemetryBuilder) (recordProducer, error) {
	if stream.TryDecoding {
		header, err := reader.Peek(detectionPeekBytes)
		// A short object yields io.EOF; the partial header is still valid.
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("peek content: %w", err)
		}
		if len(header) > 0 {
			detected := mimetype.Detect(header)
			if open, ok := archiveBackendFor(detected, reader); ok {
				return &archiveProducer{
					stream:  stream,
					open:    open,
					limits:  defaultArchiveLimits(),
					metrics: metrics,
				}, nil
			}
		}
	}

	parser, err := newParser(ctx, stream, reader)
	if err != nil {
		return nil, err
	}
	return &singleParserProducer{parser: parser}, nil
}

// archiveBackendFor returns a factory for the backend matching the detected
// archive type, or (nil,false) when the content is not a supported archive. The
// reader is the decompressed stream positioned at the archive's first byte.
//
// Later PRs extend this switch with zip/7z/rar. Streaming backends (tar) read
// reader directly; random-access backends materialize it inside their factory
// so any materialization error surfaces from records() rather than mid-stream.
func archiveBackendFor(detected *mimetype.MIME, reader io.Reader) (func() (archiveBackend, error), bool) {
	switch {
	case detected.Is("application/x-tar"):
		return func() (archiveBackend, error) { return newTarBackend(reader), nil }, true
	case detected.Is("application/zip"):
		return func() (archiveBackend, error) { return newZipBackend(reader) }, true
	case detected.Is("application/x-7z-compressed"):
		return func() (archiveBackend, error) { return newSevenZipBackend(reader) }, true
	default:
		return nil, false
	}
}

// singleParserProducer adapts a single LogParser (the non-archive path) to the
// recordProducer interface. EntryIndex is always 0.
type singleParserProducer struct {
	parser LogParser
}

var _ recordProducer = (*singleParserProducer)(nil)

func (s *singleParserProducer) records(ctx context.Context, start Offset) (iter.Seq2[any, error], error) {
	return s.parser.Parse(ctx, start.Offset)
}

func (s *singleParserProducer) appendLogBody(ctx context.Context, lr plog.LogRecord, record any) error {
	return s.parser.AppendLogBody(ctx, lr, record)
}

func (s *singleParserProducer) position() Offset {
	return Offset{Offset: s.parser.Offset()}
}

// archiveEntry is one member of an archive.
type archiveEntry interface {
	// Name is the entry's path within the archive.
	Name() string
	// IsDir reports whether the entry is a directory (nothing to parse).
	IsDir() bool
	// Open returns a reader over the entry's uncompressed bytes. For streaming
	// backends the reader is only valid until the next call to archiveBackend.Next.
	Open() (io.Reader, error)
}

// archiveBackend enumerates archive entries in forward order. Backends may be
// streaming (tar) or random-access (zip/7z/rar); the producer only iterates
// forward, so both fit the same interface.
type archiveBackend interface {
	// Next advances to and returns the next entry, or io.EOF when exhausted.
	Next() (archiveEntry, error)
	// Close releases resources held by the backend (for example a materialized
	// temp file). It is always called once iteration finishes or is abandoned.
	Close() error
}

// archiveLimits bounds archive expansion to guard against archive bombs (small
// inputs that expand to enormous output). Declared entry sizes are never
// trusted; readers are capped as bytes actually flow.
type archiveLimits struct {
	maxEntries    int
	maxEntryBytes int64
	maxTotalBytes int64
}

// defaultArchiveLimits returns generous caps that still abort a runaway archive
// long before it can exhaust processing resources. They are high enough not to
// interfere with legitimate large log archives.
func defaultArchiveLimits() archiveLimits {
	return archiveLimits{
		maxEntries:    100_000,
		maxEntryBytes: 8 << 30,  // 8 GiB per entry
		maxTotalBytes: 32 << 30, // 32 GiB across the whole archive
	}
}

// ErrArchiveLimitExceeded indicates an archive tripped a safety limit and was
// aborted. It routes to the unsupported-file DLQ condition so the object is not
// retried indefinitely.
type ErrArchiveLimitExceeded struct {
	Reason string
}

func (e ErrArchiveLimitExceeded) Error() string {
	return fmt.Sprintf("archive limit exceeded: %s", e.Reason)
}

// ErrCorruptArchive indicates an object was detected as an archive but its
// structure could not be decoded (a bad or truncated header, not a transient IO
// error). Backends wrap the archive library's structural open failure in it so
// the object is classified as an unsupported-file DLQ condition rather than a
// generic transient failure that would be redelivered pointlessly. IO errors from
// materializing the object are left unwrapped so they remain retryable.
type ErrCorruptArchive struct {
	Type string
	Err  error
}

func (e ErrCorruptArchive) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("corrupt %s archive: %v", e.Type, e.Err)
	}
	return fmt.Sprintf("corrupt archive: %v", e.Err)
}

func (e ErrCorruptArchive) Unwrap() error { return e.Err }

// archiveProducer iterates an archive's entries, parsing each with newParser and
// yielding all records across entries as one sequence. It owns the shared archive
// behavior (entry iteration, per-entry format detection, per-entry parse-error
// skipping, bomb limits and the (entryIndex, innerOffset) resume model), so a new
// backend only has to enumerate entries.
type archiveProducer struct {
	stream  LogStream
	open    func() (archiveBackend, error)
	limits  archiveLimits
	metrics *metadata.TelemetryBuilder

	// curIndex and curParser track the entry currently being yielded so that
	// position() and appendLogBody() reflect the right entry. They are only read
	// synchronously with the generator (between yields), so no locking is needed.
	curIndex  int
	curParser LogParser
}

var _ recordProducer = (*archiveProducer)(nil)

func (a *archiveProducer) records(ctx context.Context, start Offset) (iter.Seq2[any, error], error) {
	backend, err := a.open()
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}

	return func(yield func(any, error) bool) {
		defer func() {
			if cerr := backend.Close(); cerr != nil {
				a.stream.Logger.Warn("close archive backend", zap.Error(cerr))
			}
		}()

		var totalBytes int64
		idx := -1
		for {
			entry, err := backend.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				// A malformed or truncated archive stops iteration; records
				// emitted so far are kept, mirroring the leaf parsers' behavior
				// on unexpected EOF.
				a.stream.Logger.Warn("stopping archive iteration after entry error", zap.Error(err))
				return
			}
			idx++
			a.curIndex = idx

			if idx >= a.limits.maxEntries {
				yield(nil, ErrArchiveLimitExceeded{Reason: fmt.Sprintf("entry count exceeded %d", a.limits.maxEntries)})
				return
			}
			if entry.IsDir() {
				continue
			}
			// Entries before the resume index were fully consumed in a prior run.
			if idx < start.EntryIndex {
				continue
			}
			entryStart := int64(0)
			if idx == start.EntryIndex {
				entryStart = start.Offset
			}

			if fatal := a.consumeEntry(ctx, entry, entryStart, &totalBytes, yield); fatal {
				return
			}
		}
	}, nil
}

// consumeEntry parses a single entry and yields its records. It returns true when
// a fatal (object-failing) condition occurred and iteration must stop; per-entry
// parse failures are logged and skipped (returning false so iteration continues).
func (a *archiveProducer) consumeEntry(ctx context.Context, entry archiveEntry, entryStart int64, totalBytes *int64, yield func(any, error) bool) (fatal bool) {
	er, err := entry.Open()
	if err != nil {
		a.skipEntry(ctx, entry.Name(), fmt.Errorf("open entry: %w", err))
		return false
	}
	// Random-access backends (zip/7z/rar) return a per-entry io.ReadCloser that
	// must be closed once the entry is consumed. Streaming backends (tar) return
	// the shared stream reader, which is not an io.Closer and is left untouched.
	if c, ok := er.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}

	capped := &cappingReader{
		r:          er,
		entryLimit: a.limits.maxEntryBytes,
		total:      totalBytes,
		totalLimit: a.limits.maxTotalBytes,
	}
	entryReader := NewBufferedReader(capped, a.stream.MaxLogSize)

	// Each entry re-enters the format stage independently. Entries carry no HTTP
	// labels, so content type / content encoding are nil and detection is purely
	// from the entry's bytes.
	entryStream := LogStream{
		Name:        entry.Name(),
		MaxLogSize:  a.stream.MaxLogSize,
		Logger:      a.stream.Logger,
		TryDecoding: true,
	}

	parser, err := newParser(ctx, entryStream, entryReader)
	if err != nil {
		// Recognized-but-unsupported entry (image, PDF, unknown binary): skip it
		// rather than failing the whole archive.
		a.skipEntry(ctx, entry.Name(), err)
		return false
	}
	a.curParser = parser

	seq, err := parser.Parse(ctx, entryStart)
	if err != nil {
		// The entry's content did not match a supported structure (for example a
		// JSON object without a Records array). Skip it.
		a.skipEntry(ctx, entry.Name(), err)
		return false
	}

	for rec, rerr := range seq {
		if rerr != nil {
			var limitErr ErrArchiveLimitExceeded
			if errors.As(rerr, &limitErr) {
				// A bomb limit tripped mid-read: fail the object.
				yield(nil, limitErr)
				return true
			}
			// A per-record decode error: forward it so the worker skips just this
			// record, consistent with the non-archive path.
			if !yield(nil, rerr) {
				return true
			}
			continue
		}
		if !yield(rec, nil) {
			return true
		}
	}
	return false
}

// skipEntry logs and counts an entry that could not be parsed, without failing
// the archive. GcseventParseErrors is used (not the DLQ metric) because the
// object itself still succeeds.
func (a *archiveProducer) skipEntry(ctx context.Context, name string, err error) {
	a.stream.Logger.Warn("skipping unparseable archive entry",
		zap.String("entry", name), zap.Error(err))
	if a.metrics != nil {
		a.metrics.GcseventParseErrors.Add(ctx, 1)
	}
}

func (a *archiveProducer) appendLogBody(ctx context.Context, lr plog.LogRecord, record any) error {
	if a.curParser == nil {
		return fmt.Errorf("no active archive entry parser")
	}
	return a.curParser.AppendLogBody(ctx, lr, record)
}

func (a *archiveProducer) position() Offset {
	var inner int64
	if a.curParser != nil {
		inner = a.curParser.Offset()
	}
	return Offset{EntryIndex: a.curIndex, Offset: inner}
}

// cappingReader enforces the per-entry and cumulative byte caps as bytes flow.
// Once a cap is tripped it fails every subsequent Read so the parser aborts
// promptly. It is single-goroutine (driven synchronously by the parser), so the
// shared total counter needs no synchronization.
type cappingReader struct {
	r          io.Reader
	entryRead  int64
	entryLimit int64
	total      *int64
	totalLimit int64
	tripped    error
}

func (c *cappingReader) Read(p []byte) (int, error) {
	if c.tripped != nil {
		return 0, c.tripped
	}
	n, err := c.r.Read(p)
	c.entryRead += int64(n)
	*c.total += int64(n)
	if c.entryLimit > 0 && c.entryRead > c.entryLimit {
		c.tripped = ErrArchiveLimitExceeded{Reason: "per-entry uncompressed size exceeded"}
		return 0, c.tripped
	}
	if c.totalLimit > 0 && *c.total > c.totalLimit {
		c.tripped = ErrArchiveLimitExceeded{Reason: "total uncompressed size exceeded"}
		return 0, c.tripped
	}
	return n, err
}
