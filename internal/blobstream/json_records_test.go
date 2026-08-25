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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// driveJSON runs a body through the producer and returns each record body.
func driveJSON(t *testing.T, body string) ([]any, error) {
	t.Helper()
	ctx := context.Background()

	stream := detectionStream(body, zap.NewNop(), true)
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	if err != nil {
		return nil, err
	}

	seq, err := producer.Records(ctx, Offset{})
	if err != nil {
		return nil, err
	}

	var bodies []any
	for rec, rerr := range seq {
		if rerr != nil {
			return bodies, rerr
		}
		lr := plog.NewLogRecord()
		require.NoError(t, producer.AppendLogBody(ctx, lr, rec))
		bodies = append(bodies, lr.Body().AsRaw())
	}
	return bodies, nil
}

// TestJSONRecords_ReadsSupportedLayouts covers the layouts the parser accepts. The
// wrapper cases place each value shape ahead of the "Records" key, so the search walks
// past all of them before it finds the array.
func TestJSONRecords_ReadsSupportedLayouts(t *testing.T) {
	t.Parallel()

	want := []any{map[string]any{"host": "a"}, map[string]any{"host": "b"}}
	records := `[{"host":"a"},{"host":"b"}]`

	testCases := []struct {
		name string
		body string
		want []any
	}{
		{name: "top level array", body: records, want: want},
		{name: "records first", body: `{"Records":` + records + `}`, want: want},
		{name: "after a string", body: `{"owner":"team","Records":` + records + `}`, want: want},
		{name: "after a number", body: `{"count":2,"Records":` + records + `}`, want: want},
		{name: "after a boolean", body: `{"final":true,"Records":` + records + `}`, want: want},
		{name: "after a null", body: `{"next":null,"Records":` + records + `}`, want: want},
		{name: "after a nested object", body: `{"meta":{"a":{"b":[1,2]}},"Records":` + records + `}`, want: want},
		{name: "after a nested array", body: `{"tags":[{"k":"v"},[1,2]],"Records":` + records + `}`, want: want},
		{name: "empty array", body: `[]`, want: nil},
		{name: "empty records array", body: `{"Records":[]}`, want: nil},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bodies, err := driveJSON(t, tc.body)
			require.NoError(t, err)
			require.Equal(t, tc.want, bodies)
		})
	}
}

// TestJSONRecords_ReadsSingleValueLayouts covers the shapes the "Records" search alone
// rejected. Each is a complete JSON value, so the value-sequence path reads it as one
// record instead of sending it to the line parser.
func TestJSONRecords_ReadsSingleValueLayouts(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
		want []any
	}{
		{
			name: "empty object",
			body: `{}`,
			want: []any{map[string]any{}},
		},
		{
			name: "object with no records key",
			body: `{"host":"a","msg":"first"}`,
			want: []any{map[string]any{"host": "a", "msg": "first"}},
		},
		{
			name: "records holding a string",
			body: `{"Records":"not an array"}`,
			want: []any{map[string]any{"Records": "not an array"}},
		},
		{
			name: "records holding an object",
			body: `{"Records":{"host":"a"}}`,
			want: []any{map[string]any{"Records": map[string]any{"host": "a"}}},
		},
		{
			name: "records holding a number",
			body: `{"Records":7}`,
			want: []any{map[string]any{"Records": float64(7)}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bodies, err := driveJSON(t, tc.body)
			require.NoError(t, err)
			require.Equal(t, tc.want, bodies)
		})
	}
}

// TestJSONRecords_GivesUpPastTheSearchBudget asserts the search stops once it reaches a
// key beyond the budget. Searching the whole object would read an unbounded number of
// bytes before the first record.
func TestJSONRecords_GivesUpPastTheSearchBudget(t *testing.T) {
	t.Parallel()

	padding := strings.Repeat("x", maxRecordsSearchBytes+64)
	body := `{"padding":"` + padding + `","owner":"team","Records":[{"host":"a"}]}`

	_, err := driveJSON(t, body)
	require.ErrorIs(t, err, ErrNotArrayOrKnownObject)
}

// TestJSONRecords_ReportsTruncatedObjects asserts an object cut off before its
// "Records" key surfaces the read failure rather than reporting no records.
func TestJSONRecords_ReportsTruncatedObjects(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "cut inside a key", body: `{"own`},
		{name: "cut before a value", body: `{"owner":`},
		{name: "cut inside a skipped value", body: `{"meta":{"a":`},
		{name: "cut after the records key", body: `{"Records":`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := driveJSON(t, tc.body)
			require.Error(t, err)
		})
	}
}

// TestJSONRecords_StopsWhenTheConsumerBreaks asserts the iterator releases when the
// caller stops early, which is how a batch limit ends a read.
func TestJSONRecords_StopsWhenTheConsumerBreaks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stream := detectionStream(`[{"n":1},{"n":2},{"n":3}]`, zap.NewNop(), true)

	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)

	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var seen int
	for range seq {
		seen++
		break
	}
	require.Equal(t, 1, seen)
	require.Positive(t, producer.Position().Offset, "the position must mark the consumed record")
}

// peekOnlyReader answers probes from a fixed header and then fails every read. It puts
// a break between detection succeeding and the decoder starting.
type peekOnlyReader struct {
	BufferedReader
	header  []byte
	readErr error
}

func (r peekOnlyReader) Peek(n int) ([]byte, error) {
	if n > len(r.header) {
		return r.header, nil
	}
	return r.header[:n], nil
}

func (r peekOnlyReader) Read([]byte) (int, error) { return 0, r.readErr }

// TestJSONRecords_ReportsAFailedFirstRead asserts a stream that breaks between detection
// and the first decode surfaces the read error. The object is retried, so the failure
// must not be reported as an empty array.
func TestJSONRecords_ReportsAFailedFirstRead(t *testing.T) {
	t.Parallel()

	readErr := errors.New("connection reset by peer")
	parser := NewJSONParser(peekOnlyReader{
		BufferedReader: NewBufferedReader(strings.NewReader(""), 4096),
		header:         []byte(`[{"host":"a"}]`),
		readErr:        readErr,
	}, nil, BodyOptions{})

	_, err := parser.Parse(context.Background(), 0)
	require.ErrorIs(t, err, readErr)
	require.ErrorContains(t, err, "read first token")
}

// TestJSONRecords_RejectsNonContainerDocuments asserts a top-level value that is neither
// an array nor an object is refused. Detection normally screens these out, so this pins
// the parser's own contract for a caller that builds it directly.
func TestJSONRecords_RejectsNonContainerDocuments(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`"just a string"`, `42`, `true`, `null`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()

			parser := NewJSONParser(NewBufferedReader(strings.NewReader(body), 4096), nil, BodyOptions{})
			_, err := parser.Parse(context.Background(), 0)
			require.ErrorIs(t, err, ErrNotArrayOrKnownObject)
		})
	}
}
