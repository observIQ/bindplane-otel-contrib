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

// Package threats provides configuration for STIX/TAXII CLI commands.
package threats

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/validator"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/envprovider"
	"go.opentelemetry.io/collector/confmap/provider/fileprovider"
)

// Environment variable names for config file paths.
const (
	EnvConfigFile     = "STIX_CONFIG"
	EnvConfigOverride = "STIX_CONFIG_OVERRIDE"
)

// Config is the root configuration for STIX/TAXII CLI commands.
type Config struct {
	Validator   ValidatorConfig   `mapstructure:"validator"`
	TaxiiServer TaxiiServerConfig `mapstructure:"taxii_server"`
	TaxiiClient TaxiiClientConfig `mapstructure:"taxii_client"`
	Exchange    ExchangeConfig    `mapstructure:"exchange"`
	Match       MatchConfig       `mapstructure:"match"`
}

// ValidatorConfig configures the STIX validator.
type ValidatorConfig struct {
	SchemaDir             string   `mapstructure:"schema_dir"`
	Version               string   `mapstructure:"version"`
	Disabled              []string `mapstructure:"disabled"`
	Enabled               []string `mapstructure:"enabled"`
	Strict                bool     `mapstructure:"strict"`
	StrictTypes           bool     `mapstructure:"strict_types"`
	StrictProperties      bool     `mapstructure:"strict_properties"`
	EnforceRefs           bool     `mapstructure:"enforce_refs"`
	Interop               bool     `mapstructure:"interop"`
	Verbose               bool     `mapstructure:"verbose"`
	Silent                bool     `mapstructure:"silent"`
	MaxConcurrentObjects  int      `mapstructure:"max_concurrent_objects"`
	ParallelizeBundles    bool     `mapstructure:"parallelize_bundles"`
	PreserveObjectOrder   bool     `mapstructure:"preserve_object_order"`
	UseStreaming          bool     `mapstructure:"use_streaming"`
	StreamingMinSizeBytes int64    `mapstructure:"streaming_min_size_bytes"`
	MaxObjectsPerBundle   int      `mapstructure:"max_objects_per_bundle"`
	MaxErrorsPerObject    int      `mapstructure:"max_errors_per_object"`
	MaxErrorsPerFile      int      `mapstructure:"max_errors_per_file"`
	FailFast              bool     `mapstructure:"fail_fast"`
}

// TaxiiServerConfig configures the TAXII 2.1 server.
type TaxiiServerConfig struct {
	Addr        string `mapstructure:"addr"`
	Base        string `mapstructure:"base"`
	MaxPageSize int    `mapstructure:"max_page_size"`
	Users       string `mapstructure:"users"`
	UsersFile   string `mapstructure:"users_file"`
	DataFile    string `mapstructure:"data_file"`
}

// TaxiiClientConfig configures the TAXII 2.1 client.
type TaxiiClientConfig struct {
	DiscoveryURL string `mapstructure:"discovery_url"`
	Username     string `mapstructure:"username"`
	Password     string `mapstructure:"password"`
}

// ExchangeConfig configures threat intel exchange connections.
type ExchangeConfig struct {
	APIKey string `mapstructure:"api_key"`
}

// MatchConfig configures the STIX pattern matcher.
type MatchConfig struct {
	Verbose bool `mapstructure:"verbose"`
}

// LoadOptions configures how config is loaded.
type LoadOptions struct {
	BasePath     string
	OverridePath string
	SkipEnv      bool
}

// DefaultLoadOptions returns options that use env vars for paths.
func DefaultLoadOptions() LoadOptions {
	return LoadOptions{}
}

