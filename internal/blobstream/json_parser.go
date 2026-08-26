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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

var (
	// ErrNotArrayOrKnownObject is returned when the JSON stream is not a valid array or object
	// with a known key. When this occurs, try to parse as text.
	ErrNotArrayOrKnownObject = errors.New("expected array or object with known key")

	// errTrailingRecordsWrapper marks a records-wrapper document whose tail, or the next
	// concatenated document after it, could not be parsed as a records wrapper. It is a
	// counted per-record parse error, deliberately NOT ErrNotArrayOrKnownObject: the worker
	// treats that sentinel as "this object is not JSON" and re-reads the whole object with
	// the line parser, which would re-emit the records already delivered from this object as
	// raw line bodies and garble the checkpoint.
	errTrailingRecordsWrapper = errors.New("trailing document is not a records wrapper")
)

const (
	// maxRecordsSearchBytes is the maximum number of bytes to search for a "Records" key in
	// the first 4096 bytes of the JSON stream. This is to avoid parsing the entire file
	// looking for a "Records" key and not finding it.
	maxRecordsSearchBytes = 4096

	// mixedSequenceLogMsg is warned once per file for a value sequence that mixes objects
	// with non-object lines.
	mixedSequenceLogMsg = "mixed content in JSON value sequence; text lines are emitted as bodies, corrupted JSON is dropped"
)

// MinLogSize is the smallest usable max_log_size. Content detection peeks fixed windows
// (the widest is maxRecordsSearchBytes) against a buffer sized to max_log_size, so a
// smaller value makes every object fail detection with bufio.ErrBufferFull at runtime.
// Callers reject a configured max_log_size below this in config validation.
const MinLogSize = maxRecordsSearchBytes

// maxRecordBytesHardLimit is the OOM backstop on a single Decode: a value too large to
// even hold in memory is rejected before it is buffered whole. It is deliberately far
// above max_log_size (which is the exact hard wall, enforced on the decoded size) so a
// record merely over max_log_size still decodes and can be rejected while the decoder
// stays aligned. It is a var only so tests can lower it to exercise the backstop.
var maxRecordBytesHardLimit int64 = 128 << 20

// errRecordTooLarge marks a single top-level value that exceeds the per-record byte cap.
// It is handled like a JSON structure error: the record is a counted parse error and, for
// a value sequence, the decoder resyncs past it. Buffering an unbounded value whole is an
// OOM vector for uncompressed NDJSON, so the cap trips before that happens.
var errRecordTooLarge = errors.New("record exceeds max_log_size")

// cappedRecordReader bounds how many bytes a single read (a Decode, or a navigation token)
// may consume while armed, so one very large value cannot be buffered whole into memory.
// The parser arms it with a per-operation limit and disarms it between operations. A limit
// <= 0 disables the cap.
type cappedRecordReader struct {
	r     io.Reader
	limit int64
	n     int64
	armed bool
}

func (c *cappedRecordReader) Read(p []byte) (int, error) {
	if c.armed && c.limit > 0 {
		if c.n >= c.limit {
			return 0, errRecordTooLarge
		}
		if int64(len(p)) > c.limit-c.n {
			p = p[:c.limit-c.n]
		}
	}
	m, err := c.r.Read(p)
	if c.armed {
		c.n += int64(m)
	}
	return m, err
}

// arm starts counting a fresh operation against limit. disarm stops counting.
func (c *cappedRecordReader) arm(limit int64) { c.armed, c.n, c.limit = true, 0, limit }
func (c *cappedRecordReader) disarm()         { c.armed = false }

// recordSizeLimit is the reader's max_log_size when it reports one, else the hard limit.
func recordSizeLimit(reader BufferedReader) int64 {
	if l, ok := reader.(interface{ maxRecordBytes() int64 }); ok {
		if v := l.maxRecordBytes(); v > 0 {
			return v
		}
	}
	return maxRecordBytesHardLimit
}

type jsonParser struct {
	reader  BufferedReader
	decoder *json.Decoder
	opts    BodyOptions
	// capped bounds a single Decode so an unbounded top-level value cannot OOM the process.
	capped *cappedRecordReader
	// baseOffset is the absolute byte offset at which the current decoder started. It is
	// nonzero after a resync rebuilds the decoder past a malformed line, so Offset()
	// stays absolute for resume.
	baseOffset int64
	// src is the reader the current decoder consumes from. It starts as reader and
	// becomes the resync bufio.Reader after each resync. The resync reader has read ahead
	// past the decoder, so the next resync must continue from it, not from the drained
	// original reader, or the buffered-but-unread records between two resyncs are lost.
	src io.Reader
	// logger reports a mixed value sequence; may be nil.
	logger *zap.Logger
	// warnedMixed gates that warning to once per file.
	warnedMixed bool
}

