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

// GlobalConfig carries the settings of the reporter shared by every
// `opamp`-configured topology processor in the collector. Exactly one
// processor in a configuration should carry it — that processor sets up the
// reporter. If more than one does, the last one to start wins; if none does,
// nothing is reported over opamp.
type GlobalConfig struct {
	// Interval is the interval on which topology is reported over opamp.
	// Topology reporting is disabled if this duration is 0 or unset.
	Interval time.Duration `mapstructure:"interval"`

	// Configuration is the name of the Bindplane configuration this collector
	// is running. Stamped on every reported gateway source.
	Configuration string `mapstructure:"configuration"`

	// OrganizationID is the ID of the Bindplane organization this collector is
	// running in. Stamped on every reported gateway source.
	OrganizationID string `mapstructure:"organizationID"`

	// AccountID is the ID of the Bindplane account this collector is running
	// in. Stamped on every reported gateway source.
	AccountID string `mapstructure:"accountID"`
}

// Config is the configuration for the processor
type Config struct {
	// Interval is unused.
	// Deprecated: This parameter is only used in topology processor v1.75.0 and
	// earlier. Old Bindplane servers render it, so it must remain for their
	// configs to unmarshal. Delete with BPOP-5623.
	Interval time.Duration `mapstructure:"interval"`

	// OpAMP is the component ID of an opamp extension implementing
	// opampcustommessages.CustomCapabilityRegistry. If set, the processor's
	// topology state feeds the reporter shared by every opamp-configured
	// topology processor, which reports it to Bindplane as custom messages on
	// an interval. Every processor should reference the same extension.
	OpAMP component.ID `mapstructure:"opamp"`

	// Global carries the shared reporter's settings; the processor carrying it
	// sets up the reporter. Exactly one processor in a configuration should
	// carry it; see GlobalConfig.
	Global *GlobalConfig `mapstructure:"global"`

	// BindplaneExtension is the component ID of a bindplane extension to register
	// topology state with.
	// Deprecated: configure OpAMP instead. Kept only for backwards compatibility
	// with Bindplane servers that render this field; ignored when OpAMP is set.
	// Delete when all supported Bindplane servers render `opamp` (BPOP-5623).
	BindplaneExtension *component.ID `mapstructure:"bindplane_extension"`

	// Name of the Config where this processor is present.
	// Deprecated: set in Global instead. Only used by the deprecated
	// bindplane_extension/v1 paths, which old Bindplane servers render.
	// Delete with BPOP-5623.
	Configuration string `mapstructure:"configuration"`

	// OrganizationID of the Org where this processor is present.
	// Deprecated: set in Global instead. Only used by the deprecated
	// bindplane_extension/v1 paths, which old Bindplane servers render.
	// Delete with BPOP-5623.
	OrganizationID string `mapstructure:"organizationID"`

	// AccountID of the Account where this processor is present.
	// Deprecated: set in Global instead. Only used by the deprecated
	// bindplane_extension/v1 paths, which old Bindplane servers render.
	// Delete with BPOP-5623.
	AccountID string `mapstructure:"accountID"`
}

// Validate validates the processor configuration
func (cfg Config) Validate() error {
	// The deprecated paths (no opamp) source the gateway identity from the
	// top-level fields, as old Bindplane servers render them.
	var emptyID component.ID
	if cfg.OpAMP == emptyID {
		if cfg.Configuration == "" {
			return errors.New("`configuration` must be specified")
		}

		if cfg.OrganizationID == "" {
			return errors.New("`organizationID` must be specified")
		}

		if cfg.AccountID == "" {
			return errors.New("`accountID` must be specified")
		}
	}

	if cfg.Global != nil {
		if cfg.Global.Configuration == "" {
			return errors.New("`global.configuration` must be specified")
		}

		if cfg.Global.OrganizationID == "" {
			return errors.New("`global.organizationID` must be specified")
		}

		if cfg.Global.AccountID == "" {
			return errors.New("`global.accountID` must be specified")
		}

		if cfg.Global.Interval < 0 {
			return errInvalidInterval
		}
	}

	return nil
}
