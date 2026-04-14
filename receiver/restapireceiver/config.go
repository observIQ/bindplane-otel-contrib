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

package restapireceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/restapireceiver"

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/configopaque"
	"go.opentelemetry.io/collector/confmap"
)

const (
	// Epoch timestamp format constants for use in TimestampFormat.
	// These send the timestamp as a numeric epoch value instead of a formatted string.
	epochSeconds           = "epoch_s"
	epochMilliseconds      = "epoch_ms"
	epochMicroseconds      = "epoch_us"
	epochNanoseconds       = "epoch_ns"
	epochSecondsFractional = "epoch_s_frac"
)

// AuthMode defines the authentication mode for the REST API receiver.
type AuthMode string

const (
	authModeNone           AuthMode = "none"
	authModeAPIKey         AuthMode = "apikey"
	authModeBearer         AuthMode = "bearer"
	authModeBasic          AuthMode = "basic"
	authModeOAuth2         AuthMode = "oauth2"
	authModeAkamaiEdgeGrid AuthMode = "akamai_edgegrid"
)

// UnmarshalText implements the encoding.TextUnmarshaler interface
func (m *AuthMode) UnmarshalText(text []byte) error {
	mode := AuthMode(text)
	switch mode {
	case authModeNone, authModeAPIKey, authModeBearer, authModeBasic, authModeOAuth2, authModeAkamaiEdgeGrid:
		*m = mode
		return nil
	default:
		return fmt.Errorf("invalid auth mode: %s, must be one of: none, apikey, bearer, basic, oauth2, akamai_edgegrid", text)
	}
}

// ResponseFormat defines the response format for the REST API receiver.
type ResponseFormat string

const (
	responseFormatJSON   ResponseFormat = "json"
	responseFormatNDJSON ResponseFormat = "ndjson"
)

// UnmarshalText implements the encoding.TextUnmarshaler interface
func (f *ResponseFormat) UnmarshalText(text []byte) error {
	format := ResponseFormat(text)
	switch format {
	case responseFormatJSON, responseFormatNDJSON:
		*f = format
		return nil
	default:
		return fmt.Errorf("invalid response_format: %s, must be one of: json, ndjson", text)
	}
}

// ResponseSource defines where pagination response attributes are extracted from.
type ResponseSource string

const (
	responseSourceBody   ResponseSource = "body"
	responseSourceHeader ResponseSource = "header"
)

// UnmarshalText implements the encoding.TextUnmarshaler interface
func (s *ResponseSource) UnmarshalText(text []byte) error {
	src := ResponseSource(text)
	switch src {
	case responseSourceBody, responseSourceHeader:
		*s = src
		return nil
	default:
		return fmt.Errorf("invalid response_source: %s, must be one of: body, header", text)
	}
}

// PaginationMode defines the pagination mode for the REST API receiver.
type PaginationMode string

const (
	paginationModeNone        PaginationMode = "none"
	paginationModeOffsetLimit PaginationMode = "offset_limit"
	paginationModePageSize    PaginationMode = "page_size"
	paginationModeTimestamp   PaginationMode = "timestamp"
)

// UnmarshalText implements the encoding.TextUnmarshaler interface
func (m *PaginationMode) UnmarshalText(text []byte) error {
	mode := PaginationMode(text)
	switch mode {
	case paginationModeNone, paginationModeOffsetLimit, paginationModePageSize, paginationModeTimestamp:
		*m = mode
		return nil
	default:
		return fmt.Errorf("invalid pagination mode: %s, must be one of: none, offset_limit, page_size, timestamp", text)
	}
}