var _ LogParser = (*jsonParser)(nil)

// NewJSONParser creates a new JSON parser. logger (may be nil) reports a mixed value sequence.
func NewJSONParser(reader BufferedReader, logger *zap.Logger, opts BodyOptions) LogParser {
	capped := &cappedRecordReader{r: reader}
	return &jsonParser{
		reader:  reader,
		decoder: json.NewDecoder(capped),
		opts:    opts,
		src:     reader,
		capped:  capped,
		logger:  logger,
	}
}

// jsonPeekBytes is the number of leading bytes inspected to classify the stream
// as JSON. It only needs to reach the second meaningful byte past any leading
// whitespace, so a small window is sufficient.
const jsonPeekBytes = 512

// StartsWithJSONObjectOrArray reports whether the reader begins with one of the
// JSON shapes this parser supports: an object (`{...}` or `{}`) or an array of
// objects (`[{...}]` or `[]`). It uses Peek and does not advance the reader.
//
// A leading `{` or `[` alone is too weak: a common log line such as
// `[2024-01-01T00:00:00Z] INFO ...` starts with `[` but is not JSON. So the check
// is structural. After leading whitespace, the first meaningful byte must be `{`
// or `[`, and the next meaningful byte must confirm the shape:
//
//   - `{` must be followed by `"` (a key) or `}` (empty object);
//   - `[` must be followed by `{` (array of objects) or `]` (empty array).
//
// This accepts array-of-objects, `{"Records":[...]}`, `{}` and `[]`, and rejects
// `[2024-...]`, `[1,2,3]`, and `["a","b"]`, all of which route to line parsing.
func StartsWithJSONObjectOrArray(reader BufferedReader) (bool, error) {
	buf, err := reader.Peek(jsonPeekBytes)
	if err != nil {
		// Fewer than jsonPeekBytes available is fine; classify from what we have.
		if !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("peek: %w", err)
		}
	}

	open, rest, ok := firstMeaningfulByte(buf)
	if !ok {
		return false, nil
	}

	switch open {
	case '{':
		next, _, ok := firstMeaningfulByte(rest)
		return ok && (next == '"' || next == '}'), nil
	case '[':
		next, _, ok := firstMeaningfulByte(rest)
		return ok && (next == '{' || next == ']'), nil
	default:
		return false, nil
	}
}

// firstMeaningfulByte returns the first non-whitespace byte in buf, the bytes
// following it, and whether one was found.
func firstMeaningfulByte(buf []byte) (b byte, rest []byte, ok bool) {
	for i := 0; i < len(buf); i++ {
		switch buf[i] {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return buf[i], buf[i+1:], true
		}
	}
	return 0, nil, false
}

// jsonShape describes how an object holds its records. Classification runs before the
// decoder reads. A json.Decoder cannot rewind after a search for a "Records" key.
type jsonShape int

const (
	// jsonShapeArray is a top-level array of records: [{...},{...}].
	jsonShapeArray jsonShape = iota

	// jsonShapeRecordsWrapper is a single object whose "Records" key holds the array.
	jsonShapeRecordsWrapper

	// jsonShapeValueSequence is one top-level value after another. It covers
	// newline-delimited JSON, a lone object, and concatenated documents. A
	// json.Decoder reads all three the same way, so NDJSON needs no detection.
	jsonShapeValueSequence
)

