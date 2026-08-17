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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"

	"go.opentelemetry.io/collector/pdata/plog"
)

var (
	// ErrNotArrayOrKnownObject is returned when the JSON stream is not a valid array or object
	// with a known key. When this occurs, try to parse as text.
	ErrNotArrayOrKnownObject = errors.New("expected array or object with known key")
)

const (
	// maxRecordsSearchBytes is the maximum number of bytes to search for a "Records" key in
	// the first 4096 bytes of the JSON stream. This is to avoid parsing the entire file
	// looking for a "Records" key and not finding it.
	maxRecordsSearchBytes = 4096
)

type jsonParser struct {
	reader  BufferedReader
	decoder *json.Decoder
	opts    BodyOptions
}

var _ LogParser = (*jsonParser)(nil)

// NewJSONParser creates a new JSON parser.
func NewJSONParser(reader BufferedReader, opts BodyOptions) LogParser {
	return &jsonParser{
		reader:  reader,
		decoder: json.NewDecoder(reader),
		opts:    opts,
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
		return 0, fmt.Errorf("peek: %w", err)
	}

	first, _, ok := firstMeaningfulByte(window)
	if !ok {
		return 0, ErrNotArrayOrKnownObject
	}

	switch first {
	case '[':
		return jsonShapeArray, nil
	case '{':
		if opensRecordsArray(window) {
			return jsonShapeRecordsWrapper, nil
		}
		if firstValueFitsInWindow(window) {
			return jsonShapeValueSequence, nil
		}
		// A document too large for the window goes back to the caller, which falls
		// back to line parsing. One value that large fills memory.
		return 0, ErrNotArrayOrKnownObject
	default:
		return 0, ErrNotArrayOrKnownObject
	}
}

// firstValueFitsInWindow reports that a complete top-level value ends inside window.
//
// It separates a sequence of records from one oversized document. The sequence path
// holds each record in memory, so it runs only when the first record is small.
func firstValueFitsInWindow(window []byte) bool {
	var first json.RawMessage
	return json.NewDecoder(bytes.NewReader(window)).Decode(&first) == nil
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
	return p.yieldValues(startOffset, shape != jsonShapeValueSequence), nil
}

// seekRecordsArray advances the decoder into the "Records" array. It skips the other
// keys and decodes none of their values.
func (p *jsonParser) seekRecordsArray() error {
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
			if p.decoder.InputOffset() > maxRecordsSearchBytes {
				return ErrNotArrayOrKnownObject
			}
			if err := skipValue(p.decoder, maxRecordsSearchBytes); err != nil {
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
	return p.decoder.InputOffset()
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
func (p *jsonParser) yieldValues(startOffset int64, closes bool) iter.Seq2[any, error] {
	return func(yield func(any, error) bool) {
		for p.decoder.More() {
			// RawMessage keeps each element's exact bytes for log.record.original.
			// AppendLogBody decodes the body from the same bytes.
			var record json.RawMessage
			currentOffset := p.decoder.InputOffset()

			if err := p.decoder.Decode(&record); err != nil {
				// The stream ran out. Whether that is the end of the array or a
				// cut is settled by the closing-delimiter check below.
				if errors.Is(err, io.EOF) {
					break
				}
				// The object stops part way through a record. The missing bytes
				// were never written, so a retry reads the same thing.
				if errors.Is(err, io.ErrUnexpectedEOF) {
					yield(nil, ErrTruncatedObject{Err: err})
					return
				}
				// Both cases below are terminal. A json.Decoder cannot resync, so
				// every later Decode fails on the same byte while More() reports
				// more input.
				//
				// Malformed bytes stay a record error, because a retry reads the
				// same bytes. A broken stream is marked, because the rest of the
				// object is still readable later.
				if isJSONStructureError(err) {
					yield(nil, fmt.Errorf("decode record: %w", err))
					return
				}
				yield(nil, ErrStreamRead{Err: err})
				return
			}

			// if we haven't hit the start offset, skip the record
			if currentOffset < startOffset {
				continue
			}
			if !yield(record, nil) {
				return
			}
		}
		// The loop above also ends when the object stops part way through, because
		// More reports no further element either way. A bracketed shape closes with a
		// delimiter, so anything else there means the bytes ran out early.
		if !closes {
			return
		}
		if _, err := p.decoder.Token(); err != nil {
			yield(nil, ErrTruncatedObject{Err: err})
		}
	}
}

// AppendLogBody appends the log record to the log record body using FromRaw, decoding
// the element's original bytes. In raw mode the original text becomes the body instead.
func (p *jsonParser) AppendLogBody(_ context.Context, lr plog.LogRecord, record any) error {
	raw, ok := record.(json.RawMessage)
	if !ok {
		return fmt.Errorf("expected json record, got %T", record)
	}

	original := string(raw)
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
