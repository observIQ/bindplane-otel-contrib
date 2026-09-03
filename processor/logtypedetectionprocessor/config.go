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
	"time"

	"go.opentelemetry.io/collector/component"
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
	errInvalidOpAMPTimeout    = errors.New("opamp_request_timeout must be >= 0")
	errMatcherStorageNoOpAMP  = errors.New("matcher_storage requires opamp to be set")
)

// Config is the config of the processor.
type Config struct {
	Matchers         []MatcherConfig `mapstructure:"matchers"`
	FingerprintField string          `mapstructure:"fingerprint_field"`
	LogTypeField     string          `mapstructure:"log_type_field"`

	// ID of the storage extension used to persist the fingerprint map
	FingerprintStorageID *component.ID `mapstructure:"fingerprint_storage"`

	// How often the fingerprint map is written to the storage extension
	FingerprintPersistInterval time.Duration `mapstructure:"fingerprint_persist_interval"`

	// Maximum number of fingerprint-to-log-type mappings held in memory
	MaxSavedFingerprints int `mapstructure:"max_saved_fingerprints"`

	// ID of the opamp extension used to load matchers from an opamp server
	OpAMP *component.ID `mapstructure:"opamp"`

	// How long startup waits for the opamp server to send matchers, 0 to wait indefinitely
	OpAMPRequestTimeout time.Duration `mapstructure:"opamp_request_timeout"`

	// ID of the storage extension used to persist matchers received over opamp
	MatcherStorageID *component.ID `mapstructure:"matcher_storage"`
}

func createDefaultConfig() component.Config {
	return &Config{
		Matchers:                   []MatcherConfig{},
		FingerprintField:           defaultFingerprintField,
		LogTypeField:               defaultLogTypeField,
		FingerprintPersistInterval: defaultFingerprintPersistInterval,
		MaxSavedFingerprints:       defaultMaxSavedFingerprints,
		OpAMPRequestTimeout:        defaultOpAMPRequestTimeout,
	}
}

// Validate validates the processor configuration
func (c Config) Validate() error {
	if c.LogTypeField == "" {
		return errMissingLogTypeField
	}

	if c.FingerprintStorageID != nil && c.FingerprintPersistInterval <= 0 {
		return errInvalidPersistInterval
	}

	if c.MaxSavedFingerprints <= 0 {
		return errInvalidMaxFingerprints
	}

	if c.OpAMP != nil && c.OpAMPRequestTimeout < 0 {
		return errInvalidOpAMPTimeout
	}

	if c.MatcherStorageID != nil && c.OpAMP == nil {
		return errMatcherStorageNoOpAMP
	}

	for _, m := range c.Matchers {
		if err := m.Validate(); err != nil {
			return err
		}
	}
	return nil
}
