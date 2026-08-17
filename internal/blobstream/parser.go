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
	"context"
	"fmt"
	"iter"

	"github.com/gabriel-vasile/mimetype"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// ErrUnsupportedContent is returned when the decoded content is a recognized but
// unsupported type (for example an image or a PDF). It carries the detected MIME
// type and maps to the unsupported-file DLQ condition.
type ErrUnsupportedContent struct {
	MIMEType string
}

func (e ErrUnsupportedContent) Error() string {
	return fmt.Sprintf("unsupported content type: %s", e.MIMEType)
}

// LogParser is an interface that can parse a log stream into a sequence of log records
// and can also append a single log body to a LogRecord.
type LogParser interface {
	// Parse parses the log stream into a sequence of log records. The parser should return
	// an error if the stream is not valid.
	Parse(ctx context.Context, startOffset int64) (logs iter.Seq2[any, error], err error)

	// AppendLogBody appends a single log body to a LogRecord. Different parsers may result
	// in different log bodies so this is the responsibility of the parser.
	AppendLogBody(ctx context.Context, lr plog.LogRecord, record any) error

	// Offset returns the current offset of the log stream.
	Offset() int64
}

func newParser(stream LogStream, reader BufferedReader) (parser LogParser, err error) {
	opts := stream.bodyOptions()

	// if we're not trying to decode, use the line parser
	if !stream.TryDecoding {
		return NewLineParser(reader, opts), nil
	}

	// check for avro first
	if isAvroOcf(stream, reader) {
		// Avro runs before the raw check. It is binary and holds no original text, so
		// raw mode takes the JSON encoding of each record instead. See BodyOptions.
		return NewAvroOcfParser(reader, stream.Logger, opts), nil
	}

	// Raw mode skips parser selection, and still runs the content gate below. Without
	// the gate an image emits as garbled lines.
	if opts.Raw {
		return lineParserIfText(reader, opts)
	}

	// check for json
	if isJSON(stream, reader) {
		return NewJSONParser(reader, opts), nil
	}

	// Terminal: the content is neither Avro nor JSON.
	return lineParserIfText(reader, opts)
}

// lineParserIfText returns a line parser for text. It rejects non-text content, which
// then goes to the DLQ instead of emitting garbled lines.
func lineParserIfText(reader BufferedReader, opts BodyOptions) (LogParser, error) {
	header, _ := reader.Peek(detectionPeekBytes)
	if len(header) == 0 {
		// Empty object: nothing to parse, but not an error.
		return NewLineParser(reader, opts), nil
	}
	detected := mimetype.Detect(header)
	if isTextMIME(detected) {
		return NewLineParser(reader, opts), nil
	}
	return nil, ErrUnsupportedContent{MIMEType: detected.String()}
}

// isTextMIME reports whether the detected type is textual by walking its parent
// chain up to text/plain (mimetype models text formats as descendants of it).
func isTextMIME(mt *mimetype.MIME) bool {
	for m := mt; m != nil; m = m.Parent() {
		if m.Is("text/plain") {
			return true
		}
	}
	return false
}

// isJSON reports whether the stream content is JSON. Detection is content-only:
// the object name and content type are not consulted, because customers routinely
// store JSON under a wrong or missing extension and GCS reports a generic content
// type such as application/octet-stream.
// A failed probe is not an error: detection is best effort, and unrecognized content
// falls through to line parsing.
func isJSON(stream LogStream, reader BufferedReader) bool {
	startsWithJSONObjectOrArray, err := StartsWithJSONObjectOrArray(reader)
	if err != nil {
		stream.Logger.Warn("failed to check if starts with json object or array", zap.Error(err))
		return false
	}

	return startsWithJSONObjectOrArray
}

// isAvroOcf reports whether the stream content is Avro OCF, based solely on the
// object's leading magic bytes.
// A failed probe is not an error: detection is best effort, and unrecognized content
// falls through to line parsing.
func isAvroOcf(stream LogStream, reader BufferedReader) bool {
	startsWithAvroOcfMagic, err := StartsWithAvroOcfMagic(reader)
	if err != nil {
		stream.Logger.Warn("failed to check if starts with avro ocf magic", zap.Error(err))
		return false
	}

	return startsWithAvroOcfMagic
}
