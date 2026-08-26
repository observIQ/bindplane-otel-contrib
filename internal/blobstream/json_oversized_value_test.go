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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJSONSequence_OversizedValueIsSkipped asserts that a single top-level value larger
// than max_log_size is skipped as a per-record parse error and the records after it are
// still delivered, rather than buffered whole in memory. Without a bound a multi-gigabyte
// NDJSON value would be read entirely into memory and OOM the collector; the reader here
// is sized to 4096, so a 6 KB value exceeds the bound.
func TestJSONSequence_OversizedValueIsSkipped(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 6000)
	body := `{"n":1}` + "\n" + `{"big":"` + big + `"}` + "\n" + `{"n":2}` + "\n"

	got, errCount := collectRecordsAndErrors(t, body)
	require.Equal(t, []string{`{"n":1}`, `{"n":2}`}, got,
		"records before and after the oversized value are delivered")
	require.Equal(t, 1, errCount, "the oversized value is skipped and reported once")
}
