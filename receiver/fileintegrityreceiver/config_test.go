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

package fileintegrityreceiver

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	dir := t.TempDir()
	t.Run("empty_paths", func(t *testing.T) {
		cfg := &Config{}
		require.EqualError(t, cfg.Validate(), "paths must contain at least one path")
	})
	t.Run("path_missing", func(t *testing.T) {
		cfg := &Config{Paths: []string{filepath.Join(dir, "nope")}}
		err := cfg.Validate()
		require.Error(t, err)
	})
	t.Run("hashing_needs_debounce", func(t *testing.T) {
		cfg := &Config{
			Paths: []string{dir},
			Hashing: HashingConfig{
				Enabled:  true,
				Debounce: 0,
				MaxBytes: 1024,
			},
		}
		require.EqualError(t, cfg.Validate(), "hashing.debounce must be positive when hashing is enabled")
	})
	t.Run("hashing_needs_max_bytes", func(t *testing.T) {
		cfg := &Config{
			Paths: []string{dir},
			Hashing: HashingConfig{
				Enabled:  true,
				Debounce: time.Second,
				MaxBytes: 0,
			},
		}
		require.EqualError(t, cfg.Validate(), "hashing.max_bytes must be positive when hashing is enabled")
	})
	t.Run("ok", func(t *testing.T) {
		cfg := &Config{Paths: []string{dir}}
		require.NoError(t, cfg.Validate())
	})
}

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	require.Equal(t, 2*time.Second, cfg.Hashing.Debounce)
	require.Equal(t, int64(32*1024*1024), cfg.Hashing.MaxBytes)
	require.False(t, cfg.Hashing.Enabled)
	require.Equal(t, 65536, cfg.MaxWatches)
}

func TestConfigValidate_MaxWatchesNegative(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Paths: []string{dir}, MaxWatches: -1}
	require.EqualError(t, cfg.Validate(), "max_watches must be non-negative")
}
