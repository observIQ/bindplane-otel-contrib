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

package blobstream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/gabriel-vasile/mimetype"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// RecordProducer yields decoded log records from an object and reports a
// resumable position. It unifies the non-archive path (a single parser over the
// whole object) and the archive path (many entries, each parsed independently),
// so the worker drives both through one loop.
type RecordProducer interface {
	// records yields the object's log records starting at the given resume
	// position. Per-record errors are yielded inline (the worker skips them);
	// fatal conditions (for example an archive-bomb limit) are yielded as an
	// error that satisfies isDLQConditionError so the worker fails the object.
	Records(ctx context.Context, start Offset) (iter.Seq2[any, error], error)

	// appendLogBody appends a record's body to lr. It must be called while the
	// producing entry is current (immediately after the record is yielded), so
	// the archive path can delegate to the entry's own parser.
	AppendLogBody(ctx context.Context, lr plog.LogRecord, record any) error

	// position returns the current resumable position.
	Position() Offset
}

// CloseProducer releases resources a producer holds when its Records iterator is not driven
// to completion — for example a materialized archive's temp file. It is safe to call on any
// producer and after the iterator has already been fully consumed. Callers should defer it
// right after creating a producer.
func CloseProducer(p RecordProducer) {
	if c, ok := p.(io.Closer); ok {
		_ = c.Close()
	}
}

// NewRecordProducer selects a producer from the object's decompressed content.
// A recognized archive (currently tar; zip/7z/rar are added by later backends)
// becomes an archiveProducer; everything else falls back to the single-parser
// path (Avro / JSON / line / unsupported) via newParser.
//
// Archive detection only runs when TryDecoding is set. The !TryDecoding path is
// the worker's line-parse retry, which is only reached when the first pass
// returned ErrNotArrayOrKnownObject; an archive never returns that error to the
// worker (per-entry parse errors are handled inside the archive), so the retry
// pass never needs to re-detect an archive.
// The context is accepted so detection can grow cancellable work without a signature
// change. Nothing it does today blocks.
func NewRecordProducer(_ context.Context, stream LogStream, reader BufferedReader, onParseError ParseErrorFunc) (RecordProducer, error) {
	if stream.TryDecoding {
		header, err := reader.Peek(detectionPeekBytes)
		// A short object yields io.EOF; the partial header is still valid. A truncated
		// compression layer yields io.ErrUnexpectedEOF here; the partial header is still
		// valid for detection, and the truncation is classified later at open/parse time,
		// so tolerate it like a clean short read (matching stream.decompress).
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			// A non-EOF peek failure here is a decode failure of the object's own
			// (de)compression layer (for example a bad-checksum gzip). Returned bare it
			// is unclassified, so the worker treats it as transient and redelivers the
			// same bytes forever. Classify it via the raw source: a broken/short download
			// retries, a clean-but-corrupt object goes to the DLQ.
			return nil, classifyReadFailure(reader, err)
		}
		if len(header) > 0 {
			detected := mimetype.Detect(header)
			limits := defaultArchiveLimits()
			if open, ok := archiveBackendFor(detected, reader, stream.archiveTempDir, limits.maxTotalBytes); ok {
				return &archiveProducer{
					stream:       stream,
					open:         open,
					limits:       limits,
					onParseError: onParseError,
					reader:       reader,
					format:       detected.String(),
				}, nil
			}
		}
	}

	parser, err := newParser(stream, reader)
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
// (into tempDir, OS default when empty) so any materialization error surfaces from
// Records() rather than mid-stream.
func archiveBackendFor(detected *mimetype.MIME, reader io.Reader, tempDir string, maxBytes int64) (func() (archiveBackend, error), bool) {
	switch {
	case detected.Is("application/x-tar"):
		return func() (archiveBackend, error) { return newTarBackend(reader), nil }, true
	case detected.Is("application/zip"):
		return func() (archiveBackend, error) { return newZipBackend(reader, tempDir, maxBytes) }, true
	case detected.Is("application/x-7z-compressed"):
		return func() (archiveBackend, error) { return newSevenZipBackend(reader, tempDir, maxBytes) }, true
	case detected.Is("application/x-rar-compressed"):
		return func() (archiveBackend, error) { return newRarBackend(reader) }, true
	default:
		return nil, false
	}
}

// singleParserProducer adapts a single LogParser (the non-archive path) to the
// RecordProducer interface. EntryIndex is always 0.
type singleParserProducer struct {
	parser LogParser
}

