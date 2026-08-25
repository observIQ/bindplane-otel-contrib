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

func driveJSONRecordsAndError(t *testing.T, body string, bufSize int) ([]string, error) {
	t.Helper()
	reader := NewBufferedReader(strings.NewReader(body), bufSize)
	parser := NewJSONParser(reader, nil, BodyOptions{})
	seq, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var records []string
	var lastErr error
	for rec, rerr := range seq {
		if rerr != nil {
			lastErr = rerr
			continue
		}
		records = append(records, string(rec.(json.RawMessage)))
	}
	return records, lastErr
}

// TestJSONRecordsWrapper_TrailingNonWrapperIsNotSentinel asserts that when a records
// wrapper is followed by a concatenated document that is not itself a records wrapper,
// the one parsed record is delivered and the trailing document is reported as a plain
// per-record parse error. The error must NOT unwrap to ErrNotArrayOrKnownObject: the
// worker uses that sentinel to re-read the whole object with the line parser, which would
// re-emit the already-delivered record as a raw line body and garble the checkpoint.
func TestJSONRecordsWrapper_TrailingNonWrapperIsNotSentinel(t *testing.T) {
	t.Parallel()

	records, lastErr := driveJSONRecordsAndError(t, `{"Records":[{"n":"one"}]}{"not":"a wrapper"}`, 4096)

	require.Equal(t, []string{`{"n":"one"}`}, records, "the one parsed record is delivered")
	require.Error(t, lastErr, "the trailing non-wrapper document is reported")
	require.NotErrorIs(t, lastErr, ErrNotArrayOrKnownObject,
		"must not unwrap to the sentinel that triggers a whole-object line re-read")
	require.False(t, IsUnsupportedContent(lastErr),
		"a trailing non-wrapper is a counted per-record parse error, not a DLQ/fallback condition")
}

// TestJSONRecordsWrapper_OversizedTailIsCountedNotSilent asserts that when a records
// wrapper's trailing keys exceed the search limit (so finishObject cannot reach the next
// document), the failure is counted as one parse error rather than silently dropping the
// documents concatenated after it. The error must not be the fallback sentinel.
func TestJSONRecordsWrapper_OversizedTailIsCountedNotSilent(t *testing.T) {
	t.Parallel()

	// A trailing array long enough that finishObject's per-value offset bound
	// (maxRecordsSearchBytes past the wrapper's Records array) is exceeded while skipping
	// it, so finishObject cannot reach the concatenated document that follows.
	tail := strings.Repeat("0,", 2600) + "0" // ~5 KB, beyond maxRecordsSearchBytes (4096)
	body := `{"Records":[{"n":"one"}],"tail":[` + tail + `]}` + `{"Records":[{"n":"two"}]}`

	records, lastErr := driveJSONRecordsAndError(t, body, 16384)

	require.Equal(t, []string{`{"n":"one"}`}, records, "the parsed record before the oversized tail is delivered")
	require.Error(t, lastErr, "the oversized tail is counted, not silently dropped")
	require.NotErrorIs(t, lastErr, ErrNotArrayOrKnownObject,
		"must not unwrap to the sentinel that triggers a whole-object line re-read")
	require.False(t, IsUnsupportedContent(lastErr),
		"an unreadable wrapper tail is a counted per-record parse error, not a DLQ/fallback condition")
}
