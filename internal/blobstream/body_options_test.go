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

// Internal test file — uses package blobstream to access unexported symbols.
package blobstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

const jsonArrayObject = `[
  {"host":"a","msg":"first"},
  {"host":"b","msg":"second"}
]
`

// collectBodies runs a producer over the stream. It returns each record body and each
// log.record.original attribute.
func collectBodies(t *testing.T, stream LogStream) (bodies []any, originals []string) {
	t.Helper()

	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	producer, err := NewRecordProducer(context.Background(), stream, reader, nil)
	require.NoError(t, err)

	records, err := producer.Records(context.Background(), Offset{})
	require.NoError(t, err)

	for record, err := range records {
		require.NoError(t, err)

		lr := plog.NewLogRecord()
		require.NoError(t, producer.AppendLogBody(context.Background(), lr, record))
		bodies = append(bodies, lr.Body().AsRaw())

		if original, ok := lr.Attributes().Get(logRecordOriginalAttribute); ok {
			originals = append(originals, original.Str())
		}
	}
	return bodies, originals
}

func newTestStream(body string, raw, includeOriginal bool) LogStream {
	return LogStream{
		Name:                     "object",
		Body:                     io.NopCloser(strings.NewReader(body)),
		MaxLogSize:               4096,
		Logger:                   zap.NewNop(),
		TryDecoding:              true,
		Raw:                      raw,
		IncludeLogRecordOriginal: includeOriginal,
	}
}

// TestDefaults_ParseAndNoOriginal asserts a JSON array parses into structured bodies,
// and adds no log.record.original attribute.
func TestDefaults_ParseAndNoOriginal(t *testing.T) {
	t.Parallel()

	bodies, originals := collectBodies(t, newTestStream(jsonArrayObject, false, false))

	require.Len(t, bodies, 2, "a JSON array parses into one record per element")
	require.Equal(t, map[string]any{"host": "a", "msg": "first"}, bodies[0])
	require.Empty(t, originals, "log.record.original is opt-in")
}

// TestRaw_EmitsOriginalTextInsteadOfParsedStructure asserts raw mode emits each record's
// original text as the body, split into the same records structured parsing produces. A
// JSON array yields one record per element (its exact bytes), not the array's raw lines:
// a record whose body is "[" or "]" is meaningless to a downstream consumer.
func TestRaw_EmitsOriginalTextInsteadOfParsedStructure(t *testing.T) {
	t.Parallel()

	bodies, _ := collectBodies(t, newTestStream(jsonArrayObject, true, false))

	require.Equal(t, []any{
		`{"host":"a","msg":"first"}`,
		`{"host":"b","msg":"second"}`,
	}, bodies, "raw mode emits each element's original text as its own record")
}

// TestRaw_StillRejectsUnsupportedContent asserts raw mode keeps the content gate.
// Without it an image emits as garbled lines instead of going to the DLQ.
func TestRaw_StillRejectsUnsupportedContent(t *testing.T) {
	t.Parallel()

	// A minimal PNG header is enough for content detection.
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0}, 64)...)

	stream := newTestStream(string(png), true, false)
	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	_, err = NewRecordProducer(context.Background(), stream, reader, nil)
	require.Error(t, err)
	require.True(t, IsUnsupportedContent(err), "raw mode must still route binary content to the DLQ")
}

// TestIncludeLogRecordOriginal_ParsedJSON asserts a parsed record keeps the exact
// original bytes with the structured body.
func TestIncludeLogRecordOriginal_ParsedJSON(t *testing.T) {
	t.Parallel()

	bodies, originals := collectBodies(t, newTestStream(jsonArrayObject, false, true))

	require.Len(t, bodies, 2)
	require.Equal(t, map[string]any{"host": "a", "msg": "first"}, bodies[0],
		"the body stays parsed; the original is additional")
	require.Equal(t, []string{
		`{"host":"a","msg":"first"}`,
		`{"host":"b","msg":"second"}`,
	}, originals)
}

// TestIncludeLogRecordOriginal_Lines asserts the option applies to line-parsed
// objects. The body and the original match.
func TestIncludeLogRecordOriginal_Lines(t *testing.T) {
	t.Parallel()

	bodies, originals := collectBodies(t, newTestStream("first line\nsecond line\n", false, true))

	require.Equal(t, []any{"first line", "second line"}, bodies)
	require.Equal(t, []string{"first line", "second line"}, originals)
}

// TestRaw_WithIncludeLogRecordOriginal asserts the two options work together. The body
// and the attribute both hold the original text.
func TestRaw_WithIncludeLogRecordOriginal(t *testing.T) {
	t.Parallel()

	bodies, originals := collectBodies(t, newTestStream("first line\nsecond line\n", true, true))

	require.Equal(t, []any{"first line", "second line"}, bodies)
	require.Equal(t, []string{"first line", "second line"}, originals)
}

// TestRaw_JSONArrayWithIncludeOriginal asserts that for a JSON array in raw mode both the
// body and the log.record.original attribute hold each element's original text — one
// record per element, never the whole multi-record document.
func TestRaw_JSONArrayWithIncludeOriginal(t *testing.T) {
	t.Parallel()

	bodies, originals := collectBodies(t, newTestStream(jsonArrayObject, true, true))

	require.Equal(t, []any{
		`{"host":"a","msg":"first"}`,
		`{"host":"b","msg":"second"}`,
	}, bodies, "raw body is each element's original text")
	require.Equal(t, []string{
		`{"host":"a","msg":"first"}`,
		`{"host":"b","msg":"second"}`,
	}, originals, "log.record.original is each element's original text, not the whole document")
}

// TestBodyOptions_ApplyInsideArchives asserts the options reach archive entries. Each
// entry runs format detection with a new stream, so the options must cross that edge.
func TestBodyOptions_ApplyInsideArchives(t *testing.T) {
	t.Parallel()

	archive := tarBytes(t, []tarFile{{name: "logs.json", body: []byte(jsonArrayObject)}})

	stream := LogStream{
		Name:                     "logs.tar",
		Body:                     io.NopCloser(bytes.NewReader(archive)),
		MaxLogSize:               4096,
		Logger:                   zap.NewNop(),
		TryDecoding:              true,
		IncludeLogRecordOriginal: true,
	}

	bodies, originals := collectBodies(t, stream)

	require.Equal(t, map[string]any{"host": "a", "msg": "first"}, bodies[0])
	require.Equal(t, []string{
		`{"host":"a","msg":"first"}`,
		`{"host":"b","msg":"second"}`,
	}, originals, "archive entries should honour include_log_record_original")
}

// TestRaw_AvroEmitsJSONText asserts raw mode gives Avro a text body rather than
// rejecting it. Avro OCF is binary and has no original text, so the JSON encoding of
// each record is the only text form available.
func TestRaw_AvroEmitsJSONText(t *testing.T) {
	t.Parallel()

	file, err := os.Open("testdata/sample_logs.avro")
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	stream := LogStream{
		Name:                     "sample_logs.avro",
		Body:                     file,
		MaxLogSize:               4096,
		Logger:                   zap.NewNop(),
		TryDecoding:              true,
		Raw:                      true,
		IncludeLogRecordOriginal: true,
	}

	bodies, originals := collectBodies(t, stream)

	require.Len(t, bodies, 10)
	for i, body := range bodies {
		text, ok := body.(string)
		require.True(t, ok, "record %d should be text, got %T", i, body)
		require.True(t, json.Valid([]byte(text)), "record %d should be valid JSON", i)
	}
	require.Equal(t, bodies[0], any(originals[0]),
		"the attribute should carry the same text as the body")
}