// Config defines configuration for the REST API receiver.
type Config struct {
	// URL is the base URL for the REST API endpoint (required).
	URL string `mapstructure:"url"`

	// ResponseFormat defines the format of the API response body.
	// "json" (default): standard JSON array or object with a data field.
	// "ndjson": newline-delimited JSON where each line is a separate JSON object.
	//   In NDJSON mode, the last line is treated as metadata (e.g., containing pagination cursors)
	//   and is not included in the data output.
	ResponseFormat ResponseFormat `mapstructure:"response_format"`

	// ResponseField is the name of the field in the response that contains the array of items.
	// If empty, the response is assumed to be a top-level array.
	// Not used when response_format is "ndjson".
	ResponseField string `mapstructure:"response_field"`

	// Auth defines authentication configuration.
	AuthMode             AuthMode             `mapstructure:"auth_mode"`
	APIKeyConfig         APIKeyConfig         `mapstructure:"apikey"`
	BearerConfig         BearerConfig         `mapstructure:"bearer"`
	BasicConfig          BasicConfig          `mapstructure:"basic"`
	OAuth2Config         OAuth2Config         `mapstructure:"oauth2"`
	AkamaiEdgeGridConfig AkamaiEdgeGridConfig `mapstructure:"akamai_edgegrid"`

	// Pagination defines pagination configuration.
	Pagination PaginationConfig `mapstructure:"pagination"`

	// StartTimeParamName is the query parameter name for the start time (e.g., "since", "from", "start_time").
	// When used with timestamp pagination, this parameter's value advances through response data.
	// With any other pagination mode, it is sent as a static parameter on every request.
	StartTimeParamName string `mapstructure:"start_time_param_name"`

	// StartTimeValue is the initial value for the start time parameter.
	// Accepts a timestamp in the configured timestamp_format or RFC3339 (e.g., "2025-01-01T00:00:00Z").
	// For epoch formats, accepts a numeric string (e.g., "1704067200").
	// Use "now" to send the current time.
	StartTimeValue string `mapstructure:"start_time_value"`

	// EndTimeParamName is the query parameter name for the end time (e.g., "until", "to", "end_time").
	// If set, the end time value is sent on every request regardless of pagination mode.
	EndTimeParamName string `mapstructure:"end_time_param_name"`

	// EndTimeValue configures what value to send for the end time parameter.
	// Supported values:
	//   - "now" (default): the current time at each request
	//   - A fixed timestamp string in the configured timestamp_format or RFC3339
	//   - For epoch formats, a numeric string (e.g., "1704067200")
	EndTimeValue string `mapstructure:"end_time_value"`

	// TimestampFormat is the format for the start/end time query parameters.
	// Common formats:
	//   - "2006-01-02T15:04:05Z07:00" (RFC3339, default)
	//   - "20060102150405" (YYYYMMDDHHMMSS)
	//   - "2006-01-02 15:04:05"
	//   - "epoch_s" (Unix epoch seconds)
	//   - "epoch_ms" (Unix epoch milliseconds)
	//   - "epoch_us" (Unix epoch microseconds)
	//   - "epoch_ns" (Unix epoch nanoseconds)
	//   - "epoch_s_frac" (Unix epoch fractional seconds)
	// If not set, defaults to RFC3339.
	TimestampFormat string `mapstructure:"timestamp_format"`

	// MinPollInterval is the minimum interval between API polls.
	// The receiver uses adaptive polling that resets to this interval when data
	// is received, and backs off when no data is returned.
	MinPollInterval time.Duration `mapstructure:"min_poll_interval"`

	// MaxPollInterval is the maximum interval between API polls.
	// The receiver uses adaptive polling that starts with a short interval and
	// backs off when no data is returned, up to this maximum.
	MaxPollInterval time.Duration `mapstructure:"max_poll_interval"`

	// BackoffMultiplier is the multiplier for increasing the poll interval
	// when no data or a partial page is returned. For example, with a multiplier
	// of 2.0 and a current interval of 10s, the next interval will be 20s.
	// Must be greater than 1.0. Defaults to 2.0.
	BackoffMultiplier float64 `mapstructure:"backoff_multiplier"`

	// Headers is an optional map of headers to send with each request.
	// These headers are applied after authentication headers, so they can
	// override default headers like Accept if needed.
	// Header values will appear in debug logs.
	Headers map[string]string `mapstructure:"headers"`

	// SensitiveHeaders is an optional map of headers containing sensitive values
	// (e.g., auth tokens, API keys). Values are masked in logs and debug output.
	// These headers are applied after authentication and regular headers,
	// so they can override any previously set values.
	SensitiveHeaders map[string]configopaque.String `mapstructure:"sensitive_headers"`

	// deprecationWarnings collects warnings about deprecated config fields
	// that were automatically migrated. Logged at receiver start time.
	deprecationWarnings []string

	// ClientConfig defines HTTP client configuration.
	ClientConfig confighttp.ClientConfig `mapstructure:",squash"`

	// StorageID is the optional storage extension ID for checkpointing.
	StorageID *component.ID `mapstructure:"storage"`

	// Metrics defines configuration for metrics extraction.
	Metrics MetricsConfig `mapstructure:"metrics"`
}

