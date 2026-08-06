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

// Package topologyprocessor collects metrics, traces, and logs for
package topologyprocessor

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/component"
)

var errInvalidInterval = errors.New("interval must be positive or 0")

// GlobalConfig carries the settings of the reporter shared by every topology
// processor in the collector. Only one processor in a configuration should
// carry it; if more than one does, the last one to start wins. If none does,
// the reporter uses its defaults (1m interval).
type GlobalConfig struct {
	// Interval is the interval on which topology is reported over opamp.
	// Topology reporting is disabled if this duration is 0.
	Interval time.Duration `mapstructure:"interval"`
}

// Config is the configuration for the processor
type Config struct {
	// Interval is unused.
	// Deprecated: This parameter is only used in topology processor v1.75.0 and
	// earlier. Old Bindplane servers render it, so it must remain for their
	// configs to unmarshal. Delete with BPOP-5623.
	Interval time.Duration `mapstructure:"interval"`

	// OpAMP is the component ID of an opamp extension implementing
	// opampcustommessages.CustomCapabilityRegistry. If set, the reporter shared
	// by every topology processor reports all topology state to Bindplane as
	// custom messages on an interval. Every processor should reference the same
	// extension; the last distinct value to start wins.
	// If unset, the processor's topology state still feeds the shared reporter.
	OpAMP component.ID `mapstructure:"opamp"`

	// Global carries the shared reporter's settings. Only one processor in a
	// configuration should carry it; see GlobalConfig.
	Global *GlobalConfig `mapstructure:"global"`

	// BindplaneExtension is the component ID of a bindplane extension to register
	// topology state with.
	// Deprecated: configure OpAMP instead. Kept only for backwards compatibility
	// with Bindplane servers that render this field; ignored when OpAMP is set.
	// Delete when all supported Bindplane servers render `opamp` (BPOP-5623).
	BindplaneExtension *component.ID `mapstructure:"bindplane_extension"`

	// Name of the Config where this processor is present
	Configuration string `mapstructure:"configuration"`

	// OrganizationID of the Org where this processor is present
	OrganizationID string `mapstructure:"organizationID"`

	// AccountID of the Account where this processor is present
	AccountID string `mapstructure:"accountID"`
}

// Validate validates the processor configuration
func (cfg Config) Validate() error {
	if cfg.Configuration == "" {
		return errors.New("`configuration` must be specified")
	}

	if cfg.OrganizationID == "" {
		return errors.New("`organizationID` must be specified")
	}

	if cfg.AccountID == "" {
		return errors.New("`accountID` must be specified")
	}

	if cfg.Global != nil && cfg.Global.Interval < 0 {
		return errInvalidInterval
	}

	return nil
}
