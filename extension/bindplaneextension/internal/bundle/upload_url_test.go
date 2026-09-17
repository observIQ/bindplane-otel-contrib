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

func TestUploadURL(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		agentID  string
		want     string
	}{
		{
			name:     "wss endpoint",
			endpoint: "wss://example.com/opamp",
			agentID:  "abc-123",
			want:     "https://example.com/v1/agents/abc-123/support-bundle",
		},
		{
			name:     "ws endpoint",
			endpoint: "ws://localhost:4317/opamp",
			agentID:  "agent-1",
			want:     "http://localhost:4317/v1/agents/agent-1/support-bundle",
		},
		{
			name:     "agent ID is path-escaped",
			endpoint: "wss://example.com/opamp",
			agentID:  "agent/with/slashes",
			want:     "https://example.com/v1/agents/agent%2Fwith%2Fslashes/support-bundle",
		},
		{
			name:     "unsupported scheme returns empty",
			endpoint: "https://example.com/opamp",
			agentID:  "abc",
			want:     "",
		},
		{
			name:     "empty endpoint returns empty",
			endpoint: "",
			agentID:  "abc",
			want:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := UploadURL(tc.endpoint, tc.agentID)
			require.Equal(t, tc.want, got)
		})
	}
}