// MetricsConfig defines configuration for extracting metrics from API responses.
type MetricsConfig struct {
	// NameField is the name of the field in each response item that contains the metric name.
	// If not specified or not found, defaults to "restapi.metric".
	NameField string `mapstructure:"name_field"`

	// DescriptionField is the name of the field in each response item that contains the metric description.
	// If not specified or not found, defaults to "Metric from REST API".
	DescriptionField string `mapstructure:"description_field"`

	// TypeField is the name of the field in each response item that contains the metric type.
	// Valid types: "gauge", "sum", "histogram", "summary".
	// If not specified or not found, defaults to "gauge".
	TypeField string `mapstructure:"type_field"`

	// UnitField is the name of the field in each response item that contains the metric unit.
	// If not specified or not found, no unit will be set.
	UnitField string `mapstructure:"unit_field"`

	// MonotonicField is the name of the field in each response item that indicates if a sum metric is monotonic.
	// Only applies to sum metrics. Should contain a boolean value.
	// If not specified or not found, defaults to false for safety.
	MonotonicField string `mapstructure:"monotonic_field"`

	// AggregationTemporalityField is the name of the field in each response item that contains the aggregation temporality.
	// Valid values: "cumulative", "delta".
	// Only applies to sum and histogram metrics.
	// If not specified or not found, defaults to "cumulative".
	AggregationTemporalityField string `mapstructure:"aggregation_temporality_field"`
}

// APIKeyConfig defines API key authentication configuration.
type APIKeyConfig struct {
	HeaderName string              `mapstructure:"header_name"`
	Value      configopaque.String `mapstructure:"value"`
}

// BearerConfig defines bearer token authentication configuration.
type BearerConfig struct {
	Token configopaque.String `mapstructure:"token"`
}

// BasicConfig defines basic authentication configuration.
type BasicConfig struct {
	Username string              `mapstructure:"username"`
	Password configopaque.String `mapstructure:"password"`
}

// OAuth2Config defines OAuth2 client credentials authentication configuration.
type OAuth2Config struct {
	ClientID       string              `mapstructure:"client_id"`
	ClientSecret   configopaque.String `mapstructure:"client_secret"`
	TokenURL       string              `mapstructure:"token_url"`
	Scopes         []string            `mapstructure:"scopes"`
	EndpointParams map[string]string   `mapstructure:"endpoint_params"`
}

// AkamaiEdgeGridConfig defines Akamai EdgeGrid authentication configuration.
type AkamaiEdgeGridConfig struct {
	AccessToken  configopaque.String `mapstructure:"access_token"`
	ClientToken  configopaque.String `mapstructure:"client_token"`
	ClientSecret configopaque.String `mapstructure:"client_secret"`
	// AccountKey is an optional accountSwitchKey used by partners to make requests
	// against a managed account. When set, it is added as the accountSwitchKey
	// query parameter on every outgoing request.
	AccountKey string `mapstructure:"account_key"`
}

