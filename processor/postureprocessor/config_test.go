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

package postureprocessor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"

	"github.com/observiq/bindplane-otel-contrib/internal/posture"
)

func baseCfg() *Config {
	sid := component.MustNewID("file_storage")
	pid := component.MustNewID("posture")
	return &Config{
		Levels:           posture.DefaultLevels,
		StorageID:        &sid,
		PostureExtension: &pid,
		Tiers:            []TierConfig{{Name: "battle", Condition: "true", MinLevel: "silent"}},
	}
}

func TestValidate(t *testing.T) {
	require.NoError(t, baseCfg().Validate())

	t.Run("storage required", func(t *testing.T) {
		c := baseCfg()
		c.StorageID = nil
		assert.ErrorContains(t, c.Validate(), "storage is required")
	})

	t.Run("posture source required", func(t *testing.T) {
		c := baseCfg()
		c.PostureExtension = nil
		assert.ErrorContains(t, c.Validate(), "posture_extension or posture")
	})

	t.Run("posture sources mutually exclusive", func(t *testing.T) {
		c := baseCfg()
		c.Posture = &posture.Config{}
		assert.ErrorContains(t, c.Validate(), "only one of")
	})

	t.Run("at least one tier", func(t *testing.T) {
		c := baseCfg()
		c.Tiers = nil
		assert.ErrorContains(t, c.Validate(), "at least one tier")
	})

	t.Run("duplicate tier name", func(t *testing.T) {
		c := baseCfg()
		c.Tiers = append(c.Tiers, TierConfig{Name: "battle", Condition: "true", MinLevel: "full"})
		assert.ErrorContains(t, c.Validate(), "duplicate tier")
	})

	t.Run("reserved tier name", func(t *testing.T) {
		c := baseCfg()
		c.Tiers[0].Name = defaultTierName
		assert.ErrorContains(t, c.Validate(), "reserved")
	})

	t.Run("unknown min_level", func(t *testing.T) {
		c := baseCfg()
		c.Tiers[0].MinLevel = "bogus"
		assert.Error(t, c.Validate())
	})

	t.Run("invalid overflow policy", func(t *testing.T) {
		c := baseCfg()
		c.Buffer.OverflowPolicy = "nope"
		assert.ErrorContains(t, c.Validate(), "overflow_policy")
	})

	t.Run("jitter range", func(t *testing.T) {
		c := baseCfg()
		c.Drain.JitterFraction = 1.5
		assert.ErrorContains(t, c.Validate(), "jitter_fraction")
	})
}

func TestFactoryDefaultConfig(t *testing.T) {
	cfg := NewFactory().CreateDefaultConfig().(*Config)
	assert.Equal(t, posture.DefaultLevels, cfg.Levels)
	assert.Equal(t, overflowDropOldest, cfg.Buffer.OverflowPolicy)
	assert.Positive(t, cfg.Drain.Interval)
}