// classifyJSON reads an object's shape from its leading bytes. It consumes nothing.
//
// The window matches the budget the "Records" search always used. A wrapper that holds
// the key deeper than the budget classifies as it did before.
func classifyJSON(reader BufferedReader) (jsonShape, error) {
	window, err := reader.Peek(maxRecordsSearchBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		// A broken source, a short download, or a truncated compression layer
		// (io.ErrUnexpectedEOF) surfaces here. A bare error is treated as transient and
		// redelivers deterministic content forever, so classify it: a broken or short
		// source retries, a content-truncated one delivers what was read and acks.
		return 0, classifyReadFailure(reader, err)
	}

	first, _, ok := firstMeaningfulByte(window)
	if !ok {
		return 0, ErrNotArrayOrKnownObject
	}

	switch first {
	case '[':
		return jsonShapeArray, nil
	case '{':
		// Decode the first value once. If it fits, reuse it to tell a records wrapper
		// from a value sequence without peeking the window again. If it overflows the
		// window, only a records wrapper (whose large array need not fit) is still
		// usable, found by token-streaming; a value that large fills memory, so a value
		// sequence goes back to the caller to fall back to line parsing. Consequence: an
		// NDJSON object whose FIRST value exceeds the window is delivered as line-parsed
		// string bodies rather than structured records. Documented limitation, by design.
		if first, fits := firstValue(window); fits {
			if opensRecordsArray(first) {
				return jsonShapeRecordsWrapper, nil
			}
			return jsonShapeValueSequence, nil
		}
		if opensRecordsArray(window) {
			return jsonShapeRecordsWrapper, nil
		}
		return 0, ErrNotArrayOrKnownObject
	default:
		return 0, ErrNotArrayOrKnownObject
	}
}

// firstValue decodes the first complete top-level value in window, reporting whether one
// ends inside the window. It separates a sequence of records from one oversized document:
// the sequence path holds each record in memory, so it runs only when the first record is
// small. The decoded value is returned so the caller can inspect it without re-decoding.
func firstValue(window []byte) (json.RawMessage, bool) {
	var first json.RawMessage
	if json.NewDecoder(bytes.NewReader(window)).Decode(&first) != nil {
		return nil, false
	}
	return first, true
}

// opensRecordsArray reports that window starts an object whose "Records" key holds an
// array. It decodes instead of matching text, so the word "Records" in a value does not
// match. The throwaway decoder reads only the peeked bytes.
func opensRecordsArray(window []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(window))

	tok, err := decoder.Token()
	if err != nil || tok != json.Delim('{') {
		return false
	}

	for {
		tok, err := decoder.Token()
		if err != nil {
			// A truncated window or the end of the object: no "Records" key in budget.
			return false
		}
		// A closing delimiter or a cut window is not a key, so no Records array opens.
		key, ok := tok.(string)
		if !ok {
			return false
		}

		if key != "Records" {
			if err := skipValue(decoder, maxRecordsSearchBytes); err != nil {
				return false
			}
			continue
		}

		tok, err = decoder.Token()
		if err != nil {
			return false
		}
		return tok == json.Delim('[')
	}
}

// Parse reads the JSON stream into a sequence of log records. The stream holds an array
// of records, an object with a "Records" array, or a sequence of top-level values.
//
// It returns ErrNotArrayOrKnownObject for any other content.
func (p *jsonParser) Parse(_ context.Context, startOffset int64) (logs iter.Seq2[any, error], err error) {
	shape, err := classifyJSON(p.reader)
	if err != nil {
		return nil, err
	}

	switch shape {
	case jsonShapeArray:
		// Step into the array so the loop below sees its elements.
		if _, err := p.decoder.Token(); err != nil {
			return nil, fmt.Errorf("read first token: %w", err)
		}

	case jsonShapeRecordsWrapper:
		if err := p.seekRecordsArray(); err != nil {
			return nil, err
		}

	case jsonShapeValueSequence:
		// Nothing to step into: the records are the top-level values themselves.
	}

	// A sequence of top-level values has no closing delimiter, so only the two
	// bracketed shapes can be checked for one.
	return p.yieldValues(startOffset, shape), nil
}

// seekRecordsArray advances the decoder into the "Records" array. It skips the other
// keys and decodes none of their values.
func (p *jsonParser) seekRecordsArray() error {
	// Bound the search relative to this object's start so a later concatenated document
	// is measured from its own beginning, not the cumulative stream offset.
	start := p.decoder.InputOffset()
	if _, err := p.decoder.Token(); err != nil {
		return fmt.Errorf("read first token: %w", err)
	}

	for p.decoder.More() {
		tok, err := p.decoder.Token()
		if err != nil {
			return fmt.Errorf("read token: %w", err)
		}
		// Inside an object the decoder only returns string keys, so the assertion
		// cannot fail. A zero value falls through to the skip below, which keeps the
		// decoder aligned.
		key, _ := tok.(string)

		if key != "Records" {
			if p.decoder.InputOffset()-start > maxRecordsSearchBytes {
				return ErrNotArrayOrKnownObject
			}
			// Bound the skipped value: one oversized token must not be buffered whole (an
			// OOM vector), and a value past the search budget means Records is not here.
			p.capped.arm(maxRecordsSearchBytes)
			err := skipValue(p.decoder, start+maxRecordsSearchBytes)
			p.capped.disarm()
			if errors.Is(err, errRecordTooLarge) {
				return ErrNotArrayOrKnownObject
			}
			if err != nil {
				return fmt.Errorf("skip value: %w", err)
			}
			continue
		}

		tok, err = p.decoder.Token()
		if err != nil {
			return fmt.Errorf("read token: %w", err)
		}
		if tok != json.Delim('[') {
			// "Records" exists but is not an array
			return ErrNotArrayOrKnownObject
		}
		return nil
	}

	return ErrNotArrayOrKnownObject
}

