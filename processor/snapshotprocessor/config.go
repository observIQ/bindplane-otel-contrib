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
	"time"

	"go.opentelemetry.io/collector/component"
)

var defaultOpAMPExtensionID = component.MustNewID("opamp")

// defaultRefreshInterval bounds how often full snapshot buffers are refreshed
// with a new batch. A snapshot is an on-demand debug view, so refreshing it
// faster than a few times per second buys nothing a human can perceive.
const defaultRefreshInterval = 250 * time.Millisecond

// Config is the configuration for the processor
type Config struct {
	// Enable controls whether snapshots are collected
	Enabled bool         `mapstructure:"enabled"`
	OpAMP   component.ID `mapstructure:"opamp"`

	// RefreshInterval bounds how often a telemetry batch is admitted to the
	// snapshot buffers once they are full. Batches arriving inside the
	// interval pass through with no buffering cost. Zero admits every batch.
	// Defaults to 250ms.
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`
}

// Validate validates the processor configuration
func (cfg Config) Validate() error {
	var emptyID component.ID
	if cfg.OpAMP == emptyID {
		return errors.New("`opamp` must be specified")
	}

	if cfg.RefreshInterval < 0 {
		return errors.New("`refresh_interval` cannot be negative")
	}

	return nil
}
