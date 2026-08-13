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
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyJSON_RejectsEmptyAndBlankStreams(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "spaces", body: "   "},
		{name: "newlines and tabs", body: "\n\t\r\n"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reader := NewBufferedReader(strings.NewReader(tc.body), 4096)
			_, err := classifyJSON(reader)
			require.ErrorIs(t, err, ErrNotArrayOrKnownObject)
		})
	}
}

func TestClassifyJSON_PropagatesPeekErrors(t *testing.T) {
	t.Parallel()

	readErr := errors.New("connection reset by peer")
	// The shared stub lives in parser_detection_test.go.
	reader := &failingPeekReader{
		BufferedReader: NewBufferedReader(strings.NewReader(`[{"host":"a"}]`), 4096),
		peekErr:        readErr,
	}

	_, err := classifyJSON(reader)
	require.ErrorIs(t, err, readErr)
}

// TestJSONSequence_TruncatedFinalValueStops asserts a stream cut mid-record keeps the
// complete records and reports the cut. The fragment is never emitted, and ending
// quietly would ack the object and lose whatever followed.
func TestJSONSequence_TruncatedFinalValueStops(t *testing.T) {
	t.Parallel()

	reader := NewBufferedReader(strings.NewReader("{\"host\":\"a\"}\n{\"host\":\"b"), 4096)
	parser := NewJSONParser(reader, BodyOptions{})

	records, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var got []string
	var last error
	for record, err := range records {
		if err != nil {
			last = err
			continue
		}
		got = append(got, string(record.(json.RawMessage)))
	}
	require.Equal(t, []string{`{"host":"a"}`}, got, "the complete record is still delivered")
	require.True(t, IsTruncatedObject(last), "the cut record must be reported, not dropped quietly")
}

// TestJSONSequence_StopsWhenConsumerBreaks asserts the iterator releases when the
// caller stops early, which is how a batch limit ends a read.
func TestJSONSequence_StopsWhenConsumerBreaks(t *testing.T) {
	t.Parallel()

	reader := NewBufferedReader(strings.NewReader("{\"n\":1}\n{\"n\":2}\n{\"n\":3}\n"), 4096)
	parser := NewJSONParser(reader, BodyOptions{})

	records, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var seen int
	for range records {
		seen++
		break
	}
	require.Equal(t, 1, seen)
	require.Positive(t, parser.Offset(), "the offset must mark the consumed record")
}

// TestJSONSequence_SkipsRecordsBeforeStartOffset asserts resume drops the records a
// previous run already sent.
func TestJSONSequence_SkipsRecordsBeforeStartOffset(t *testing.T) {
	t.Parallel()

	body := "{\"n\":1}\n{\"n\":2}\n{\"n\":3}\n"
	startOffset := int64(strings.Index(body, "{\"n\":3}"))

	reader := NewBufferedReader(strings.NewReader(body), 4096)
	parser := NewJSONParser(reader, BodyOptions{})

	records, err := parser.Parse(context.Background(), startOffset)
	require.NoError(t, err)

	var got []string
	for record, err := range records {
		require.NoError(t, err)
		got = append(got, string(record.(json.RawMessage)))
	}
	require.Equal(t, []string{`{"n":3}`}, got)
}
