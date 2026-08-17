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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
)

const ndjsonObject = `{"host":"a","msg":"first"}
{"host":"b","msg":"second"}
{"host":"c","msg":"third"}
`

// TestNDJSON_ParsesEachLineAsARecord asserts the parser parses newline-delimited JSON
// instead of emitting text. Before, JSON detection failed and the worker re-read the
// object with the line parser.
func TestNDJSON_ParsesEachLineAsARecord(t *testing.T) {
	t.Parallel()

	bodies, originals := collectBodies(t, newTestStream(ndjsonObject, false, false))

	require.Equal(t, []any{
		map[string]any{"host": "a", "msg": "first"},
		map[string]any{"host": "b", "msg": "second"},
		map[string]any{"host": "c", "msg": "third"},
	}, bodies)
	require.Empty(t, originals)
}

// TestNDJSON_RawEmitsTheOriginalLines asserts the raw option overrides NDJSON parsing.
// SecOps pipelines then receive the original line.
func TestNDJSON_RawEmitsTheOriginalLines(t *testing.T) {
	t.Parallel()

	bodies, _ := collectBodies(t, newTestStream(ndjsonObject, true, false))

	require.Equal(t, []any{
		`{"host":"a","msg":"first"}`,
		`{"host":"b","msg":"second"}`,
		`{"host":"c","msg":"third"}`,
	}, bodies)
}

// TestNDJSON_IncludeLogRecordOriginal asserts a parsed NDJSON record carries its
// original line with the structured body.
func TestNDJSON_IncludeLogRecordOriginal(t *testing.T) {
	t.Parallel()

	bodies, originals := collectBodies(t, newTestStream(ndjsonObject, false, true))

	require.Equal(t, map[string]any{"host": "a", "msg": "first"}, bodies[0])
	require.Equal(t, []string{
		`{"host":"a","msg":"first"}`,
		`{"host":"b","msg":"second"}`,
		`{"host":"c","msg":"third"}`,
	}, originals)
}

// TestNDJSON_MalformedRecordTerminatesTheStream asserts a malformed record stops the
// read instead of spinning.
//
// A json.Decoder cannot resync after a syntax error. Every later Decode fails on the
// same byte, and More() reports more input. Records before the error still arrive.
func TestNDJSON_MalformedRecordTerminatesTheStream(t *testing.T) {
	t.Parallel()

	const withBadLine = `{"host":"a"}
{"host":"b"}
this line is not json
{"host":"c"}
`
	stream := newTestStream(withBadLine, false, false)
	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	producer, err := NewRecordProducer(context.Background(), stream, reader, nil)
	require.NoError(t, err)

	records, err := producer.Records(context.Background(), Offset{})
	require.NoError(t, err)

	var bodies []any
	var errs []error
	iterations := 0
	exhausted := false
	for record, err := range records {
		iterations++
		if iterations > 1000 {
			exhausted = true
			break
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		lr := plog.NewLogRecord()
		require.NoError(t, producer.AppendLogBody(context.Background(), lr, record))
		bodies = append(bodies, lr.Body().AsRaw())
	}

	require.False(t, exhausted, "a wedged decoder must not spin on the same error")
	require.Equal(t, []any{
		map[string]any{"host": "a"},
		map[string]any{"host": "b"},
	}, bodies, "records decoded before the corruption are still delivered")
	require.Len(t, errs, 1, "the decode error should be surfaced exactly once")
}

// TestNDJSON_DoesNotClaimSingleObjectForms asserts detection stays narrow. A one-line
// Records wrapper and a pretty-printed object still use the JSON parser.
func TestNDJSON_DoesNotClaimSingleObjectForms(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
		want []any
	}{
		{
			name: "single-line Records wrapper",
			body: `{"Records":[{"host":"a"},{"host":"b"}]}`,
			want: []any{map[string]any{"host": "a"}, map[string]any{"host": "b"}},
		},
		{
			name: "pretty-printed array of objects",
			body: "[\n  {\"host\":\"a\"},\n  {\"host\":\"b\"}\n]\n",
			want: []any{map[string]any{"host": "a"}, map[string]any{"host": "b"}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bodies, _ := collectBodies(t, newTestStream(tc.body, false, false))
			require.Equal(t, tc.want, bodies)
		})
	}
}

// TestJSONShapes covers the three layouts the parser reads. Detection previously
// stopped at "is it an array", so some layouts fell through to line parsing.
func TestJSONShapes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
		want []any
	}{
		{
			name: "top-level array",
			body: `[{"host":"a"},{"host":"b"}]`,
			want: []any{map[string]any{"host": "a"}, map[string]any{"host": "b"}},
		},
		{
			name: "Records wrapper",
			body: `{"other":"ignored","Records":[{"host":"a"},{"host":"b"}]}`,
			want: []any{map[string]any{"host": "a"}, map[string]any{"host": "b"}},
		},
		{
			name: "newline-delimited objects",
			body: "{\"host\":\"a\"}\n{\"host\":\"b\"}\n",
			want: []any{map[string]any{"host": "a"}, map[string]any{"host": "b"}},
		},
		{
			name: "concatenated pretty-printed documents",
			body: "{\n  \"host\": \"a\"\n}\n{\n  \"host\": \"b\"\n}\n",
			want: []any{map[string]any{"host": "a"}, map[string]any{"host": "b"}},
		},
		{
			name: "single object",
			body: `{"host":"a"}`,
			want: []any{map[string]any{"host": "a"}},
		},
		{
			// "Records" as a value must not match the wrapper key. Classification
			// decodes instead of matching text.
			name: "object mentioning Records as a value",
			body: "{\"msg\":\"see Records for details\"}\n{\"msg\":\"second\"}\n",
			want: []any{
				map[string]any{"msg": "see Records for details"},
				map[string]any{"msg": "second"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bodies, _ := collectBodies(t, newTestStream(tc.body, false, false))
			require.Equal(t, tc.want, bodies)
		})
	}
}

// TestJSON_OversizedDocumentIsNotDecodedAsOneValue asserts the parser sends an object
// too large for the peek window to line parsing.
//
// The sequence path holds each record in memory. A large document there fills memory.
// The line parser caps each record at max_log_size.
func TestJSON_OversizedDocumentIsNotDecodedAsOneValue(t *testing.T) {
	t.Parallel()

	// One object whose first key alone runs past the classification window.
	padding := strings.Repeat("x", maxRecordsSearchBytes*2)
	body := `{"padding":"` + padding + `","host":"a"}`

	stream := newTestStream(body, false, false)
	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	producer, err := NewRecordProducer(context.Background(), stream, reader, nil)
	require.NoError(t, err)

	// The worker reads this error as a signal to re-read the object with the line
	// parser, which caps each record at max_log_size.
	_, err = producer.Records(context.Background(), Offset{})
	require.ErrorIs(t, err, ErrNotArrayOrKnownObject,
		"an oversized document should fall through to line parsing rather than being decoded whole")
}
