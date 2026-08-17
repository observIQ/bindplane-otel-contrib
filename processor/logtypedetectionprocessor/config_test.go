// Copyright  observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logtypedetectionprocessor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateDefaultProcessorConfig(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	require.Equal(t, defaultFingerprintField, cfg.FingerprintField)
	require.Equal(t, defaultLogTypeField, cfg.LogTypeField)
	require.Len(t, cfg.Matchers, 0)
}

func TestConfig_Validate(t *testing.T) {
	testCases := []struct {
		name   string
		config *Config
		err    error
	}{
		{
			name:   "default",
			config: createDefaultConfig().(*Config),
		},
		{
			name: "missing fingerprint field is accepted",
			config: &Config{
				LogTypeField: "log_type_field",
			},
		},
		{
			name: "missing log type field",
			config: &Config{
				FingerprintField: "fingerprint_field",
			},
			err: errMissingLogTypeFieldError,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
