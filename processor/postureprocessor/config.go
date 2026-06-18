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

// Package postureprocessor provides a processor that gates telemetry egress by
// a connectivity/EMCON posture level. High-priority tiers egress immediately;
// lower-priority tiers are persisted to disk and drained to the same
// destination when the posture rises to permit them.
package postureprocessor

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"

	"github.com/observiq/bindplane-otel-contrib/internal/posture"
)

const (
	overflowDropOldest = "drop_oldest"
	overflowDropNewest = "drop_newest"
	overflowBlockDrop  = "block_drop"

	defaultDrainInterval  = time.Second
	defaultJitterFraction = 0.2
)

// Config is the configuration for the posture processor.
type Config struct {
	// Levels is the ordered list of posture level names, lowest (most
	// restrictive) first. Must match the levels of the referenced posture
	// extension. Defaults to posture.DefaultLevels.
	Levels []string `mapstructure:"levels"`

	// Tiers classify telemetry by priority. The first tier whose condition
	// matches a record owns it; unmatched records fall to the default tier.
	Tiers []TierConfig `mapstructure:"tiers"`

	// DefaultMinLevel is the minimum posture level at which unmatched (default
	// tier) telemetry egresses. Defaults to the highest level.
	DefaultMinLevel string `mapstructure:"default_min_level"`

	// PostureExtension references a posture extension that supplies the level.
	// Mutually exclusive with Posture.
	PostureExtension *component.ID `mapstructure:"posture_extension,omitempty"`

	// Posture configures an inline posture provider. Intended for single-signal
	// pipelines; multi-signal deployments should use a shared posture extension.
	// Mutually exclusive with PostureExtension.
	Posture *posture.Config `mapstructure:"posture,omitempty"`

	// StorageID references the storage extension used to persist buffered
	// telemetry. Required.
	StorageID *component.ID `mapstructure:"storage"`

	// Drain controls how buffered telemetry is released when the posture rises.
	Drain DrainConfig `mapstructure:"drain"`

	// Buffer controls the per-tier on-disk buffer limits and overflow behavior.
	Buffer BufferConfig `mapstructure:"buffer"`
}

// TierConfig defines a priority tier.
type TierConfig struct {
	// Name identifies the tier; must be unique. Used in storage keys and metrics.
	Name string `mapstructure:"name"`
	// Condition is an OTTL condition. The first tier whose condition matches a
	// record owns it.
	Condition string `mapstructure:"condition"`
	// MinLevel is the minimum posture level at which this tier egresses.
	MinLevel string `mapstructure:"min_level"`
}

// DrainConfig controls the rate at which buffered telemetry is released.
type DrainConfig struct {
	// Interval is the delay between draining successive buffered batches.
	Interval time.Duration `mapstructure:"interval"`
	// MaxBytesPerSec throttles drain throughput. 0 means unlimited.
	MaxBytesPerSec int64 `mapstructure:"max_bytes_per_sec"`
	// JitterFraction randomizes the interval by up to this fraction (0..1) to
	// avoid a drain stampede when many collectors recover at once.
	JitterFraction float64 `mapstructure:"jitter_fraction"`
}

// BufferConfig controls the per-tier on-disk buffer.
type BufferConfig struct {
	// MaxBytes is the per-tier byte quota. 0 means unlimited (discouraged).
	MaxBytes int64 `mapstructure:"max_bytes"`
	// MaxItems is the per-tier batch count quota. 0 means unlimited.
	MaxItems int64 `mapstructure:"max_items"`
	// OverflowPolicy is applied when a tier's quota is exceeded:
	// drop_oldest, drop_newest, or block_drop. Defaults to drop_oldest.
	OverflowPolicy string `mapstructure:"overflow_policy"`
}

// Validate validates the processor configuration.
func (c Config) Validate() error {
	ls, err := posture.NewLevelSet(c.levelsOrDefault())
	if err != nil {
		return err
	}

	if c.StorageID == nil {
		return errors.New("storage is required")
	}

	switch {
	case c.PostureExtension == nil && c.Posture == nil:
		return errors.New("one of posture_extension or posture is required")
	case c.PostureExtension != nil && c.Posture != nil:
		return errors.New("only one of posture_extension or posture may be set")
	case c.Posture != nil:
		if err := c.Posture.Validate(); err != nil {
			return fmt.Errorf("posture: %w", err)
		}
	}

	if len(c.Tiers) == 0 {
		return errors.New("at least one tier is required")
	}
	seen := make(map[string]struct{}, len(c.Tiers))
	for i, t := range c.Tiers {
		if t.Name == "" {
			return fmt.Errorf("tiers[%d]: name is required", i)
		}
		if _, ok := seen[t.Name]; ok {
			return fmt.Errorf("duplicate tier name %q", t.Name)
		}
		seen[t.Name] = struct{}{}
		if t.Condition == "" {
			return fmt.Errorf("tier %q: condition is required", t.Name)
		}
		if _, err := ls.Parse(t.MinLevel); err != nil {
			return fmt.Errorf("tier %q min_level: %w", t.Name, err)
		}
	}
	if _, ok := seen[defaultTierName]; ok {
		return fmt.Errorf("tier name %q is reserved for unmatched telemetry", defaultTierName)
	}
	if c.DefaultMinLevel != "" {
		if _, err := ls.Parse(c.DefaultMinLevel); err != nil {
			return fmt.Errorf("default_min_level: %w", err)
		}
	}

	switch c.Buffer.OverflowPolicy {
	case "", overflowDropOldest, overflowDropNewest, overflowBlockDrop:
	default:
		return fmt.Errorf("invalid overflow_policy %q", c.Buffer.OverflowPolicy)
	}

	if c.Drain.JitterFraction < 0 || c.Drain.JitterFraction > 1 {
		return errors.New("drain.jitter_fraction must be between 0 and 1")
	}

	return nil
}

func (c Config) levelsOrDefault() []string {
	if len(c.Levels) == 0 {
		return posture.DefaultLevels
	}
	return c.Levels
}