func (p *jsonParser) Offset() int64 {
	return p.baseOffset + p.decoder.InputOffset()
}

func skipValue(decoder *json.Decoder, maxBytes int64) error {
	if decoder.InputOffset() > maxBytes {
		return ErrNotArrayOrKnownObject
	}

	// Read the next token to determine what we're skipping
	tok, err := decoder.Token()
	if err != nil {
		return err
	}

	switch delim := tok.(type) {
	case json.Delim:
		// If it's a delimiter, we need to skip everything inside
		switch delim {
		case '{', '[':
			// For each opening, keep skipping values until we find the matching closing
			for decoder.More() {
				if err := skipValue(decoder, maxBytes); err != nil {
					return err
				}
			}
			// Consume the closing delimiter
			_, err := decoder.Token()
			return err
		}
	}
	// If it's not a delimiter, it's a primitive value, so nothing more to skip
	return nil
}

// yieldValues decodes one record at a time. decoder.More() reports another element or
// top-level value, so one loop drives an array and a sequence of documents.
func (p *jsonParser) yieldValues(startOffset int64, shape jsonShape) iter.Seq2[any, error] {
	// A value sequence has no closing delimiter; the bracketed shapes close with one.
	closes := shape != jsonShapeValueSequence
	return func(yield func(any, error) bool) {
		for {
			for p.decoder.More() {
				// RawMessage keeps each element's exact bytes for log.record.original.
				// AppendLogBody decodes the body from the same bytes. Each record is held
				// whole in memory, so the capped reader bounds a single Decode to the OOM
				// backstop; the exact max_log_size wall is enforced on the decoded size below.
				var record json.RawMessage
				startInput := p.decoder.InputOffset()
				currentOffset := p.baseOffset + startInput

				p.capped.arm(maxRecordBytesHardLimit)
				err := p.decoder.Decode(&record)
				p.capped.disarm()
				if err != nil {
					// The stream ran out. Whether that is the end of the array or a
					// cut is settled by the closing-delimiter check below.
					if errors.Is(err, io.EOF) {
						break
					}
					// Malformed bytes, or a value too large to decode within the OOM backstop,
					// are a per-record parse error. A json.Decoder cannot resync in place, so a
					// value sequence rebuilds the decoder past the bad line to keep the lines
					// after it; a bracketed shape has no delimiter to realign to after a
					// mid-decode failure, so it stops. A retry reads the same bytes.
					if isJSONStructureError(err) || errors.Is(err, errRecordTooLarge) {
						// Bracketed shapes stop here rather than resync: recovering the elements
						// after a bad one needs bracket-depth tracking that mis-fires on braces
						// and commas inside strings, so it is intentionally deferred for now.
						if closes {
							if currentOffset >= startOffset {
								if !yield(nil, fmt.Errorf("decode record: %w", err)) {
									return
								}
							}
							return
						}
						// A value sequence captures the bad line and resyncs past it. Malformed
						// bytes mark the file mixed; an over-size record does not.
						line, ok := p.resyncAfterNewline(false)
						if isJSONStructureError(err) {
							p.warnMixedOnce()
						}
						// A line that is text rather than broken JSON structure is a real string:
						// emit it as a string body. Corrupted JSON and an over-size record stay
						// parse errors. On resume a line below the checkpoint was already handled,
						// so gate the yield while still resyncing past it.
						if isJSONStructureError(err) && isTextLine(line) {
							if currentOffset >= startOffset {
								if !yield(rawTextLine(bytes.TrimRight(line, "\r")), nil) {
									return
								}
							}
							if !ok {
								// A broken or short source mid-capture is a retryable read
								// failure, not a clean end to be acked.
								if serr := p.boundaryStreamErr(); serr != nil {
									yield(nil, serr)
								}
								return
							}
							continue
						}
						if currentOffset >= startOffset {
							if !yield(nil, fmt.Errorf("decode record: %w", err)) {
								return
							}
						}
						if !ok {
							// The resync gave up. If the raw source broke or fell short (rather
							// than a clean end), that is a retryable read failure, not a clean
							// finish to be acked.
							if serr := p.boundaryStreamErr(); serr != nil {
								yield(nil, serr)
							}
							return
						}
						continue
					}
					// Otherwise sort a broken source stream (retry) from a truncated
					// object (deliver + ack) from content the decoder rejected.
					yield(nil, classifyReadFailure(p.reader, err))
					return
				}

				// if we haven't hit the start offset, skip the record
				if currentOffset < startOffset {
					continue
				}
				// max_log_size is a hard wall: reject a record whose decoded size exceeds it.
				// The decoder is aligned at the next record, so the loop continues and the
				// records after it still deliver (no dropped tail).
				if p.decoder.InputOffset()-startInput > recordSizeLimit(p.reader) {
					if !yield(nil, fmt.Errorf("decode record: exceeds max_log_size")) {
						return
					}
					continue
				}
				// A non-object value in a value sequence is not a record. Objects fall through
				// as records below, so same-line concatenated objects still parse.
				if !isJSONObject(record) {
					if shape != jsonShapeValueSequence {
						if !yield(nil, fmt.Errorf("decode record: expected a JSON object")) {
							return
						}
						continue
					}
					p.warnMixedOnce()
					// If the value consumed its whole line it is a bare scalar: a JSON string
					// keeps its unquoted body, anything else is emitted as its own text. If there
					// is trailing content the value was only the prefix of a text line (e.g. a
					// timestamp the decoder read as a number), so emit the whole line as text;
					// corrupted JSON structure is dropped.
					if !lineHasTrailing(p.decoder) {
						if isJSONString(record) {
							if !yield(record, nil) {
								return
							}
						} else if !yield(rawTextLine(string(record)), nil) {
							return
						}
						continue
					}
					trailing, ok := p.resyncAfterNewline(true)
					line := append(append([]byte{}, record...), trailing...)
					if isTextLine(line) {
						if !yield(rawTextLine(strings.TrimSpace(string(line))), nil) {
							return
						}
					} else if !yield(nil, fmt.Errorf("decode record: expected a JSON object")) {
						return
					}
					if !ok {
						// A broken or short source mid-capture is a retryable read failure.
						if serr := p.boundaryStreamErr(); serr != nil {
							yield(nil, serr)
						}
						return
					}
					continue
				}
				if !yield(record, nil) {
					return
				}
			}

			// A value sequence has no closing delimiter, so the loop ending is normally a
			// clean finish. json.Decoder.More() swallows the read error from its
			// look-ahead, though, so a source that breaks at a value boundary ends the
			// loop the same way. Consult the raw source: a broken or short download
			// retries; a clean end does not.
			if !closes {
				if serr := p.boundaryStreamErr(); serr != nil {
					yield(nil, serr)
				}
				return
			}

			// A bracketed shape closes with a delimiter; anything else means the bytes
			// ran out before the boundary.
			if _, err := p.decoder.Token(); err != nil {
				if p.reader.RawReadErr() != nil || p.reader.RawTruncated() {
					yield(nil, ErrStreamRead{Err: err})
				} else {
					yield(nil, ErrTruncatedObject{Err: err})
				}
				return
			}

			// A records wrapper still holds the object's remaining keys and closing brace
			// after its Records array; consume them to reach the next document. A failure
			// here can be the source breaking mid-tail, which the token reads swallow the
			// same way, so consult the raw source before ending on it.
			if shape == jsonShapeRecordsWrapper {
				if err := p.finishObject(); err != nil {
					if serr := p.boundaryStreamErr(); serr != nil {
						yield(nil, serr)
					} else {
						// A non-stream failure to consume the wrapper's tail (for example
						// trailing keys past the search limit). Count it as one parse error
						// rather than silently dropping the documents concatenated after it. A
						// non-sentinel error so the worker skips this document and acks, rather
						// than re-reading the whole object as lines. It re-counts on a
						// redelivery (the checkpoint cannot advance past a non-delivering tail),
						// which is accepted as metric-only.
						yield(nil, fmt.Errorf("%w: %v", errTrailingRecordsWrapper, err))
					}
					return
				}
			}

			// Documents may be concatenated (for example one array file appended to
			// another, or several {"Records": [...]} objects in a row). Continue into the
			// next document of the same shape; a clean end stops the loop. More() swallows
			// the boundary read error, so a source that broke or fell short right here
			// otherwise looks like a clean end; consult the raw source.
			if !p.decoder.More() {
				if serr := p.boundaryStreamErr(); serr != nil {
					yield(nil, serr)
				}
				return
			}
			// More() reported another document. A break while reading its prefix is a
			// stream failure (retryable), not deterministic garbage, so consult the raw
			// source first; otherwise count it as one parse error, gated so a redelivery
			// past this point does not re-count it, and the object still acks.
			switch shape {
			case jsonShapeArray:
				if _, err := p.decoder.Token(); err != nil {
					if serr := p.boundaryStreamErr(); serr != nil {
						yield(nil, serr)
					} else {
						yield(nil, fmt.Errorf("decode record: %w", err))
					}
					return
				}
			case jsonShapeRecordsWrapper:
				if err := p.seekRecordsArray(); err != nil {
					if serr := p.boundaryStreamErr(); serr != nil {
						yield(nil, serr)
					} else {
						// A trailing document that is not a records wrapper. Wrapping
						// ErrNotArrayOrKnownObject would make the worker re-read the whole
						// object as lines and re-emit delivered records, so use a non-sentinel
						// error: the object acks with this document counted and skipped.
						yield(nil, fmt.Errorf("%w: %v", errTrailingRecordsWrapper, err))
					}
					return
				}
			}
		}
	}
}

