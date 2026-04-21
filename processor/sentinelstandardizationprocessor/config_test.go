// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sentinelstandardizationprocessor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "empty config is valid",
			cfg:  Config{},
		},
		{
			name: "valid rule with all fields",
			cfg: Config{
				SentinelField: []SentinelFieldRule{
					{
						Condition:  `attributes["event.type"] == "auth"`,
						StreamName: "Custom-AuthEvents_CL",
						RuleID:     "dcr-00000000000000000000000000000001",
						IngestionLabels: map[string]string{
							"env":  "prod",
							"team": "security",
						},
					},
				},
			},
		},
		{
			name: "valid rule with empty condition defaults to true",
			cfg: Config{
				SentinelField: []SentinelFieldRule{
					{StreamName: "Custom-Default_CL"},
				},
			},
		},
		{
			name: "missing stream_name is rejected",
			cfg: Config{
				SentinelField: []SentinelFieldRule{
					{Condition: "true"},
				},
			},
			wantErr: "sentinel_field[0]: stream_name is required",
		},
		{
			name: "invalid OTTL condition is rejected",
			cfg: Config{
				SentinelField: []SentinelFieldRule{
					{Condition: "this is not ottl", StreamName: "Custom-Test_CL"},
				},
			},
			wantErr: "sentinel_field[0]: invalid condition",
		},
		{
			name: "invalid rule reports its index",
			cfg: Config{
				SentinelField: []SentinelFieldRule{
					{Condition: "true", StreamName: "Custom-First_CL"},
					{Condition: "&&", StreamName: "Custom-Second_CL"},
				},
			},
			wantErr: "sentinel_field[1]: invalid condition",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
