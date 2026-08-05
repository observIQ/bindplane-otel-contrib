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

// Config is the configuration for the processor
type Config struct {
	// Interval is the interval on which topology is reported over opamp.
	// Topology reporting is disabled if this duration is 0.
	// Only used when OpAMP is set.
	Interval time.Duration `mapstructure:"interval"`

	// OpAMP is the component ID of an opamp extension implementing
	// opampcustommessages.CustomCapabilityRegistry. If set, the processor reports
	// its topology state to Bindplane as custom messages on an interval.
	// If unset, the processor falls back to registering its topology state with
	// the extension named by BindplaneExtension, or with the package-level
	// registry read by the bindplane agent when neither is set.
	OpAMP component.ID `mapstructure:"opamp"`

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

	if cfg.Interval < 0 {
		return errInvalidInterval
	}

	return nil
}