// finishObject consumes the remaining keys and the closing brace of the object the
// decoder is currently inside, after its records have been read, so the decoder is left
// positioned at the next concatenated document.
func (p *jsonParser) finishObject() error {
	start := p.decoder.InputOffset()
	for p.decoder.More() {
		if _, err := p.decoder.Token(); err != nil {
			return err
		}
		// Bound the skipped tail value so an oversized token is not buffered whole.
		p.capped.arm(maxRecordsSearchBytes)
		err := skipValue(p.decoder, start+maxRecordsSearchBytes)
		p.capped.disarm()
		if err != nil {
			return err
		}
	}
	_, err := p.decoder.Token() // the closing '}'
	return err
}

// boundaryStreamErr reports the raw source's state at a document boundary. json.Decoder's
// More() and its boundary token reads swallow the underlying read error from their
// look-ahead, so a source that broke or fell short of its known size at a boundary looks
// like a clean end. It returns a retryable ErrStreamRead in that case, or nil when the
// stream truly ended cleanly.
func (p *jsonParser) boundaryStreamErr() error {
	if rerr := p.reader.RawReadErr(); rerr != nil {
		return ErrStreamRead{Err: rerr}
	}
	if p.reader.RawTruncated() {
		return ErrStreamRead{Err: io.ErrUnexpectedEOF}
	}
	return nil
}

