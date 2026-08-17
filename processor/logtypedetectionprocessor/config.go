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

	"go.opentelemetry.io/collector/component"
)

const (
	defaultFingerprintField = "fingerprint"
	defaultLogTypeField     = "log_type"
)

var errMissingLogTypeFieldError = errors.New("log_type_field is required")

// Config is the config of the processor.
type Config struct {
	Matchers         []MatcherConfig `mapstructure:"matchers"`
	FingerprintField string          `mapstructure:"fingerprint_field"`
	LogTypeField     string          `mapstructure:"log_type_field"`
}

func createDefaultConfig() component.Config {
	return &Config{
		Matchers:         []MatcherConfig{},
		FingerprintField: defaultFingerprintField,
		LogTypeField:     defaultLogTypeField,
	}
}

// Validate validates the processor configuration
func (c Config) Validate() error {
	if c.LogTypeField == "" {
		return errMissingLogTypeFieldError
	}

	if c.Matchers != nil {
		for _, m := range c.Matchers {
			if err := m.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}
