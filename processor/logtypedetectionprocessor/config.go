// Copyright  observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logtypedetectionprocessor

import (
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/go-version"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
)

const (
	defaultFingerprintField           = "fingerprint"
	defaultLogTypeField               = "log_type"
	defaultFingerprintPersistInterval = 5 * time.Minute
	defaultMaxSavedFingerprints       = 10_000
	defaultOpAMPRequestTimeout        = 30 * time.Second
	unknownLogType                    = "unknown"
)

var (
	errMissingLogTypeField    = errors.New("log_type_field is required")
	errInvalidPersistInterval = errors.New("fingerprint_persist_interval must be > 0")
	errInvalidMaxFingerprints = errors.New("max_saved_fingerprints must be > 0")
	errMissingOpAMPExtension  = errors.New("opamp::extension is required")
	errInvalidOpAMPTimeout    = errors.New("opamp::request_timeout must be >= 0")
	errInvalidOpAMPMaxVersion = errors.New("opamp::matchers_version must be a semver version")
)

// Config is the config of the processor.
type Config struct {
	Matchers         []MatcherConfig `mapstructure:"matchers"`
	FingerprintField string          `mapstructure:"fingerprint_field"`
	LogTypeField     string          `mapstructure:"log_type_field"`

	// StorageID is the storage extension used to persist the fingerprint map and opamp matchers.
	StorageID *component.ID `mapstructure:"storage"`

	// FingerprintPersistInterval is how often the fingerprint map is persisted.
	FingerprintPersistInterval time.Duration `mapstructure:"fingerprint_persist_interval"`

	// MaxSavedFingerprints is the maximum number of mappings held in memory.
	MaxSavedFingerprints int `mapstructure:"max_saved_fingerprints"`

	// OpAMP loads matchers from an opamp server when set.
	OpAMP *OpAMPConfig `mapstructure:"opamp"`
}

// OpAMPConfig is how matchers are fetched from an opamp server.
type OpAMPConfig struct {
	// Extension is the ID of the opamp extension to send requests through.
	Extension component.ID `mapstructure:"extension"`

	// MatchersVersion is the highest matcher set version to accept, empty for the newest.
	MatchersVersion string `mapstructure:"matchers_version"`

	// RequestTimeout is how long the processor keeps asking for matchers, 0 to keep asking indefinitely.
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
}

// Unmarshal presets the defaults of the optional opamp block.
func (c *OpAMPConfig) Unmarshal(conf *confmap.Conf) error {
	c.RequestTimeout = defaultOpAMPRequestTimeout
	return conf.Unmarshal(c)
}

func createDefaultConfig() component.Config {
	return &Config{
		Matchers:                   []MatcherConfig{},
		FingerprintField:           defaultFingerprintField,
		LogTypeField:               defaultLogTypeField,
		FingerprintPersistInterval: defaultFingerprintPersistInterval,
		MaxSavedFingerprints:       defaultMaxSavedFingerprints,
	}
}

// Validate validates the processor configuration
func (c Config) Validate() error {
	if c.LogTypeField == "" {
		return errMissingLogTypeField
	}

	if c.StorageID != nil && c.FingerprintPersistInterval <= 0 {
		return errInvalidPersistInterval
	}

	if c.MaxSavedFingerprints <= 0 {
		return errInvalidMaxFingerprints
	}

	if c.OpAMP != nil {
		if c.OpAMP.Extension == (component.ID{}) {
			return errMissingOpAMPExtension
		}
		if c.OpAMP.RequestTimeout < 0 {
			return errInvalidOpAMPTimeout
		}
		if c.OpAMP.MatchersVersion != "" {
			if _, err := version.NewVersion(c.OpAMP.MatchersVersion); err != nil {
				return fmt.Errorf("%w: %w", errInvalidOpAMPMaxVersion, err)
			}
		}
	}

	for _, m := range c.Matchers {
		if err := m.Validate(); err != nil {
			return err
		}
	}
	return nil
}