// PaginationConfig defines pagination configuration.
type PaginationConfig struct {
	// Mode is the pagination mode: "none", "offset_limit", or "page_size".
	Mode PaginationMode `mapstructure:"mode"`

	// ResponseSource controls where pagination response attributes are extracted from.
	// "body" (default): extract from the response body (or NDJSON metadata line).
	// "header": extract from HTTP response headers.
	// Affects all response-based pagination fields: next_offset_field_name,
	// total_record_count_field, and total_pages_field_name.
	ResponseSource ResponseSource `mapstructure:"response_source"`

	// OffsetLimit defines offset/limit pagination.
	OffsetLimit OffsetLimitPagination `mapstructure:"offset_limit"`

	// PageSize defines page/size pagination.
	PageSize PageSizePagination `mapstructure:"page_size"`

	// Timestamp defines timestamp-based pagination.
	Timestamp TimestampPagination `mapstructure:"timestamp"`

	// TotalRecordCountField is the name of the field or header that contains the total record count.
	TotalRecordCountField string `mapstructure:"total_record_count_field"`

	// PageLimit is the maximum number of pages to fetch (0 = no limit).
	PageLimit int `mapstructure:"page_limit"`

	// ZeroBasedIndex indicates whether pagination starts at index 0 (true) or 1 (false).
	ZeroBasedIndex bool `mapstructure:"zero_based_index"`
}

// OffsetLimitPagination defines offset/limit pagination configuration.
type OffsetLimitPagination struct {
	// OffsetFieldName is the name of the query parameter for offset.
	OffsetFieldName string `mapstructure:"offset_field_name"`

	// StartingOffset is the starting offset value.
	StartingOffset int `mapstructure:"starting_offset"`

	// LimitFieldName is the name of the query parameter for limit.
	LimitFieldName string `mapstructure:"limit_field_name"`

	// NextOffsetFieldName is the name of the field or header that contains the next offset token.
	// When set, the receiver uses token-based (cursor) pagination instead of numeric offsets.
	// Where the value is extracted from depends on pagination.response_source.
	NextOffsetFieldName string `mapstructure:"next_offset_field_name"`
}

// PageSizePagination defines page/size pagination configuration.
type PageSizePagination struct {
	// PageNumFieldName is the name of the query parameter for page number.
	PageNumFieldName string `mapstructure:"page_num_field_name"`

	// StartingPage is the starting page number.
	StartingPage int `mapstructure:"starting_page"`

	// PageSizeFieldName is the name of the query parameter for page size.
	PageSizeFieldName string `mapstructure:"page_size_field_name"`

	// TotalPagesFieldName is the name of the field in the response that contains the total page count.
	TotalPagesFieldName string `mapstructure:"total_pages_field_name"`
}

// TimestampPagination defines timestamp-based pagination configuration.
// The start/end time parameter names, values, and format are configured at the
// top level of the receiver config (start_time_param_name, end_time_param_name, etc.).
// Timestamp pagination advances the start time through response data.
type TimestampPagination struct {
	// TimestampFieldName is the name of the field in each response item that contains the timestamp value.
	// This is used to extract the timestamp from the last item for the next page.
	TimestampFieldName string `mapstructure:"timestamp_field_name"`

	// PageSizeFieldName is the name of the query parameter for page size (e.g., "perPage", "limit").
	PageSizeFieldName string `mapstructure:"page_size_field_name"`

	// PageSize is the page size to use.
	PageSize int `mapstructure:"page_size"`
}

// deprecatedTimestampKeys maps old pagination.timestamp.* keys to their new top-level equivalents.
var deprecatedTimestampKeys = map[string]string{
	"pagination::timestamp::param_name":        "start_time_param_name",
	"pagination::timestamp::initial_timestamp": "start_time_value",
	"pagination::timestamp::timestamp_format":  "timestamp_format",
}

