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
	unknownLogType                    = "unknown"
)

var (
	errMissingLogTypeField    = errors.New("log_type_field is required")
	errInvalidPersistInterval = errors.New("fingerprint_persist_interval must be > 0")
	errInvalidMaxFingerprints = errors.New("max_saved_fingerprints must be > 0")
)

// Config is the config of the processor.
type Config struct {
	Matchers         []MatcherConfig `mapstructure:"matchers"`
	FingerprintField string          `mapstructure:"fingerprint_field"`
	LogTypeField     string          `mapstructure:"log_type_field"`

	// FingerprintStorageID is the storage extension used to persist the fingerprint map.
	FingerprintStorageID *component.ID `mapstructure:"fingerprint_storage"`

	// FingerprintPersistInterval is how often the fingerprint map is persisted.
	FingerprintPersistInterval time.Duration `mapstructure:"fingerprint_persist_interval"`

	// MaxSavedFingerprints is the maximum number of mappings held in memory.
	MaxSavedFingerprints int `mapstructure:"max_saved_fingerprints"`
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

	if c.FingerprintStorageID != nil && c.FingerprintPersistInterval <= 0 {
		return errInvalidPersistInterval
	}

	if c.MaxSavedFingerprints <= 0 {
		return errInvalidMaxFingerprints
	}

	for _, m := range c.Matchers {
		if err := m.Validate(); err != nil {
			return err
		}
	}
	return nil
}