// resyncAfterNewline rebuilds the decoder to read from just past the newline that ends
// the current malformed line, so the lines after it still parse. A json.Decoder cannot
// resync in place, so the remaining input (its read-ahead buffer plus the source) is
// re-wrapped in a fresh decoder. baseOffset is advanced by the discarded bytes so Offset()
// stays absolute for resume. It returns the discarded line's content (from its first
// non-space byte, so the caller can classify text vs corrupted JSON) and whether a
// terminating newline was found; on EOF it returns the content read with ok=false.
func (p *jsonParser) resyncAfterNewline(keepLeading bool) (line []byte, ok bool) {
	base := p.baseOffset + p.decoder.InputOffset()
	remaining := bufio.NewReader(io.MultiReader(p.decoder.Buffered(), p.src))

	var discarded int64
	var content []byte
	// keepLeading captures from the current byte (for trailing after a decoded value, where
	// leading whitespace is part of the line); otherwise leading whitespace and separator
	// newlines before the malformed content are skipped.
	sawContent := keepLeading
	for {
		b, err := remaining.ReadByte()
		if err != nil {
			// The source ended without a terminating newline. Return the content read so a
			// final unterminated line can still be handled; ok=false means nothing remains.
			return content, false
		}
		discarded++
		switch b {
		case ' ', '\t', '\r':
			// Leading whitespace is a separator; interior whitespace is part of the line.
			if sawContent {
				content = append(content, b)
			}
		case '\n':
			// A newline before any content is the separator ending the prior record;
			// keep going. A newline after content ends the malformed line: resume there.
			if sawContent {
				p.baseOffset = base + discarded
				// Rebuild the decoder over the capped reader so the per-record size bound
				// still applies after a resync.
				p.capped.r = remaining
				p.decoder = json.NewDecoder(p.capped)
				// The next resync continues from this reader, which has read ahead past
				// what the decoder has consumed, so it must not fall back to p.reader.
				p.src = remaining
				return content, true
			}
		default:
			sawContent = true
			content = append(content, b)
		}
	}
}

