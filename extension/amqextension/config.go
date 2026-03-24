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

package amqextension

import (
	"errors"
	"fmt"
	"time"

	filter "github.com/observiq/bindplane-otel-contrib/internal/amqfilter"
)

// Config is the configuration for the AMQ (Approximate Membership Query) filter extension.
type Config struct {
	// Filters is the list of named filters to create.
	Filters []FilterConfig `mapstructure:"filters"`

	// ResetInterval is the interval at which to reset all filters (0 = never reset).
	ResetInterval time.Duration `mapstructure:"reset_interval,omitempty"`

	// Telemetry configures telemetry for the AMQ extension.
	Telemetry *TelemetryConfig `mapstructure:"telemetry,omitempty"`

	_ struct{} // prevent unkeyed literal initialization
}

// FilterConfig configures a single named AMQ filter.
type FilterConfig struct {
	// Name is the unique identifier for this filter.
	Name string `mapstructure:"name"`

	// Kind specifies the filter algorithm: "bloom", "cuckoo", "scalable_cuckoo", or "vacuum".
	// Defaults to "bloom" if not specified.
	Kind string `mapstructure:"kind,omitempty"`

	// Bloom filter options (kind: bloom)
	// EstimatedCount is the expected number of elements to store in the filter.
	EstimatedCount uint `mapstructure:"estimated_count,omitempty"`
	// FalsePositiveRate is the desired false positive rate (e.g., 0.01 for 1%).
	FalsePositiveRate float64 `mapstructure:"false_positive_rate,omitempty"`
	// MaxEstimatedCount caps the filter sizing (0 = no cap).
	MaxEstimatedCount uint `mapstructure:"max_estimated_count,omitempty"`

	// Cuckoo and Vacuum filter options (kind: cuckoo, vacuum)
	// Capacity is the expected number of elements for cuckoo/vacuum filters.
	Capacity uint `mapstructure:"capacity,omitempty"`

	// Scalable Cuckoo filter options (kind: scalable_cuckoo)
	// InitialCapacity is the starting capacity for scalable cuckoo filters.
	InitialCapacity uint `mapstructure:"initial_capacity,omitempty"`
	// LoadFactor controls when the filter scales (0.0-1.0, default 0.9).
	LoadFactor float32 `mapstructure:"load_factor,omitempty"`

	// Vacuum filter options (kind: vacuum)
	// FingerprintBits controls fingerprint size (default 8, max 32).
	FingerprintBits uint `mapstructure:"fingerprint_bits,omitempty"`

	_ struct{} // prevent unkeyed literal initialization
}

// TelemetryConfig configures telemetry for the AMQ extension.
type TelemetryConfig struct {
	// Enabled enables telemetry reporting.
	Enabled bool `mapstructure:"enabled"`

	// UpdateInterval is the interval at which to update telemetry.
	UpdateInterval time.Duration `mapstructure:"update_interval"`

	_ struct{} // prevent unkeyed literal initialization
}

// Validate returns an error if the config is invalid
func (c Config) Validate() error {
	if len(c.Filters) == 0 {
		return errors.New("at least one filter is required")
	}

	seen := make(map[string]bool)
	for _, f := range c.Filters {
		if err := f.validate(); err != nil {
			return err
		}
		if seen[f.Name] {
			return errors.New("duplicate filter name: " + f.Name)
		}
		seen[f.Name] = true
	}

	if c.ResetInterval < 0 {
		return errors.New("reset_interval must be non-negative")
	}

	if c.Telemetry != nil {
		if err := c.Telemetry.validate(); err != nil {
			return err
		}
	}

	return nil
}

// FilterKind returns the filter.Kind for this config, defaulting to bloom.
func (f FilterConfig) FilterKind() filter.Kind {
	if f.Kind == "" {
		return filter.KindBloom
	}
	return filter.Kind(f.Kind)
}

func (f FilterConfig) validate() error {
	if f.Name == "" {
		return errors.New("filter name is required")
	}

	kind := f.FilterKind()
	switch kind {
	case filter.KindBloom:
		if f.EstimatedCount == 0 {
			return fmt.Errorf("estimated_count is required for bloom filter: %s", f.Name)
		}
		if f.FalsePositiveRate <= 0 || f.FalsePositiveRate >= 1 {
			return fmt.Errorf("false_positive_rate must be between 0 and 1 (exclusive) for bloom filter: %s", f.Name)
		}
	case filter.KindCuckoo:
		if f.Capacity == 0 {
			return fmt.Errorf("capacity is required for cuckoo filter: %s", f.Name)
		}
	case filter.KindScalableCuckoo:
		// InitialCapacity and LoadFactor are optional, have library defaults
	case filter.KindVacuum:
		if f.Capacity == 0 {
			return fmt.Errorf("capacity is required for vacuum filter: %s", f.Name)
		}
	default:
		return fmt.Errorf("unknown filter kind %q for filter: %s", f.Kind, f.Name)
	}

	return nil
}

// ToFilterConfig converts this FilterConfig to the internal filter.FilterConfig interface.
func (f FilterConfig) ToFilterConfig() filter.FilterConfig {
	switch f.FilterKind() {
	case filter.KindBloom:
		return filter.BloomOptions{
			EstimatedCount:    f.EstimatedCount,
			FalsePositiveRate: f.FalsePositiveRate,
			MaxEstimatedCount: f.MaxEstimatedCount,
		}
	case filter.KindCuckoo:
		return filter.CuckooOptions{
			Capacity: f.Capacity,
		}
	case filter.KindScalableCuckoo:
		return filter.ScalableCuckooOptions{
			InitialCapacity: f.InitialCapacity,
			LoadFactor:      f.LoadFactor,
		}
	case filter.KindVacuum:
		return filter.VacuumOptions{
			Capacity:        f.Capacity,
			FingerprintBits: f.FingerprintBits,
		}
	default:
		return nil
	}
}

func (t TelemetryConfig) validate() error {
	if t.UpdateInterval < 0 {
		return errors.New("telemetry update_interval must be non-negative")
	}
	return nil
}
