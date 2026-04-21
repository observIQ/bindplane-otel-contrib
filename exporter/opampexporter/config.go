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

package opampexporter

import (
	"errors"

	"go.opentelemetry.io/collector/component"
)

var defaultOpAMPExtensionID = component.MustNewID("opamp")

// Config is the configuration for the opamp exporter.
type Config struct {
	// OpAMP is the component ID of the opamp extension to use to register the
	// custom capability and send custom messages.
	OpAMP component.ID `mapstructure:"opamp"`
}

// Validate validates the exporter configuration.
func (cfg Config) Validate() error {
	var emptyID component.ID
	if cfg.OpAMP == emptyID {
		return errors.New("`opamp` must be specified")
	}

	return nil
}
