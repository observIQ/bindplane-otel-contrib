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

package bindplaneextension

import (
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/collector/component"
)

// Config is the configuration for the bindplane extension
type Config struct {
	// Labels in "k1=v1,k2=v2" format
	// Deprecated: This was never used and is not supported.
	Labels string `mapstructure:"labels"`
	// Component ID of the opamp extension. If not specified, then
	// this extension will not generate any custom messages for throughput metrics or topology.
	OpAMP component.ID `mapstructure:"opamp"`
	// MeasurementsInterval is the interval on which to report measurements.
	// Measurements reporting is disabled if this duration is 0.
	MeasurementsInterval time.Duration `mapstructure:"measurements_interval"`
	// TopologyInterval is the interval on which to report topology.
	// Topology reporting is disabled if this duration is 0.
	TopologyInterval time.Duration `mapstructure:"topology_interval"`
	// ExtraMeasurementsAttributes are a map of key-value pairs to add to all reported measurements.
	ExtraMeasurementsAttributes map[string]string `mapstructure:"extra_measurements_attributes,omitempty"`
	// SupportBundle configures support bundle collection and upload.
	SupportBundle SupportBundleConfig `mapstructure:"support_bundle,omitempty"`
}

// SupportBundleConfig holds all knobs for on-demand support bundle collection.
type SupportBundleConfig struct {
	// OpAMPEndpoint is the WebSocket endpoint of the OpAMP server (e.g. wss://example.com/opamp).
	// Used to derive the REST upload URL.
	OpAMPEndpoint string `mapstructure:"opamp_endpoint,omitempty"`
	// AgentID is the agent's unique identifier, used to construct the upload path.
	AgentID string `mapstructure:"agent_id,omitempty"`
	// CollectorConfigPath is the path to the OTel collector config file to include in bundles.
	CollectorConfigPath string `mapstructure:"collector_config_path,omitempty"`
	// CollectorLogDir is the directory containing collector log files.
	CollectorLogDir string `mapstructure:"collector_log_dir,omitempty"`
	// ManagerConfigPath is the path to the OpAMP manager config file.
	ManagerConfigPath string `mapstructure:"manager_config_path,omitempty"`
	// CollectorInstallRoot is the collector install root directory. When set the bundle
	// includes plugins/, version.txt, and other install-level artifacts.
	CollectorInstallRoot string `mapstructure:"collector_install_root,omitempty"`
	// EncryptionPublicKeyPath is an optional path to a PEM-encoded RSA public key used to
	// encrypt bundles into BNDL format. When omitted the default embedded key is used.
	EncryptionPublicKeyPath string `mapstructure:"encryption_public_key_path,omitempty"`
}

// Validate returns an error if the config is invalid
func (c Config) Validate() error {
	if c.MeasurementsInterval < 0 {
		return errors.New("measurements interval must be positive or 0")
	}

	if c.TopologyInterval < 0 {
		return errors.New("topology interval must be positive or 0")
	}

	if path := c.SupportBundle.EncryptionPublicKeyPath; path != "" {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("support_bundle.encryption_public_key_path %q: %w", path, err)
		}
	}

	return nil
}
