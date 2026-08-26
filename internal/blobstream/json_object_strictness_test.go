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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func collectRecordsAndErrors(t *testing.T, body string) ([]string, int) {
	t.Helper()
	reader := NewBufferedReader(strings.NewReader(body), 4096)
	parser := NewJSONParser(reader, nil, BodyOptions{})
	records, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var got []string
	var errCount int
	for record, rerr := range records {
		if rerr != nil {
			errCount++
			continue
		}
		switch r := record.(type) {
		case json.RawMessage:
			got = append(got, string(r))
		case rawTextLine:
			got = append(got, string(r))
		default:
			t.Fatalf("unexpected record type %T", record)
		}
	}
	return got, errCount
}

// TestJSONArray_SkipsNonObjectElements asserts a non-object array element is skipped as a
// parse error rather than emitted as a scalar body; the object elements still deliver.
func TestJSONArray_SkipsNonObjectElements(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, `[{"host":"a"}, 5, "x", {"host":"b"}]`)
	require.Equal(t, []string{`{"host":"a"}`, `{"host":"b"}`}, got, "only object elements are delivered")
	require.Equal(t, 2, errCount, "each non-object element is reported as a skipped parse error")
}

// TestJSONSequence_EmitsScalarLineAsText asserts a top-level value sequence (NDJSON) is more
// lenient than an array: a bare scalar line is emitted as its own text rather than skipped,
// so no data is lost. The object lines still deliver as records.
func TestJSONSequence_EmitsScalarLineAsText(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, "{\"n\":1}\n5\n{\"n\":2}\n")
	require.Equal(t, []string{`{"n":1}`, `5`, `{"n":2}`}, got, "the scalar line is emitted as text between the objects")
	require.Equal(t, 0, errCount, "nothing is dropped")
}

// TestJSONArray_StopsWhenConsumerBreaksOnSkippedElement asserts the iterator releases if
// the caller stops while a non-object element is being skipped (a batch limit reached on
// the skip error itself).
func TestJSONArray_StopsWhenConsumerBreaksOnSkippedElement(t *testing.T) {
	t.Parallel()

	reader := NewBufferedReader(strings.NewReader(`[5, {"a":1}]`), 4096)
	parser := NewJSONParser(reader, nil, BodyOptions{})
	records, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var seen int
	for _, rerr := range records {
		seen++
		require.Error(t, rerr, "the first element is a non-object, reported as a skip error")
		break
	}
	require.Equal(t, 1, seen)
}
