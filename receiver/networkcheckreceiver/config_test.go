// Copyright  observIQ, Inc.
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

package networkcheckreceiver

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/confmap/confmaptest"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{
			name: "valid_icmp",
			cfg: &Config{
				Targets: []TargetConfig{
					{Method: MethodICMP, ClientConfig: func() confighttp.ClientConfig {
						c := confighttp.NewDefaultClientConfig()
						c.Endpoint = "8.8.8.8"
						return c
					}()},
				},
			},
		},
		{
			name:    "no_targets",
			cfg:     &Config{},
			wantErr: true,
		},
		{
			name: "invalid_method",
			cfg: &Config{
				Targets: []TargetConfig{
					{Method: "tcp"},
				},
			},
			wantErr: true,
		},
		{
			name: "negative_batch_size",
			cfg: &Config{
				Targets:   []TargetConfig{{Method: MethodICMP}},
				BatchSize: -1,
			},
			wantErr: true,
		},
		{
			name: "invalid_traceroute_threshold",
			cfg: &Config{
				Targets: []TargetConfig{{Method: MethodICMP}},
				Traceroute: TracerouteConfig{
					Enabled:          true,
					FailureThreshold: 1.5,
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	cm, err := confmaptest.LoadConf(filepath.Join("testdata", "config.yaml"))
	require.NoError(t, err)

	f := NewFactory()
	cfg := f.CreateDefaultConfig()

	sub, err := cm.Sub("receivers::networkstat")
	require.NoError(t, err)
	require.NoError(t, sub.Unmarshal(cfg))

	c := cfg.(*Config)
	require.Len(t, c.Targets, 2)
	require.Equal(t, "8.8.8.8", c.Targets[0].Endpoint)
	require.Equal(t, MethodICMP, c.Targets[0].Method)
	require.Equal(t, MethodHTTP, c.Targets[1].Method)
	require.True(t, c.Traceroute.Enabled)
}
