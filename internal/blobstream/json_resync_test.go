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

// TestJSONSequence_ResyncsAfterMalformedLine asserts a malformed NDJSON line is skipped
// and the lines after it are still delivered, rather than the malformed line stopping the
// whole object at the first syntax error.
func TestJSONSequence_ResyncsAfterMalformedLine(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, "{\"n\":1}\n{bad line}\n{\"n\":2}\n{\"n\":3}\n")
	require.Equal(t, []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, got,
		"lines before and after the malformed line are delivered")
	require.Equal(t, 1, errCount, "the malformed line is skipped and reported once")
}

// TestJSONSequence_ResyncsAcrossConsecutiveMalformedLines asserts each malformed line is
// skipped independently, so a run of bad lines does not lose the good ones after them.
func TestJSONSequence_ResyncsAcrossConsecutiveMalformedLines(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, "{\"n\":1}\n{bad1\nalso bad\n{\"n\":2}\n")
	require.Equal(t, []string{`{"n":1}`, `{"n":2}`}, got)
	require.Equal(t, 2, errCount, "each malformed line is reported")
}

// TestJSONSequence_ResumeAfterResyncSkipsDeliveredRecords asserts the resume offset stays
// absolute across a resync, so a re-run past the malformed line delivers only the records
// that follow, never re-delivering earlier ones.
func TestJSONSequence_ResumeAfterResyncSkipsDeliveredRecords(t *testing.T) {
	t.Parallel()

	body := "{\"n\":1}\n{bad}\n{\"n\":2}\n{\"n\":3}\n"

	// Pass 1: read everything, capturing the offset right after {"n":2}.
	reader := NewBufferedReader(strings.NewReader(body), 4096)
	parser := NewJSONParser(reader, nil, BodyOptions{})
	records, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var resumeOffset int64
	for record, rerr := range records {
		if rerr != nil {
			continue
		}
		if string(record.(json.RawMessage)) == `{"n":2}` {
			resumeOffset = parser.Offset()
		}
	}
	require.Positive(t, resumeOffset)

	// Pass 2: resume past {"n":2}; only {"n":3} should remain.
	reader2 := NewBufferedReader(strings.NewReader(body), 4096)
	parser2 := NewJSONParser(reader2, nil, BodyOptions{})
	records2, err := parser2.Parse(context.Background(), resumeOffset)
	require.NoError(t, err)

	var got []string
	for record, rerr := range records2 {
		if rerr != nil {
			continue
		}
		got = append(got, string(record.(json.RawMessage)))
	}
	require.Equal(t, []string{`{"n":3}`}, got, "resume delivers only the records after the checkpoint")
}

// TestJSONSequence_ResyncGivesUpWithoutTrailingNewline asserts that when the malformed
// tail has no terminating newline to realign to, the records before it are delivered and
// reading stops (best-effort skip-to-EOF), rather than spinning on the same bytes.
func TestJSONSequence_ResyncGivesUpWithoutTrailingNewline(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, "{\"n\":1}\n{bad with no terminating newline")
	require.Equal(t, []string{`{"n":1}`}, got, "the record before the unrecoverable tail is delivered")
	require.Equal(t, 1, errCount, "the malformed tail is reported once, then reading stops")
}
