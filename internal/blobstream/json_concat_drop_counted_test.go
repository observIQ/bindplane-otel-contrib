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
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJSONConcatenated_ArrayFollowedByGarbageIsCounted asserts that trailing content after
// a concatenated array that is not another array is surfaced as a parse error, rather than
// silently dropped. The array's records are still delivered and the object still acks.
func TestJSONConcatenated_ArrayFollowedByGarbageIsCounted(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, `[{"n":1},{"n":2}]garbage`)
	require.Equal(t, []string{`{"n":1}`, `{"n":2}`}, got, "the array's records are delivered")
	require.Equal(t, 1, errCount, "the dropped trailing content is counted, not silently discarded")
}

// TestJSONConcatenated_WrapperFollowedByNonWrapperIsCounted asserts the same for a records
// wrapper followed by a document that is not a wrapper.
func TestJSONConcatenated_WrapperFollowedByNonWrapperIsCounted(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, `{"Records":[{"n":1}]}{"not":"a wrapper"}`)
	require.Equal(t, []string{`{"n":1}`}, got, "the first wrapper's records are delivered")
	require.Equal(t, 1, errCount, "the dropped trailing document is counted")
}