// isTextLine reports that a captured value-sequence line is plain text rather than broken
// JSON structure. A line whose first byte opens an object or array is treated as corrupted
// JSON; anything else is a real text line.
func isTextLine(line []byte) bool {
	return len(line) > 0 && line[0] != '{' && line[0] != '['
}

// lineHasTrailing reports whether non-whitespace remains before the next newline after the
// value just decoded, i.e. the value was only the prefix of a longer text line. It reads the
// decoder's already-buffered bytes, which does not disturb the decoder's own position. If the
// buffer ends before a newline is seen, it reports no trailing (the common line fits the
// buffer; a value split across a buffer refill is treated as a clean whole-line value).
func lineHasTrailing(dec *json.Decoder) bool {
	buf := dec.Buffered()
	b := make([]byte, 1)
	for {
		n, err := buf.Read(b)
		if n == 0 || err != nil {
			return false
		}
		switch b[0] {
		case ' ', '\t', '\r':
		case '\n':
			return false
		default:
			return true
		}
	}
}

// rawTextLine is a value-sequence line that is text rather than JSON. AppendLogBody emits it
// as a plain string body, preserving the original line in both raw and structured modes.
type rawTextLine string

// AppendLogBody appends the log record to the log record body using FromRaw, decoding
// the element's original bytes. In raw mode the original text becomes the body instead.
func (p *jsonParser) AppendLogBody(_ context.Context, lr plog.LogRecord, record any) error {
	// A recovered text line is already a string; emit it verbatim as the body.
	if txt, ok := record.(rawTextLine); ok {
		lr.Body().SetStr(string(txt))
		p.opts.setOriginal(lr, string(txt))
		return nil
	}

	raw, ok := record.(json.RawMessage)
	if !ok {
		return fmt.Errorf("expected json record, got %T", record)
	}

	original := string(raw)

	// Raw mode emits each record's original text as the body, keeping the per-record
	// split that structured parsing produces rather than the parsed structure. The
	// original is this element's exact bytes, never the whole multi-record document.
	if p.opts.Raw {
		lr.Body().SetStr(original)
		p.opts.setOriginal(lr, original)
		return nil
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("decode record body: %w", err)
	}
	// Dropped, not returned. The Unmarshal above rejects bad input, so this only sees
	// types FromRaw handles. It documents no error cases, so that rests on its current
	// behaviour: https://pkg.go.dev/go.opentelemetry.io/collector/pdata/pcommon#Value.FromRaw
	_ = lr.Body().FromRaw(decoded)
	p.opts.setOriginal(lr, original)
	return nil
}

// isJSONObject reports that raw is a JSON object. A successful Decode into a RawMessage
// trims leading whitespace, so the first byte is the value's opening token. Array
// elements and top-level values that are scalars or arrays are not records this receiver
// emits.
func isJSONObject(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '{'
}

// isJSONString reports that raw is a JSON string value, judged from its opening token.
func isJSONString(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '"'
}

// warnMixedOnce warns once per file that this value sequence mixes objects with non-object
// lines. It is a no-op without a logger.
func (p *jsonParser) warnMixedOnce() {
	if p.logger == nil || p.warnedMixed {
		return
	}
	p.warnedMixed = true
	p.logger.Warn(mixedSequenceLogMsg)
}

// isJSONStructureError reports that the bytes themselves are malformed, rather than the
// read failing. encoding/json returns its own error types for content it cannot parse
// and passes a reader's error through untouched.
func isJSONStructureError(err error) bool {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return true
	}
	var unmarshalType *json.UnmarshalTypeError
	return errors.As(err, &unmarshalType)
}
