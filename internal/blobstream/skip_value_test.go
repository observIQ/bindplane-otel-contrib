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
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// decoderAfterFirstKey returns a decoder positioned on the value of the first key in
// body, which is where skipValue is called from.
func decoderAfterFirstKey(t *testing.T, body string) *json.Decoder {
	t.Helper()

	decoder := json.NewDecoder(strings.NewReader(body))

	open, err := decoder.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('{'), open)

	_, err = decoder.Token()
	require.NoError(t, err)

	return decoder
}

// TestSkipValue_LeavesTheDecoderOnTheNextKey covers each value shape the search walks
// past while looking for a "Records" key. Stopping one token short or long would make
// the next key unreadable and lose the array.
func TestSkipValue_LeavesTheDecoderOnTheNextKey(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "string", body: `{"skipped":"a string","next":1}`},
		{name: "number", body: `{"skipped":42,"next":1}`},
		{name: "boolean", body: `{"skipped":true,"next":1}`},
		{name: "null", body: `{"skipped":null,"next":1}`},
		{name: "empty object", body: `{"skipped":{},"next":1}`},
		{name: "empty array", body: `{"skipped":[],"next":1}`},
		{name: "flat object", body: `{"skipped":{"a":1,"b":2},"next":1}`},
		{name: "flat array", body: `{"skipped":[1,2,3],"next":1}`},
		{name: "nested object", body: `{"skipped":{"a":{"b":{"c":[1,{"d":2}]}}},"next":1}`},
		{name: "array of objects", body: `{"skipped":[{"a":1},{"b":[2,3]}],"next":1}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decoder := decoderAfterFirstKey(t, tc.body)
			require.NoError(t, skipValue(decoder, maxRecordsSearchBytes))

			tok, err := decoder.Token()
			require.NoError(t, err)
			require.Equal(t, "next", tok, "the decoder must land on the following key")
		})
	}
}

// TestSkipValue_StopsPastTheSearchBudget asserts the walk gives up once it runs past
// the budget. Without the cap, a large object ahead of a "Records" key would be walked
// in full before the search could fail.
func TestSkipValue_StopsPastTheSearchBudget(t *testing.T) {
	t.Parallel()

	decoder := decoderAfterFirstKey(t, `{"skipped":"a string","next":1}`)

	// A budget already behind the decoder's position ends the walk immediately.
	require.ErrorIs(t, skipValue(decoder, 0), ErrNotArrayOrKnownObject)
}

// TestSkipValue_ReportsTruncatedValues asserts a value cut off mid-structure returns
// the decoder's error rather than reporting a clean skip.
func TestSkipValue_ReportsTruncatedValues(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "truncated object", body: `{"skipped":{"a":1`},
		{name: "truncated array", body: `{"skipped":[1,2`},
		{name: "truncated nested value", body: `{"skipped":{"a":{"b":`},
		{name: "value missing entirely", body: `{"skipped":`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			decoder := decoderAfterFirstKey(t, tc.body)
			require.Error(t, skipValue(decoder, maxRecordsSearchBytes))
		})
	}
}
