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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJSONSequence_SecondResyncKeepsRecords asserts that a value sequence with TWO
// malformed lines close together (both inside one read-ahead window) still delivers
// every good record after the second one. The first resync rebuilds the decoder on a
// bufio.Reader that has read ahead from the source; the second resync must continue from
// that same reader, not from the now-drained original source, or the buffered-but-unread
// records between the two are silently dropped.
func TestJSONSequence_SecondResyncKeepsRecords(t *testing.T) {
	t.Parallel()

	const total = 400
	// Two malformed lines close together (indices 50 and 60) with many good records
	// after them, so a second-resync loss shows up as missing tail records.
	bad := map[int]bool{50: true, 60: true}

	var sb strings.Builder
	var want []string
	for i := 0; i < total; i++ {
		if bad[i] {
			sb.WriteString("{bad line}\n")
			continue
		}
		line := fmt.Sprintf(`{"n":%d}`, i)
		sb.WriteString(line)
		sb.WriteByte('\n')
		want = append(want, line)
	}

	got, errCount := collectRecordsAndErrors(t, sb.String())
	require.Equal(t, want, got, "every good record survives across two resyncs")
	require.Equal(t, len(bad), errCount, "each malformed line is reported exactly once")
}
