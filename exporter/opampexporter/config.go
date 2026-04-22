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

const (
	// defaultCapability is the OpAMP custom capability registered by the
	// exporter when `custom_message.capability` is not overridden.
	defaultCapability = "com.bindplane.opampexporter"
	// defaultMessageType is the OpAMP custom message type used for each
	// payload when `custom_message.type` is not overridden.
	defaultMessageType = "otlp"
)

// Config is the configuration for the opamp exporter.
type Config struct {
	// OpAMP is the component ID of the opamp extension to use to register the
	// custom capability and send custom messages.
	OpAMP component.ID `mapstructure:"opamp"`

	// CustomMessage controls the OpAMP custom capability and message type
	// used for every outgoing payload. Configure distinct values on separate
	// exporter instances to differentiate payload streams (e.g. throughput
	// vs. health metrics).
	CustomMessage CustomMessageConfig `mapstructure:"custom_message"`
}

// CustomMessageConfig configures the OpAMP custom capability and message
// type used by the exporter.
type CustomMessageConfig struct {
	// Capability is the OpAMP custom capability registered by this exporter
	// and used on every outgoing custom message.
	Capability string `mapstructure:"capability"`

	// Type is the OpAMP custom message type used for each outgoing payload.
	Type string `mapstructure:"type"`
}

// Validate validates the exporter configuration.
func (cfg Config) Validate() error {
	var emptyID component.ID
	if cfg.OpAMP == emptyID {
		return errors.New("`opamp` must be specified")
	}
	if cfg.CustomMessage.Capability == "" {
		return errors.New("`custom_message.capability` must be specified")
	}
	if cfg.CustomMessage.Type == "" {
		return errors.New("`custom_message.type` must be specified")
	}

	return nil
}