// Unmarshal implements confmap.Unmarshaler to migrate deprecated pagination.timestamp fields
// to their new top-level equivalents.
func (c *Config) Unmarshal(conf *confmap.Conf) error {
	if conf == nil {
		return nil
	}

	// Only run migration once — the confmap decoder may invoke Unmarshal
	// multiple times (once from the hook, once from conf.Unmarshal below).
	// On the second call the merged keys from the first pass would cause
	// false "both set" warnings, so skip migration if already done.
	if c.deprecationWarnings == nil {
		var warnings []string
		for oldKey, newKey := range deprecatedTimestampKeys {
			if !conf.IsSet(oldKey) {
				continue
			}
			if conf.IsSet(newKey) {
				warnings = append(warnings,
					fmt.Sprintf("both deprecated %q and new %q are set; using %q",
						oldKeyDisplay(oldKey), newKey, newKey))
				continue
			}
			val := conf.Get(oldKey)
			merged := confmap.NewFromStringMap(map[string]any{newKey: val})
			if err := conf.Merge(merged); err != nil {
				return fmt.Errorf("failed to migrate deprecated key %q: %w", oldKeyDisplay(oldKey), err)
			}
			warnings = append(warnings,
				fmt.Sprintf("%q is deprecated; use %q instead", oldKeyDisplay(oldKey), newKey))
		}
		// Use empty (non-nil) slice to mark migration as done even when
		// there are no warnings, so subsequent calls skip the block.
		if warnings == nil {
			warnings = []string{}
		}
		c.deprecationWarnings = warnings
	}

	// Perform the default unmarshal.
	return conf.Unmarshal(c, confmap.WithIgnoreUnused())
}