// Load reads config files and returns a Config. Environment variable substitution
// (${VAR} syntax) is handled by OTel's confmap envprovider.
func Load(ctx context.Context, opts LoadOptions) (*Config, error) {
	basePath := opts.BasePath
	overridePath := opts.OverridePath

	if !opts.SkipEnv {
		if basePath == "" {
			basePath = os.Getenv(EnvConfigFile)
		}
		if overridePath == "" {
			overridePath = os.Getenv(EnvConfigOverride)
		}
	}

	// No config files specified - return defaults
	if basePath == "" && overridePath == "" {
		return DefaultConfig(), nil
	}

	// Build list of URIs for confmap resolver
	var uris []string
	if basePath != "" {
		if _, err := os.Stat(basePath); err != nil {
			if os.IsNotExist(err) {
				return DefaultConfig(), nil
			}
			return nil, fmt.Errorf("stat base config %s: %w", basePath, err)
		}
		uris = append(uris, filepath.Clean(basePath))
	}
	if overridePath != "" {
		if _, err := os.Stat(overridePath); err != nil {
			if os.IsNotExist(err) && len(uris) > 0 {
				// Override doesn't exist but base does - continue with base only
			} else if os.IsNotExist(err) {
				return DefaultConfig(), nil
			} else {
				return nil, fmt.Errorf("stat override config %s: %w", overridePath, err)
			}
		} else {
			uris = append(uris, filepath.Clean(overridePath))
		}
	}

	if len(uris) == 0 {
		return DefaultConfig(), nil
	}

	resolver, err := confmap.NewResolver(confmap.ResolverSettings{
		URIs: uris,
		ProviderFactories: []confmap.ProviderFactory{
			fileprovider.NewFactory(),
			envprovider.NewFactory(),
		},
		ConverterFactories: []confmap.ConverterFactory{},
		DefaultScheme:      "file",
	})
	if err != nil {
		return nil, fmt.Errorf("create config resolver: %w", err)
	}

	conf, err := resolver.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}

	cfg := DefaultConfig()
	if err := conf.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return cfg, nil
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Validator:   DefaultValidatorConfig(),
		TaxiiServer: DefaultTaxiiServerConfig(),
		TaxiiClient: TaxiiClientConfig{},
		Exchange:    ExchangeConfig{},
		Match:       MatchConfig{},
	}
}

// DefaultValidatorConfig returns a ValidatorConfig with sensible defaults.
func DefaultValidatorConfig() ValidatorConfig {
	return ValidatorConfig{
		Version:             "2.1",
		PreserveObjectOrder: true,
	}
}

// DefaultTaxiiServerConfig returns a TaxiiServerConfig with sensible defaults.
func DefaultTaxiiServerConfig() TaxiiServerConfig {
	return TaxiiServerConfig{
		Addr:        "localhost:8080",
		Base:        "/taxii2",
		MaxPageSize: 10000,
	}
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	var errs []error

	if err := c.Validator.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("validator: %w", err))
	}
	if err := c.TaxiiServer.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("taxii_server: %w", err))
	}
	if err := c.TaxiiClient.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("taxii_client: %w", err))
	}

	return errors.Join(errs...)
}

// Validate checks if the validator configuration is valid.
func (c *ValidatorConfig) Validate() error {
	if c.Version != "" && c.Version != "2.0" && c.Version != "2.1" {
		return fmt.Errorf("unsupported version %q (expected 2.0 or 2.1)", c.Version)
	}
	if c.MaxConcurrentObjects < 0 {
		return errors.New("max_concurrent_objects must be non-negative")
	}
	if c.MaxObjectsPerBundle < 0 {
		return errors.New("max_objects_per_bundle must be non-negative")
	}
	if c.MaxErrorsPerObject < 0 {
		return errors.New("max_errors_per_object must be non-negative")
	}
	if c.MaxErrorsPerFile < 0 {
		return errors.New("max_errors_per_file must be non-negative")
	}
	return nil
}

// Validate checks if the TAXII server configuration is valid.
func (c *TaxiiServerConfig) Validate() error {
	if c.Addr != "" {
		if _, _, err := net.SplitHostPort(c.Addr); err != nil {
			return fmt.Errorf("invalid addr %q: %w", c.Addr, err)
		}
	}
	if c.MaxPageSize < 0 {
		return errors.New("max_page_size must be non-negative")
	}
	return nil
}

// Validate checks if the TAXII client configuration is valid.
func (c *TaxiiClientConfig) Validate() error {
	return nil
}

// ValidatorConfig converts the Config to a validator.Config for use with the validator package.
func (c *Config) ValidatorConfig() validator.Config {
	return validator.Config{
		Options: validator.Options{
			SchemaDir:        c.Validator.SchemaDir,
			Version:          c.Validator.Version,
			Disabled:         c.Validator.Disabled,
			Enabled:          c.Validator.Enabled,
			Strict:           c.Validator.Strict,
			StrictTypes:      c.Validator.StrictTypes,
			StrictProperties: c.Validator.StrictProperties,
			EnforceRefs:      c.Validator.EnforceRefs,
			Interop:          c.Validator.Interop,
			Verbose:          c.Validator.Verbose,
			Silent:           c.Validator.Silent,
		},
		MaxConcurrentObjects:  c.Validator.MaxConcurrentObjects,
		ParallelizeBundles:    c.Validator.ParallelizeBundles,
		PreserveObjectOrder:   c.Validator.PreserveObjectOrder,
		UseStreaming:          c.Validator.UseStreaming,
		StreamingMinSizeBytes: c.Validator.StreamingMinSizeBytes,
		MaxObjectsPerBundle:   c.Validator.MaxObjectsPerBundle,
		MaxErrorsPerObject:    c.Validator.MaxErrorsPerObject,
		MaxErrorsPerFile:      c.Validator.MaxErrorsPerFile,
		FailFast:              c.Validator.FailFast,
	}
}
