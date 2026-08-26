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

// TestJSONSequence_ResumeDoesNotReReportBelowOffsetError asserts a malformed line below the
// resume checkpoint is not reported again on resume. It was already reported in the first
// pass; re-yielding it double-counts the parse-error metric. The resync past it still
// happens so the records after it are reached.
func TestJSONSequence_ResumeDoesNotReReportBelowOffsetError(t *testing.T) {
	t.Parallel()

	body := "{\"n\":1}\n{bad}\n{\"n\":2}\n{\"n\":3}\n"

	// Pass 1: read everything, capturing the offset right after {"n":2}. The malformed
	// line is reported once here.
	reader := NewBufferedReader(strings.NewReader(body), 4096)
	parser := NewJSONParser(reader, nil, BodyOptions{})
	records, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var firstPassErrs int
	var resumeOffset int64
	for record, rerr := range records {
		if rerr != nil {
			firstPassErrs++
			continue
		}
		if string(record.(json.RawMessage)) == `{"n":2}` {
			resumeOffset = parser.Offset()
		}
	}
	require.Equal(t, 1, firstPassErrs, "the malformed line is reported once in the first pass")
	require.Positive(t, resumeOffset)

	// Pass 2: resume past {"n":2}. The malformed line sits below the checkpoint, so it must
	// not be reported again; only {"n":3} remains.
	reader2 := NewBufferedReader(strings.NewReader(body), 4096)
	parser2 := NewJSONParser(reader2, nil, BodyOptions{})
	records2, err := parser2.Parse(context.Background(), resumeOffset)
	require.NoError(t, err)

	var got []string
	var resumeErrs int
	for record, rerr := range records2 {
		if rerr != nil {
			resumeErrs++
			continue
		}
		got = append(got, string(record.(json.RawMessage)))
	}

	require.Equal(t, []string{`{"n":3}`}, got, "resume delivers only the records after the checkpoint")
	require.Zero(t, resumeErrs, "a malformed line below the checkpoint is not reported again on resume")
}