// oldKeyDisplay converts the internal "::" delimited key to the user-facing "." delimited form.
func oldKeyDisplay(key string) string {
	return strings.ReplaceAll(key, "::", ".")
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.URL == "" {
		return fmt.Errorf("url is required")
	}

	// Apply default response format
	if c.ResponseFormat == "" {
		c.ResponseFormat = responseFormatJSON
	}

	// Validate response format
	switch c.ResponseFormat {
	case responseFormatJSON, responseFormatNDJSON:
		// Valid formats
	default:
		return fmt.Errorf("invalid response_format: %s, must be one of: json, ndjson", c.ResponseFormat)
	}

	// Validate auth
	if c.AuthMode == "" {
		return fmt.Errorf("auth is required")
	}

	// Validate auth mode
	switch c.AuthMode {
	case authModeNone, authModeAPIKey, authModeBearer, authModeBasic, authModeOAuth2, authModeAkamaiEdgeGrid:
		// Valid modes
	default:
		return fmt.Errorf("invalid auth mode: %s, must be one of: none, apikey, bearer, basic, oauth2, akamai_edgegrid", c.AuthMode)
	}

	// Validate auth mode specific requirements
	switch c.AuthMode {
	case authModeAPIKey:
		if c.APIKeyConfig.HeaderName == "" {
			return fmt.Errorf("header_name is required when auth_mode is apikey")
		}
		if string(c.APIKeyConfig.Value) == "" {
			return fmt.Errorf("value is required when auth_mode is apikey")
		}
	case authModeBearer:
		if string(c.BearerConfig.Token) == "" {
			return fmt.Errorf("token is required when auth_mode is bearer")
		}
	case authModeBasic:
		if c.BasicConfig.Username == "" {
			return fmt.Errorf("username is required when auth_mode is basic")
		}
		if string(c.BasicConfig.Password) == "" {
			return fmt.Errorf("password is required when auth_mode is basic")
		}
	case authModeOAuth2:
		if c.OAuth2Config.ClientID == "" {
			return fmt.Errorf("client_id is required when auth_mode is oauth2")
		}
		if string(c.OAuth2Config.ClientSecret) == "" {
			return fmt.Errorf("client_secret is required when auth_mode is oauth2")
		}
		if c.OAuth2Config.TokenURL == "" {
			return fmt.Errorf("token_url is required when auth_mode is oauth2")
		}
	case authModeAkamaiEdgeGrid:
		if string(c.AkamaiEdgeGridConfig.AccessToken) == "" {
			return fmt.Errorf("access_token is required when auth_mode is akamai_edgegrid")
		}
		if string(c.AkamaiEdgeGridConfig.ClientToken) == "" {
			return fmt.Errorf("client_token is required when auth_mode is akamai_edgegrid")
		}
		if string(c.AkamaiEdgeGridConfig.ClientSecret) == "" {
			return fmt.Errorf("client_secret is required when auth_mode is akamai_edgegrid")
		}
	}

	// Validate custom headers
	for name, value := range c.Headers {
		if err := validateHeaderName(name); err != nil {
			return fmt.Errorf("invalid header name %q: %w", name, err)
		}
		if err := validateHeaderValue(value); err != nil {
			return fmt.Errorf("invalid header value for %q: %w", name, err)
		}
	}

	// Validate sensitive headers
	for name, value := range c.SensitiveHeaders {
		if err := validateHeaderName(name); err != nil {
			return fmt.Errorf("invalid sensitive header name %q: %w", name, err)
		}
		if err := validateHeaderValue(string(value)); err != nil {
			return fmt.Errorf("invalid sensitive header value for %q: %w", name, err)
		}
	}

	// Check for case-insensitive duplicate header names within each map
	headersSeen := make(map[string]string, len(c.Headers))
	for name := range c.Headers {
		canonical := http.CanonicalHeaderKey(name)
		if existing, ok := headersSeen[canonical]; ok {
			return fmt.Errorf("header %q and %q are duplicates (HTTP headers are case-insensitive)", existing, name)
		}
		headersSeen[canonical] = name
	}

	sensitiveHeadersSeen := make(map[string]string, len(c.SensitiveHeaders))
	for name := range c.SensitiveHeaders {
		canonical := http.CanonicalHeaderKey(name)
		if existing, ok := sensitiveHeadersSeen[canonical]; ok {
			return fmt.Errorf("sensitive header %q and %q are duplicates (HTTP headers are case-insensitive)", existing, name)
		}
		sensitiveHeadersSeen[canonical] = name
	}

	// Check for duplicate header names across headers and sensitive_headers (case-insensitive)
	for name := range c.SensitiveHeaders {
		canonical := http.CanonicalHeaderKey(name)
		if existing, ok := headersSeen[canonical]; ok {
			return fmt.Errorf("header %q is defined in both headers and sensitive_headers; use one or the other", existing)
		}
	}

	// Validate pagination mode
	switch c.Pagination.Mode {
	case paginationModeNone, paginationModeOffsetLimit, paginationModePageSize, paginationModeTimestamp:
		// Valid modes
	default:
		return fmt.Errorf("invalid pagination mode: %s, must be one of: none, offset_limit, page_size, timestamp", c.Pagination.Mode)
	}

	// Default response_source to body
	if c.Pagination.ResponseSource == "" {
		c.Pagination.ResponseSource = responseSourceBody
	}

	// Validate response_source
	switch c.Pagination.ResponseSource {
	case responseSourceBody, responseSourceHeader:
		// Valid
	default:
		return fmt.Errorf("invalid response_source: %s, must be one of: body, header", c.Pagination.ResponseSource)
	}

	// Validate pagination mode specific requirements
	switch c.Pagination.Mode {
	case paginationModeOffsetLimit:
		if c.Pagination.OffsetLimit.OffsetFieldName == "" {
			return fmt.Errorf("offset_field_name is required when pagination.mode is offset_limit")
		}
		if c.Pagination.OffsetLimit.LimitFieldName == "" {
			return fmt.Errorf("limit_field_name is required when pagination.mode is offset_limit")
		}
		// next_offset_field_name is required when response_source is header
		if c.Pagination.ResponseSource == responseSourceHeader && c.Pagination.OffsetLimit.NextOffsetFieldName == "" {
			return fmt.Errorf("next_offset_field_name is required when response_source is header")
		}
	case paginationModePageSize:
		if c.Pagination.PageSize.PageNumFieldName == "" {
			return fmt.Errorf("page_num_field_name is required when pagination.mode is page_size")
		}
		if c.Pagination.PageSize.PageSizeFieldName == "" {
			return fmt.Errorf("page_size_field_name is required when pagination.mode is page_size")
		}
	case paginationModeTimestamp:
		if c.StartTimeParamName == "" {
			return fmt.Errorf("start_time_param_name is required when pagination.mode is timestamp")
		}
		if c.Pagination.Timestamp.TimestampFieldName == "" {
			return fmt.Errorf("timestamp_field_name is required when pagination.mode is timestamp")
		}
	}

	// Validate start_time_value format if provided
	if err := c.validateTimestampValue(c.StartTimeValue, "start_time_value"); err != nil {
		return err
	}

	// Validate end_time_value format if provided (and not "now")
	if err := c.validateTimestampValue(c.EndTimeValue, "end_time_value"); err != nil {
		return err
	}

	// Validate start_time is before end_time
	if c.StartTimeValue != "" && c.StartTimeValue != "now" && c.EndTimeValue != "" && c.EndTimeValue != "now" {
		startTime, err := c.parseConfigTimestamp(c.StartTimeValue)
		if err == nil {
			endTime, err := c.parseConfigTimestamp(c.EndTimeValue)
			if err == nil && !startTime.Before(endTime) {
				return fmt.Errorf("start_time_value (%s) must be before end_time_value (%s)", c.StartTimeValue, c.EndTimeValue)
			}
		}
	}

	// Apply defaults if not configured (zero value means not set)
	if c.MinPollInterval == 0 {
		c.MinPollInterval = 10 * time.Second
	}

	if c.MinPollInterval < 0 {
		return fmt.Errorf("min_poll_interval must be greater than or equal to 0")
	}

	if c.MaxPollInterval == 0 {
		c.MaxPollInterval = 5 * time.Minute
	}

	if c.MaxPollInterval < 0 {
		return fmt.Errorf("max_poll_interval must be greater than or equal to 0")
	}

	if c.MinPollInterval > c.MaxPollInterval {
		return fmt.Errorf("min_poll_interval (%s) must be less than or equal to max_poll_interval (%s)", c.MinPollInterval, c.MaxPollInterval)
	}

	// Apply default backoff multiplier if not configured
	if c.BackoffMultiplier == 0 {
		c.BackoffMultiplier = 2.0
	}

	if c.BackoffMultiplier <= 1.0 {
		return fmt.Errorf("backoff_multiplier must be greater than 1.0")
	}

	return nil
}

