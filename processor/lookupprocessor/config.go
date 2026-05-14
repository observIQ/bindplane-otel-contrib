// Copyright  observIQ, Inc.
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

// Package lookupprocessor provides a processor that looks up values and adds them to telemetry.
package lookupprocessor

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/component"
)

const (
	bodyContext       = "body"
	attributesContext = "attributes"
	resourceContext   = "resource.attributes"

	sourceTypeCSV   = "csv"
	sourceTypeRedis = "redis"
	sourceTypeAPI   = "api"
)

var (
	errMissingContext     = errors.New("missing required field 'context'")
	errMissingField       = errors.New("missing required field 'field'")
	errInvalidContext     = errors.New("invalid context")
	errMissingSource      = errors.New("must specify one of 'csv', 'redis', or 'api' configuration")
	errMultipleSources    = errors.New("only one of 'csv', 'redis', or 'api' may be configured")
	errInvalidSourceType  = errors.New("invalid source_type, must be one of 'csv', 'redis', 'api'")
	errSourceTypeMismatch = errors.New("source_type does not match the configured source block")
	errMissingRedisAddr   = errors.New("redis address is required")
	errMissingAPIURL      = errors.New("api url is required")
)

// Config is the configuration for the processor.
type Config struct {
	Context    string `mapstructure:"context"`
	Field      string `mapstructure:"field"`
	SourceType string `mapstructure:"source_type"`

	CacheEnabled bool          `mapstructure:"cache_enabled"`
	CacheTTL     time.Duration `mapstructure:"cache_ttl"`
	StorageID    *component.ID `mapstructure:"storage"`

	CSV   string       `mapstructure:"csv"`
	Redis *RedisConfig `mapstructure:"redis"`
	API   *APIConfig   `mapstructure:"api"`
}

// APIConfig is the configuration for API-based lookups.
//
// Timeout bounds a single HTTP request attempt. LookupTimeout bounds the full
// Lookup including retries — without it, a chain of retried slow requests can
// exceed Timeout*MaxRetries before failing. MaxRetries/InitialDelay/RetryMultiplier
// govern the exponential backoff schedule between attempts.
type APIConfig struct {
	URL             string            `mapstructure:"url"`
	Method          string            `mapstructure:"method"`
	Headers         map[string]string `mapstructure:"headers"`
	Timeout         time.Duration     `mapstructure:"timeout"`
	LookupTimeout   time.Duration     `mapstructure:"lookup_timeout"`
	MaxRetries      int               `mapstructure:"max_retries"`
	InitialDelay    time.Duration     `mapstructure:"initial_delay"`
	RetryMultiplier int               `mapstructure:"retry_multiplier"`
	ResponseMapping map[string]string `mapstructure:"response_mapping"`
}

// RedisConfig is the configuration for Redis-based lookups.
//
// DialTimeout bounds the initial TCP/TLS dial; LookupTimeout bounds each call
// against a connected server. Both have defaults applied by the source if left
// unset.
type RedisConfig struct {
	Address       string        `mapstructure:"address"`
	Username      string        `mapstructure:"username"`
	Password      string        `mapstructure:"password"`
	DB            int           `mapstructure:"db"`
	TLS           bool          `mapstructure:"tls"`
	KeyPrefix     string        `mapstructure:"key_prefix"`
	DialTimeout   time.Duration `mapstructure:"dial_timeout"`
	LookupTimeout time.Duration `mapstructure:"lookup_timeout"`
}

// Validate validates the processor configuration.
func (cfg Config) Validate() error {
	if cfg.Context == "" {
		return errMissingContext
	}
	if cfg.Field == "" {
		return errMissingField
	}

	switch cfg.Context {
	case bodyContext, attributesContext, resourceContext:
	default:
		return errInvalidContext
	}

	sourceCount := 0
	if cfg.CSV != "" {
		sourceCount++
	}
	if cfg.Redis != nil {
		sourceCount++
	}
	if cfg.API != nil {
		sourceCount++
	}

	if sourceCount == 0 {
		return errMissingSource
	}
	if sourceCount > 1 {
		return errMultipleSources
	}

	if cfg.SourceType != "" {
		switch cfg.SourceType {
		case sourceTypeCSV:
			if cfg.CSV == "" {
				return errSourceTypeMismatch
			}
		case sourceTypeRedis:
			if cfg.Redis == nil {
				return errSourceTypeMismatch
			}
		case sourceTypeAPI:
			if cfg.API == nil {
				return errSourceTypeMismatch
			}
		default:
			return errInvalidSourceType
		}
	}

	if cfg.Redis != nil && cfg.Redis.Address == "" {
		return errMissingRedisAddr
	}
	if cfg.API != nil && cfg.API.URL == "" {
		return errMissingAPIURL
	}

	return nil
}
