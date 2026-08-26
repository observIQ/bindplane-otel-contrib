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

// TestJSONConcatenated_Arrays asserts every element of concatenated arrays is delivered,
// not just the first array's, so naively concatenated array files are read in full.
func TestJSONConcatenated_Arrays(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, `[{"n":1},{"n":2}][{"n":3}][{"n":4},{"n":5}]`)
	require.Equal(t, []string{`{"n":1}`, `{"n":2}`, `{"n":3}`, `{"n":4}`, `{"n":5}`}, got)
	require.Zero(t, errCount)
}

// TestJSONConcatenated_RecordsWrappers asserts every Records element of concatenated
// {"Records": [...]} documents is delivered, not just the first wrapper's.
func TestJSONConcatenated_RecordsWrappers(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, `{"Records":[{"n":1},{"n":2}]}{"Records":[{"n":3}]}`)
	require.Equal(t, []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, got)
	require.Zero(t, errCount)
}

// TestJSONConcatenated_WrapperWithKeysAfterRecords asserts a wrapper whose object has
// keys after its Records array is fully consumed, so the next concatenated wrapper is
// still read.
func TestJSONConcatenated_WrapperWithKeysAfterRecords(t *testing.T) {
	t.Parallel()

	got, errCount := collectRecordsAndErrors(t, `{"Records":[{"n":1}],"nextToken":"abc"}{"Records":[{"n":2}]}`)
	require.Equal(t, []string{`{"n":1}`, `{"n":2}`}, got)
	require.Zero(t, errCount)
}

// TestJSONConcatenated_WrapperFollowedByNonWrapperStops asserts that when a concatenated
// document is not another wrapper, the records read so far are kept and reading stops
// (best effort), rather than failing the whole object.
func TestJSONConcatenated_WrapperFollowedByNonWrapperStops(t *testing.T) {
	t.Parallel()

	got, _ := collectRecordsAndErrors(t, `{"Records":[{"n":1}]}{"not":"a wrapper"}`)
	require.Equal(t, []string{`{"n":1}`}, got, "the first wrapper's records are delivered; the trailing non-wrapper stops the run")
}

// TestJSONConcatenated_WrapperWithMalformedTailStops asserts a wrapper malformed after its
// Records array keeps the records read and stops (best effort).
func TestJSONConcatenated_WrapperWithMalformedTailStops(t *testing.T) {
	t.Parallel()

	got, _ := collectRecordsAndErrors(t, `{"Records":[{"n":1}],"trailing":`)
	require.Equal(t, []string{`{"n":1}`}, got)
}

// TestJSONConcatenated_ArrayFollowedByGarbageStops asserts an array followed by non-JSON
// garbage keeps the array's records and stops, rather than failing the whole object.
func TestJSONConcatenated_ArrayFollowedByGarbageStops(t *testing.T) {
	t.Parallel()

	got, _ := collectRecordsAndErrors(t, `[{"n":1},{"n":2}]garbage`)
	require.Equal(t, []string{`{"n":1}`, `{"n":2}`}, got)
}

// TestJSONConcatenated_WrapperWithGarbageKeyStops asserts a wrapper whose object has
// garbage where a key should be, after its Records array, keeps the records and stops.
func TestJSONConcatenated_WrapperWithGarbageKeyStops(t *testing.T) {
	t.Parallel()

	got, _ := collectRecordsAndErrors(t, `{"Records":[{"n":1}],garbage}`)
	require.Equal(t, []string{`{"n":1}`}, got)
}