// validateTimestampValue validates that a timestamp value string can be parsed
// using the configured timestamp_format. Returns nil if the value is empty or "now".
func (c *Config) validateTimestampValue(value, fieldName string) error {
	if value == "" || value == "now" {
		return nil
	}

	if _, err := c.parseConfigTimestamp(value); err != nil {
		formatHint := "RFC3339 (e.g., 2025-01-01T00:00:00Z)"
		if isEpochFormat(c.TimestampFormat) {
			return fmt.Errorf("%s %q must be a numeric value when using epoch timestamp_format (%s)", fieldName, value, c.TimestampFormat)
		}
		if c.TimestampFormat != "" {
			formatHint = fmt.Sprintf("configured timestamp_format (%s) or RFC3339", c.TimestampFormat)
		}
		return fmt.Errorf("%s %q could not be parsed; must be \"now\" or match %s", fieldName, value, formatHint)
	}
	return nil
}

// parseConfigTimestamp parses a user-configured timestamp value into a time.Time
// using the same logic as validateTimestampValue.
func (c *Config) parseConfigTimestamp(value string) (time.Time, error) {
	if isEpochFormat(c.TimestampFormat) {
		return parseEpochTimestamp(value, c.TimestampFormat)
	}
	if c.TimestampFormat != "" {
		if t, err := time.Parse(c.TimestampFormat, value); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not parse timestamp %q", value)
}

// validateHeaderName checks that a header name is a valid HTTP token per RFC 7230.
// Header names must be non-empty and contain only visible ASCII characters
// excluding delimiters: A-Z, a-z, 0-9, and !#$%&'*+-.^_`|~
func validateHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("header name must not be empty")
	}
	for i, c := range name {
		if c < 0x21 || c > 0x7E {
			return fmt.Errorf("contains invalid character at position %d", i)
		}
		// RFC 7230 delimiters that are not allowed in tokens
		switch c {
		case '(', ')', ',', '/', ':', ';', '<', '=', '>', '?', '@', '[', '\\', ']', '{', '}', '"':
			return fmt.Errorf("contains invalid character %q at position %d", string(c), i)
		}
	}
	return nil
}

