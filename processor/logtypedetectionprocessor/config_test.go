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
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/confmap"
)

func TestCreateDefaultProcessorConfig(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	require.Equal(t, defaultFingerprintField, cfg.FingerprintField)
	require.Equal(t, defaultLogTypeField, cfg.LogTypeField)
	require.Len(t, cfg.Matchers, 0)
	require.Equal(t, defaultMaxSavedFingerprints, cfg.MaxSavedFingerprints)
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
				LogTypeField:         "log_type_field",
				MaxSavedFingerprints: defaultMaxSavedFingerprints,
			},
		},
		{
			name: "missing log type field",
			config: &Config{
				FingerprintField:     "fingerprint_field",
				MaxSavedFingerprints: defaultMaxSavedFingerprints,
			},
			err: errMissingLogTypeField,
		},
		{
			name: "opamp without extension",
			config: &Config{
				LogTypeField:         "log_type_field",
				MaxSavedFingerprints: defaultMaxSavedFingerprints,
				OpAMP:                &OpAMPConfig{},
			},
			err: errMissingOpAMPExtension,
		},
		{
			name: "negative opamp request timeout",
			config: &Config{
				LogTypeField:         "log_type_field",
				MaxSavedFingerprints: defaultMaxSavedFingerprints,
				OpAMP:                &OpAMPConfig{Extension: opampID, RequestTimeout: -time.Second},
			},
			err: errInvalidOpAMPTimeout,
		},
		{
			name: "valid opamp max version",
			config: &Config{
				LogTypeField:         "log_type_field",
				MaxSavedFingerprints: defaultMaxSavedFingerprints,
				OpAMP:                &OpAMPConfig{Extension: opampID, MatchersVersion: "1.5.0"},
			},
		},
		{
			name: "invalid opamp max version",
			config: &Config{
				LogTypeField:         "log_type_field",
				MaxSavedFingerprints: defaultMaxSavedFingerprints,
				OpAMP:                &OpAMPConfig{Extension: opampID, MatchersVersion: "latest"},
			},
			err: errInvalidOpAMPMaxVersion,
		},
		{
			name: "non-positive max saved fingerprints",
			config: &Config{
				LogTypeField: "log_type_field",
			},
			err: errInvalidMaxFingerprints,
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

func TestUnmarshalOpAMPDefaults(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	conf := confmap.NewFromStringMap(map[string]any{"opamp": map[string]any{"extension": "opamp"}})
	require.NoError(t, conf.Unmarshal(cfg))
	require.Equal(t, opampID, cfg.OpAMP.Extension)
	require.Equal(t, defaultOpAMPRequestTimeout, cfg.OpAMP.RequestTimeout)

	cfg = createDefaultConfig().(*Config)
	conf = confmap.NewFromStringMap(map[string]any{"opamp": map[string]any{"extension": "opamp", "request_timeout": "0s"}})
	require.NoError(t, conf.Unmarshal(cfg))
	require.Zero(t, cfg.OpAMP.RequestTimeout, "an explicit zero keeps asking indefinitely")

	cfg = createDefaultConfig().(*Config)
	require.NoError(t, confmap.NewFromStringMap(map[string]any{}).Unmarshal(cfg))
	require.Nil(t, cfg.OpAMP)
}