var _ RecordProducer = (*singleParserProducer)(nil)

func (s *singleParserProducer) Records(ctx context.Context, start Offset) (iter.Seq2[any, error], error) {
	return s.parser.Parse(ctx, start.Offset)
}

func (s *singleParserProducer) AppendLogBody(ctx context.Context, lr plog.LogRecord, record any) error {
	return s.parser.AppendLogBody(ctx, lr, record)
}

func (s *singleParserProducer) Position() Offset {
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
	// Materialized reports whether the backend reads from a fully materialized copy
	// of the object (zip/7z) rather than the live object stream (tar/rar). A
	// materialized backend's Next never reports a member truncation, so a member that
	// ends in an unexpected EOF must be counted where it is read instead.
	Materialized() bool
}

// archiveLimits bounds archive expansion to guard against archive bombs (small
// inputs that expand to enormous output). Declared entry sizes are never
// trusted; readers are capped as bytes actually flow.
type archiveLimits struct {
	maxEntries      int
	maxEntryBytes   int64
	maxTotalBytes   int64
	maxNestingDepth int
}

// defaultArchiveLimits returns generous caps that still abort a runaway archive
// long before it can exhaust processing resources. They are high enough not to
// interfere with legitimate large log archives.
func defaultArchiveLimits() archiveLimits {
	return archiveLimits{
		maxEntries:      100_000,
		maxEntryBytes:   8 << 30,  // 8 GiB per entry
		maxTotalBytes:   32 << 30, // 32 GiB across the whole archive
		maxNestingDepth: 8,        // decompression layers per member (blocks quines)
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
	stream       LogStream
	open         func() (archiveBackend, error)
	limits       archiveLimits
	onParseError ParseErrorFunc

	// backend is the open archive backend. It is held so Close can release a
	// materialized backend's temp file even when the returned iterator is never driven.
	backend archiveBackend
	// reader is the decompressed stream the backend enumerates. Its ReadErr/AtEOF
	// classify a failure to advance to the next entry: a broken stream fails the
	// object (retry and resume), the bytes running out is truncation, and anything
	// else is a corrupt container.
	reader BufferedReader
	// format labels the archive for a corrupt-container error.
	format string
	// materialized records whether the backend reads a materialized copy of the
	// object (zip/7z). Set once the backend is opened in Records.
	materialized bool

	// curIndex and curParser track the entry currently being yielded so that
	// Position() and AppendLogBody() reflect the right entry. They are only read
	// synchronously with the generator (between yields), so no locking is needed.
	curIndex  int
	curParser LogParser
}

var _ RecordProducer = (*archiveProducer)(nil)

func (a *archiveProducer) Records(ctx context.Context, start Offset) (iter.Seq2[any, error], error) {
	// Release a backend from a prior Records call, so calling it twice cannot leak the
	// first materialized temp file.
	a.closeBackend()
	backend, err := a.open()
	if err != nil {
		// Reconcile an open/materialize failure with the raw source, mirroring the Next()
		// classification below. A materialized backend (zip/7z) reads the whole object
		// here, so a broken or short download, or a corrupt-at-rest archive, first
		// surfaces at open time. Without this, a short/broken download is dead-lettered
		// (unrecoverable) and an unclassified decompression error redelivers forever.
		switch {
		case a.reader != nil && (a.reader.RawReadErr() != nil || a.reader.RawTruncated()):
			// The source broke, or ended short of the object's known size: retry and
			// resume from the saved entry offset. Strip the corrupt-archive wrapper so the
			// result is purely a broken stream and does not also satisfy
			// IsUnsupportedContent, which would route it to the dead-letter queue.
			inner := err
			var corruptArchive ErrCorruptArchive
			if errors.As(err, &corruptArchive) {
				inner = corruptArchive.Err
			}
			return nil, ErrStreamRead{Err: inner}
		case IsUnsupportedContent(err):
			// Already a dead-letter condition (corrupt archive or a tripped bomb limit)
			// and the source was not short: keep the classification.
			return nil, fmt.Errorf("open archive: %w", err)
		case a.reader != nil && a.reader.RawAtEOF():
			// The source delivered cleanly but the archive would not open: corrupt
			// content (for example a truncated compression layer). Route it to the
			// dead-letter queue rather than a bare error that redelivers forever.
			return nil, ErrCorruptArchive{Type: a.format, Err: err}
		default:
			// An infrastructure failure (for example creating the materialization temp
			// file) with the source not yet exhausted: keep it generic and retryable.
			return nil, fmt.Errorf("open archive: %w", err)
		}
	}
	a.backend = backend
	a.materialized = backend.Materialized()

	return func(yield func(any, error) bool) {
		defer a.closeBackend()

		var totalBytes int64
		idx := -1
		for {
			entry, err := backend.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				// Failure to advance to the next entry is classified the same way the
				// leaf parsers classify a failed read, so records emitted so far are
				// never silently acked with the rest of the archive lost.
				//
				// This fires only for streaming backends (tar/rar): a materialized
				// backend (zip/7z) enumerates an in-memory slice, so its Next never
				// returns a non-EOF error, and a corrupt container is caught earlier at
				// open time as ErrCorruptArchive. The reader is always set on the
				// production path; the nil check guards test constructors that omit it.
				a.stream.Logger.Warn("archive iteration stopped on entry error", zap.Error(err))
				switch {
				case a.reader == nil:
					yield(nil, ErrCorruptArchive{Type: a.format, Err: err})
				case a.reader.RawReadErr() != nil || a.reader.RawTruncated():
					// The source stream broke, or ended short of the object's size:
					// the download did not complete. Fail the object so it retries and
					// resumes from the saved entry offset.
					yield(nil, ErrStreamRead{Err: err})
				case a.reader.RawAtEOF():
					// The source delivered cleanly but the archive structure ran out
					// part way through: the object is truncated.
					yield(nil, ErrTruncatedObject{Err: err})
				default:
					// The archive structure itself is malformed.
					yield(nil, ErrCorruptArchive{Type: a.format, Err: err})
				}
				return
			}
			idx++
			a.curIndex = idx
			// Clear the parser so curIndex and curParser never disagree: an entry that is
			// skipped (a directory, or one that fails before a parser is built) leaves it
			// nil, and Position() reports {curIndex, 0} rather than the previous entry's
			// offset. consumeEntry sets it before yielding a record.
			a.curParser = nil

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

	// Each entry re-enters the format stage independently. Entries carry no HTTP
	// labels, so content type / content encoding are nil and detection is purely
	// from the entry's bytes.
	entryStream := LogStream{
		Name:        entry.Name(),
		MaxLogSize:  a.stream.MaxLogSize,
		Logger:      a.stream.Logger,
		TryDecoding: true,

		// Body options apply to every entry, not only to a non-archive object.
		Raw:                      a.stream.Raw,
		IncludeLogRecordOriginal: a.stream.IncludeLogRecordOriginal,
	}

	// A member may itself be compressed (a .log.gz inside a tar). Decompress it down
	// to its leaf content before parsing. A member that trips a compression limit or
	// carries a corrupt compression layer is skipped, not fatal, so the remaining
	// members still parse.
	leaf, err := decompressEntryToLeaf(entryStream, capped, a.limits.maxNestingDepth)
	if err != nil {
		// A bomb cap tripping while decompressing/detecting the entry is fatal for
		// the object, not a benign skip: the archive is untrustworthy and must go
		// to the DLQ rather than be acked with the offending entry silently dropped.
		if a.failOnArchiveLimit(err, yield) {
			return true
		}
		a.skipEntry(ctx, entry.Name(), err)
		return false
	}
	entryReader := NewBufferedReader(leaf, a.stream.MaxLogSize)

	parser, err := newParser(entryStream, entryReader)
	if err != nil {
		// Recognized-but-unsupported entry (image, PDF, unknown binary): skip it
		// rather than failing the whole archive.
		a.skipEntry(ctx, entry.Name(), err)
		return false
	}
	a.curParser = parser

	seq, err := parser.Parse(ctx, entryStart)
	if err != nil {
		// A bomb cap tripping while the parser classifies the entry is fatal for
		// the same reason as above.
		if a.failOnArchiveLimit(err, yield) {
			return true
		}
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
			// Content this entry cannot use stops the entry, not the object. The
			// other entries still read, so failing here would send them to the
			// dead-letter queue alongside the bad one.
			if isUnusableContent(rerr) {
				a.skipEntry(ctx, entry.Name(), rerr)
				return false
			}
			// A read failure reported against an entry while the object stream itself is
			// clean (no raw read error, not short of a known size) is either the object
			// stream running out within this entry or a per-entry decode failure.
			if IsStreamRead(rerr) && a.reader != nil && a.reader.RawReadErr() == nil && !a.reader.RawTruncated() {
				if errors.Is(rerr, io.ErrUnexpectedEOF) {
					// A truncated member is reported by a STREAMING backend's Next() only when
					// the OBJECT stream itself ran out (Next then fails to advance). Two cases
					// break that assumption, and in both the object is otherwise intact so the
					// member must be counted here or it is silently dropped and the object
					// acked: a materialized backend (zip/7z) reads an in-memory index and never
					// surfaces it via Next; and a streaming backend whose container is still
					// intact (raw source not at EOF, more entries follow) advances cleanly to
					// the next entry and never surfaces it either.
					if a.materialized || !a.reader.RawAtEOF() {
						a.skipEntry(ctx, entry.Name(), rerr)
						return false
					}
					// Streaming backend AND the object stream ran out within this entry: the
					// whole object is truncated, not this one entry. Do not count a per-entry
					// parse error; the backend cannot advance to the next entry and the
					// object-level handler reports the truncation once, which the worker
					// delivers, acks, and counts as a single truncated object.
					return false
				}
				// The object stream is intact, so this is a deterministic per-entry decode
				// failure (a corrupt compressed member). Skip the entry rather than failing
				// the whole archive and looping on every redelivery.
				a.skipEntry(ctx, entry.Name(), rerr)
				return false
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

// decompressEntryToLeaf repeatedly decompresses an archive member until the
// content is no longer a recognized compression format, returning the leaf reader
// to parse. Log shippers commonly gzip each file before archiving, so a member is
// often itself compressed. Each layer's output is capped by maxDecompressedBytes,
// and the number of layers is capped by maxDepth, so a deeply nested compression
// bomb (a quine) cannot expand without bound. maxDepth is the number of layers
// permitted: 0 rejects any compressed member.
func decompressEntryToLeaf(stream LogStream, r io.Reader, maxDepth int) (io.Reader, error) {
	for depth := 0; ; depth++ {
		br := bufio.NewReaderSize(r, detectionPeekBytes)
		reader, decompressed, err := stream.decompress(br)
		if err != nil {
			return nil, err
		}
		if !decompressed {
			return br, nil
		}
		if depth >= maxDepth {
			return nil, errNestingDepthExceeded
		}
		r = &decompressLimitReader{r: reader, limit: maxDecompressedBytes}
	}
}

// errNestingDepthExceeded marks an archive member that needs more decompression
// layers than the per-member cap allows (a nested-compression quine). It is a
// per-member skip, not an object-level bomb cap, so it is deliberately not an
// ErrArchiveLimitExceeded and never fails the whole object.
var errNestingDepthExceeded = errors.New("decompression nesting depth exceeded")

// failOnArchiveLimit reports whether err is a bomb-cap trip and, if so, yields it
// as a fatal object error. Classification-stage reads (decompress detection and
// parser setup) surface a tripped cap the same way the in-loop read does; without
// this check those sites would swallow it as a benign per-entry skip and ack the
// object instead of routing the untrustworthy archive to the DLQ.
func (a *archiveProducer) failOnArchiveLimit(err error, yield func(any, error) bool) (fatal bool) {
	var limitErr ErrArchiveLimitExceeded
	if errors.As(err, &limitErr) {
		yield(nil, limitErr)
		return true
	}
	return false
}

// skipEntry logs and counts an entry that failed to parse. The object still
// succeeds, so this is a parse error and not a DLQ condition.
func (a *archiveProducer) skipEntry(ctx context.Context, name string, err error) {
	a.stream.Logger.Warn("skipping unparseable archive entry",
		zap.String("entry", name), zap.Error(err))
	if a.onParseError != nil {
		a.onParseError(ctx)
	}
}

func (a *archiveProducer) AppendLogBody(ctx context.Context, lr plog.LogRecord, record any) error {
	if a.curParser == nil {
		return fmt.Errorf("no active archive entry parser")
	}
	return a.curParser.AppendLogBody(ctx, lr, record)
}

// closeBackend releases the open backend, freeing a materialized backend's temp file.
func (a *archiveProducer) closeBackend() {
	if a.backend == nil {
		return
	}
	if cerr := a.backend.Close(); cerr != nil {
		a.stream.Logger.Warn("close archive backend", zap.Error(cerr))
	}
	a.backend = nil
}

// Close releases a materialized backend if the Records iterator was never driven to
// completion. A driven iterator already releases it; Close then no-ops. Callers detect
// this via an io.Closer type assertion, so it is optional on the RecordProducer contract.
func (a *archiveProducer) Close() error {
	a.closeBackend()
	return nil
}

func (a *archiveProducer) Position() Offset {
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
