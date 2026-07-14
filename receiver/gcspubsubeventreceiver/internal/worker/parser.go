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

func newParser(ctx context.Context, stream LogStream, reader BufferedReader) (parser LogParser, err error) {
	// if we're not trying to decode, use the line parser
	if !stream.TryDecoding {
		return NewLineParser(reader), nil
	}

	// check for avro first
	isAvro, err := isAvroOcf(ctx, stream, reader)
	if err != nil {
		stream.Logger.Warn("failed to check if is avro", zap.Error(err))
		isAvro = false
	}
	if isAvro {
		return NewAvroOcfParser(reader, stream.Logger), nil
	}

	// check for json
	isJSON, err := isJSON(ctx, stream, reader)
	if err != nil {
		// don't fail if the file is not json
		stream.Logger.Warn("failed to check if is json", zap.Error(err))
		isJSON = false
	}

	if isJSON {
		return NewJSONParser(reader), nil
	}

	// Terminal: the content is neither Avro nor JSON. Recognized text is parsed
	// line by line; recognized non-text content (an image, a PDF, an unknown
	// binary) is rejected so it lands in the DLQ instead of being emitted as
	// garbled lines.
	header, _ := reader.Peek(detectionPeekBytes)
	if len(header) == 0 {
		// Empty object: nothing to parse, but not an error.
		return NewLineParser(reader), nil
	}
	detected := mimetype.Detect(header)
	if isTextMIME(detected) {
		return NewLineParser(reader), nil
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
func isJSON(_ context.Context, stream LogStream, reader BufferedReader) (bool, error) {
	startsWithJSONObjectOrArray, err := StartsWithJSONObjectOrArray(reader)
	if err != nil {
		stream.Logger.Warn("failed to check if starts with json object or array", zap.Error(err))
		return false, nil
	}

	return startsWithJSONObjectOrArray, nil
}

// isAvroOcf reports whether the stream content is Avro OCF, based solely on the
// object's leading magic bytes.
func isAvroOcf(_ context.Context, stream LogStream, reader BufferedReader) (bool, error) {
	startsWithAvroOcfMagic, err := StartsWithAvroOcfMagic(reader)
	if err != nil {
		stream.Logger.Warn("failed to check if starts with avro ocf magic", zap.Error(err))
		return false, nil
	}

	return startsWithAvroOcfMagic, nil
}
