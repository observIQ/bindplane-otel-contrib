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

package bundle

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRequestPayload(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		wantID    string
		wantErr   bool
	}{
		{
			name:   "valid yaml",
			data:   []byte("session_id: abc-123\n"),
			wantID: "abc-123",
		},
		{
			name:   "whitespace trimmed",
			data:   []byte("session_id: \"  spaced  \"\n"),
			wantID: "spaced",
		},
		{
			name:   "empty data returns zero value",
			data:   nil,
			wantID: "",
		},
		{
			name:    "invalid yaml",
			data:    []byte(":\t:bad"),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRequestPayload(tc.data)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantID, got.SessionID)
		})
	}
}
