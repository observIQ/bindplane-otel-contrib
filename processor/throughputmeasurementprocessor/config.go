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

// Package throughputmeasurementprocessor provides a processor that measure the amount of otlp structures flowing through it
package throughputmeasurementprocessor

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/component"
)

var (
	errInvalidSamplingRatio = errors.New("sampling_ratio must be between 0.0 and 1.0")
	errInvalidInterval      = errors.New("interval must be positive or 0")
)

// GlobalConfig carries the settings of the reporter shared by every
// `opamp`-configured throughput processor in the collector. Exactly one
// processor in a configuration should carry it — that processor sets up the
// reporter. If more than one does, the last one to start wins; if none does,
// nothing is reported over opamp.
type GlobalConfig struct {
	// Interval is the interval on which measurements are reported over opamp.
	// Measurements reporting is disabled if this duration is 0 or unset.
	Interval time.Duration `mapstructure:"interval"`

	// ExtraLabels are a map of key-value pairs added to all
	// reported measurements. A processor's own `extra_labels` win on conflicting
	// keys.
	ExtraLabels map[string]string `mapstructure:"extra_labels"`
}

// Config is the configuration for the processor
type Config struct {
	// Enable controls whether measurements are taken or not.
	Enabled bool `mapstructure:"enabled"`

	// SamplingRatio is the ratio of payloads that are measured. Values between 0.0 and 1.0 are valid.
	SamplingRatio float64 `mapstructure:"sampling_ratio"`

	// OpAMP is the component ID of an opamp extension implementing
	// opampcustommessages.CustomCapabilityRegistry. If set, the processor's
	// measurements feed the reporter shared by every opamp-configured
	// throughput processor, which reports them to Bindplane as custom messages
	// on an interval. Every processor should reference the same extension.
	OpAMP component.ID `mapstructure:"opamp"`

	// Global carries the shared reporter's settings; the processor carrying it
	// sets up the reporter. Exactly one processor in a configuration should
	// carry it; see GlobalConfig.
	Global *GlobalConfig `mapstructure:"global"`

	// BindplaneExtension is the component ID of a bindplane extension to register
	// measurements with.
	// Deprecated: configure OpAMP instead. Kept only for backwards compatibility
	// with Bindplane servers that render this field; ignored when OpAMP is set.
	// Delete when all supported Bindplane servers render `opamp` (BPOP-5622).
	BindplaneExtension component.ID `mapstructure:"bindplane_extension"`

	// Extra labels to add to measurements and associate with emitted metrics
	ExtraLabels map[string]string `mapstructure:"extra_labels"`

	// When true, for logs, the processor will measure the raw bytes of the payload in addition to the protobuf size. This is more expensive but provides raw measurements if designated.
	MeasureLogRawBytes bool `mapstructure:"measure_log_raw_bytes"`
}

// Validate validates the processor configuration
func (cfg Config) Validate() error {
	// Processor not enabled no validation needed
	if !cfg.Enabled {
		return nil
	}

	// Validate sampling ration
	if cfg.SamplingRatio < 0.0 || cfg.SamplingRatio > 1.0 {
		return errInvalidSamplingRatio
	}

	if cfg.Global != nil && cfg.Global.Interval < 0 {
		return errInvalidInterval
	}

	return nil
}
