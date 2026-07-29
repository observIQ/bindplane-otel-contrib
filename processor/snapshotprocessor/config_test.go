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

package snapshotprocessor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
)

func TestConfigValidate(t *testing.T) {
	t.Run("Default config is valid", func(t *testing.T) {
		err := createDefaultConfig().(*Config).Validate()
		require.NoError(t, err)
	})

	t.Run("OpAMP ID must be specified", func(t *testing.T) {
		var emptyID component.ID

		cfg := createDefaultConfig().(*Config)
		cfg.OpAMP = emptyID

		require.ErrorContains(t, cfg.Validate(), "`opamp` must be specified")
	})

	t.Run("buffer_size must be positive", func(t *testing.T) {
		cfg := createDefaultConfig().(*Config)
		cfg.BufferSize = 0
		require.ErrorContains(t, cfg.Validate(), "`buffer_size` must be positive")

		cfg.BufferSize = -1
		require.ErrorContains(t, cfg.Validate(), "`buffer_size` must be positive")
	})

	t.Run("buffer_size is capped", func(t *testing.T) {
		cfg := createDefaultConfig().(*Config)
		cfg.BufferSize = maxBufferSize + 1
		require.ErrorContains(t, cfg.Validate(), "`buffer_size` cannot exceed")
	})

	t.Run("refresh_interval cannot be negative", func(t *testing.T) {
		cfg := createDefaultConfig().(*Config)
		cfg.RefreshInterval = -time.Second
		require.ErrorContains(t, cfg.Validate(), "`refresh_interval` cannot be negative")
	})

	t.Run("buffer_mode must be valid", func(t *testing.T) {
		cfg := createDefaultConfig().(*Config)
		cfg.BufferMode = "sometimes"
		require.ErrorContains(t, cfg.Validate(), `invalid buffer_mode "sometimes"`)

		cfg.BufferMode = "on_demand"
		require.NoError(t, cfg.Validate())

		cfg.BufferMode = ""
		require.NoError(t, cfg.Validate())
	})

	t.Run("signals must be valid", func(t *testing.T) {
		cfg := createDefaultConfig().(*Config)
		cfg.Signals = []string{"logs", "gauges"}
		require.ErrorContains(t, cfg.Validate(), `invalid signal type "gauges"`)

		cfg.Signals = []string{"logs", "metrics", "traces"}
		require.NoError(t, cfg.Validate())
	})
}

func TestConfigBuffersSignal(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	require.True(t, cfg.buffersSignal("logs"))
	require.True(t, cfg.buffersSignal("metrics"))
	require.True(t, cfg.buffersSignal("traces"))

	cfg.Signals = []string{"logs"}
	require.True(t, cfg.buffersSignal("logs"))
	require.False(t, cfg.buffersSignal("metrics"))
	require.False(t, cfg.buffersSignal("traces"))
}
