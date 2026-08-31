// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package textencodingextension // import "github.com/observiq/bindplane-otel-contrib/extension/encoding/textencodingextension"
import (
	"regexp"

	"github.com/observiq/bindplane-otel-contrib/extension/encoding/textencodingextension/internal/textutils"
)

// Config defines the configuration for the text encoding extension.
type Config struct {
	Encoding              string `mapstructure:"encoding"`
	MarshalingSeparator   string `mapstructure:"marshaling_separator"`
	UnmarshalingSeparator string `mapstructure:"unmarshaling_separator"`
	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate checks the extension configuration.
func (c *Config) Validate() error {
	if c.UnmarshalingSeparator != "" {
		if _, err := regexp.Compile(c.UnmarshalingSeparator); err != nil {
			return err
		}
	}
	_, err := textutils.LookupEncoding(c.Encoding)
	if err != nil {
		return err
	}
	return nil
}