// validateHeaderValue checks that a header value does not contain characters
// that could enable CRLF injection or other HTTP header manipulation.
// Values must not contain \r, \n, or \x00 (null byte).
func validateHeaderValue(value string) error {
	for i, c := range value {
		switch c {
		case '\r':
			return fmt.Errorf("contains carriage return (\\r) at position %d", i)
		case '\n':
			return fmt.Errorf("contains newline (\\n) at position %d", i)
		case 0x00:
			return fmt.Errorf("contains null byte at position %d", i)
		}
	}
	return nil
}

// isEpochFormat returns true if the given format string is one of the epoch timestamp formats.
func isEpochFormat(format string) bool {
	switch format {
	case epochSeconds, epochMilliseconds, epochMicroseconds, epochNanoseconds, epochSecondsFractional:
		return true
	}
	return false
}

// formatTimestampEpoch formats a time.Time as an epoch numeric string.
func formatTimestampEpoch(t time.Time, format string) string {
	switch format {
	case epochSeconds:
		return strconv.FormatInt(t.Unix(), 10)
	case epochMilliseconds:
		return strconv.FormatInt(t.UnixMilli(), 10)
	case epochMicroseconds:
		return strconv.FormatInt(t.UnixMicro(), 10)
	case epochNanoseconds:
		return strconv.FormatInt(t.UnixNano(), 10)
	case epochSecondsFractional:
		nsec := int64(t.Nanosecond())
		if nsec == 0 {
			return strconv.FormatInt(t.Unix(), 10)
		}
		frac := strings.TrimRight(fmt.Sprintf("%09d", nsec), "0")
		return fmt.Sprintf("%d.%s", t.Unix(), frac)
	default:
		return strconv.FormatInt(t.Unix(), 10)
	}
}

// parseEpochTimestamp parses a numeric epoch string into a time.Time based on the epoch format.
// For epoch_s, epoch_ms, epoch_us, epoch_ns: the value must be an integer in the configured unit.
// For epoch_s_frac: the value is fractional seconds (e.g., "1704067200.123456") where the integer
// part is seconds and the fractional digits are sub-second precision.
func parseEpochTimestamp(value string, format string) (time.Time, error) {
	// epoch_s_frac: fractional seconds (e.g., "1704067200.123456")
	if format == epochSecondsFractional {
		parts := strings.SplitN(value, ".", 2)
		sec, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("failed to parse epoch timestamp %q: %w", value, err)
		}
		var nsec int64
		if len(parts) == 2 && len(parts[1]) > 0 {
			fracStr := parts[1]
			if len(fracStr) < 9 {
				fracStr += strings.Repeat("0", 9-len(fracStr))
			} else if len(fracStr) > 9 {
				fracStr = fracStr[:9]
			}
			nsec, err = strconv.ParseInt(fracStr, 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("failed to parse fractional epoch timestamp %q: %w", value, err)
			}
		}
		return time.Unix(sec, nsec), nil
	}

	// All other epoch formats: integer value in the configured unit
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse epoch timestamp %q: %w", value, err)
	}
	switch format {
	case epochSeconds:
		return time.Unix(n, 0), nil
	case epochMilliseconds:
		return time.Unix(0, n*int64(time.Millisecond)), nil
	case epochMicroseconds:
		return time.Unix(0, n*int64(time.Microsecond)), nil
	case epochNanoseconds:
		return time.Unix(0, n), nil
	default:
		return time.Unix(n, 0), nil
	}
}
