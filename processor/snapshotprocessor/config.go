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

// Package snapshotprocessor collects metrics, traces, and logs for
package snapshotprocessor

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"go.opentelemetry.io/collector/component"
)

var defaultOpAMPExtensionID = component.MustNewID("opamp")

// defaultRefreshInterval bounds how often full snapshot buffers are refreshed
// with a new batch. A snapshot is an on-demand debug view, so refreshing it
// faster than a few times per second buys nothing a human can perceive.
const defaultRefreshInterval = 250 * time.Millisecond

// defaultBufferSize is the default number of log records, metric data
// points, or spans retained per signal type.
const defaultBufferSize = 100

// maxBufferSize caps buffer_size: every buffered item is copied and retained
// in memory, and snapshot payloads are size-limited anyway.
const maxBufferSize = 10_000

// validSignals are the accepted values for the signals option.
var validSignals = []string{"logs", "metrics", "traces"}

// Config is the configuration for the processor
type Config struct {
	// Enable controls whether snapshots are collected
	Enabled bool         `mapstructure:"enabled"`
	OpAMP   component.ID `mapstructure:"opamp"`

	// BufferSize is the approximate number of log records, metric data
	// points, or spans retained per signal type. Defaults to 100.
	BufferSize int `mapstructure:"buffer_size"`

	// RefreshInterval bounds how often a telemetry batch is admitted to the
	// snapshot buffers once they are full. Batches arriving inside the
	// interval pass through with no buffering cost. Zero admits every batch.
	// Defaults to 250ms.
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`

	// Signals limits which signal types are buffered ("logs", "metrics",
	// "traces"). Signal types not listed pass through with no buffering
	// cost. An empty list buffers all signal types.
	Signals []string `mapstructure:"signals"`
}

// Validate validates the processor configuration
func (cfg Config) Validate() error {
	var emptyID component.ID
	if cfg.OpAMP == emptyID {
		return errors.New("`opamp` must be specified")
	}

	if cfg.BufferSize <= 0 {
		return errors.New("`buffer_size` must be positive")
	}
	if cfg.BufferSize > maxBufferSize {
		return fmt.Errorf("`buffer_size` cannot exceed %d", maxBufferSize)
	}

	if cfg.RefreshInterval < 0 {
		return errors.New("`refresh_interval` cannot be negative")
	}

	for _, signal := range cfg.Signals {
		if !slices.Contains(validSignals, signal) {
			return fmt.Errorf("invalid signal type %q: must be one of %v", signal, validSignals)
		}
	}

	return nil
}

// buffersSignal reports whether the given signal type should be buffered.
func (cfg Config) buffersSignal(signal string) bool {
	if len(cfg.Signals) == 0 {
		return true
	}
	return slices.Contains(cfg.Signals, signal)
}
