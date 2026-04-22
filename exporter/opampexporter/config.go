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
	// exporter when `capability` is not overridden.
	defaultCapability = "com.bindplane.opampexporter"
	// defaultMessageType is the OpAMP custom message type used for each
	// payload when `message_type` is not overridden.
	defaultMessageType = "otlp-snappy"
)

// Config is the configuration for the opamp exporter.
type Config struct {
	// OpAMP is the component ID of the opamp extension to use to register the
	// custom capability and send custom messages.
	OpAMP component.ID `mapstructure:"opamp"`

	// Capability is the OpAMP custom capability registered by this exporter
	// and used on every outgoing custom message. Configure distinct
	// capabilities on separate exporter instances to differentiate payload
	// streams (e.g. throughput vs. health metrics).
	Capability string `mapstructure:"capability"`

	// MessageType is the OpAMP custom message type used for each outgoing
	// payload. The default indicates the payload is snappy-compressed
	// OTLP-proto.
	MessageType string `mapstructure:"message_type"`
}

// Validate validates the exporter configuration.
func (cfg Config) Validate() error {
	var emptyID component.ID
	if cfg.OpAMP == emptyID {
		return errors.New("`opamp` must be specified")
	}
	if cfg.Capability == "" {
		return errors.New("`capability` must be specified")
	}
	if cfg.MessageType == "" {
		return errors.New("`message_type` must be specified")
	}

	return nil
}
