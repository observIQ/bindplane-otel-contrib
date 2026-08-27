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

package restapireceiver

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configopaque"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/confmaptest"

	"github.com/observiq/bindplane-otel-contrib/receiver/restapireceiver/internal/metadata"
)

func TestConfig_Validate(t *testing.T) {
	testCases := []struct {
		name        string
		config      *Config
		expectedErr string
	}{
		{
			name: "valid config with apikey auth",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "missing URL",
			config: &Config{
				URL:      "",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "url is required",
		},
		{
			name: "valid config with no auth",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "invalid auth mode",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: AuthMode("invalid"),
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid auth mode: invalid, must be one of: none, apikey, bearer, basic, oauth2, akamai_edgegrid",
		},
		{
			name: "apikey auth missing header name",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "header_name is required when auth_mode is apikey",
		},
		{
			name: "apikey auth missing value",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "value is required when auth_mode is apikey",
		},
		{
			name: "valid apikey auth",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "bearer auth missing token",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeBearer,
				BearerConfig: BearerConfig{
					Token: "",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "token is required when auth_mode is bearer",
		},
		{
			name: "valid bearer auth",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeBearer,
				BearerConfig: BearerConfig{
					Token: "test-token",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "valid bearer auth with header_prefix",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeBearer,
				BearerConfig: BearerConfig{
					Token:        "test-token",
					HeaderPrefix: "CwsAuth Bearer=",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "bearer auth header_prefix with CRLF",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeBearer,
				BearerConfig: BearerConfig{
					Token:        "test-token",
					HeaderPrefix: "Bearer \r\nX-Injected: evil",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid bearer header_prefix",
		},
		{
			name: "basic auth missing username",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeBasic,
				BasicConfig: BasicConfig{
					Username: "",
					Password: "test-password",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "username is required when auth_mode is basic",
		},
		{
			name: "basic auth missing password",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeBasic,
				BasicConfig: BasicConfig{
					Username: "test-user",
					Password: "",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "password is required when auth_mode is basic",
		},
		{
			name: "valid basic auth",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeBasic,
				BasicConfig: BasicConfig{
					Username: "test-user",
					Password: "test-password",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "oauth2 auth missing client_id",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeOAuth2,
				OAuth2Config: OAuth2Config{
					ClientID:     "",
					ClientSecret: "test-client-secret",
					TokenURL:     "https://oauth.example.com/token",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "client_id is required when auth_mode is oauth2",
		},
		{
			name: "oauth2 auth missing client_secret",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeOAuth2,
				OAuth2Config: OAuth2Config{
					ClientID:     "test-client-id",
					ClientSecret: "",
					TokenURL:     "https://oauth.example.com/token",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "client_secret is required when auth_mode is oauth2",
		},
		{
			name: "oauth2 auth missing token_url",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeOAuth2,
				OAuth2Config: OAuth2Config{
					ClientID:     "test-client-id",
					ClientSecret: "test-client-secret",
					TokenURL:     "",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "token_url is required when auth_mode is oauth2",
		},
		{
			name: "valid oauth2 auth",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeOAuth2,
				OAuth2Config: OAuth2Config{
					ClientID:     "test-client-id",
					ClientSecret: "test-client-secret",
					TokenURL:     "https://oauth.example.com/token",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "valid oauth2 auth with scopes",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeOAuth2,
				OAuth2Config: OAuth2Config{
					ClientID:     "test-client-id",
					ClientSecret: "test-client-secret",
					TokenURL:     "https://oauth.example.com/token",
					Scopes:       []string{"read", "write"},
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "valid oauth2 auth with endpoint params",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeOAuth2,
				OAuth2Config: OAuth2Config{
					ClientID:     "test-client-id",
					ClientSecret: "test-client-secret",
					TokenURL:     "https://oauth.example.com/token",
					EndpointParams: map[string]string{
						"audience": "https://api.example.com",
					},
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "valid oauth2 auth with header_prefix",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeOAuth2,
				OAuth2Config: OAuth2Config{
					ClientID:     "test-client-id",
					ClientSecret: "test-client-secret",
					TokenURL:     "https://oauth.example.com/token",
					HeaderPrefix: "CwsAuth Bearer=",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "oauth2 auth header_prefix with newline",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeOAuth2,
				OAuth2Config: OAuth2Config{
					ClientID:     "test-client-id",
					ClientSecret: "test-client-secret",
					TokenURL:     "https://oauth.example.com/token",
					HeaderPrefix: "CwsAuth\nBearer=",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid oauth2 header_prefix",
		},
		{
			name: "akamai edgegrid auth missing access token",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAkamaiEdgeGrid,
				AkamaiEdgeGridConfig: AkamaiEdgeGridConfig{
					AccessToken:  "",
					ClientToken:  "test-client-token",
					ClientSecret: "test-client-secret",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "access_token is required when auth_mode is akamai_edgegrid",
		},
		{
			name: "akamai edgegrid auth missing client token",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAkamaiEdgeGrid,
				AkamaiEdgeGridConfig: AkamaiEdgeGridConfig{
					AccessToken:  "test-access-token",
					ClientSecret: "test-client-secret",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "client_token is required when auth_mode is akamai_edgegrid",
		},
		{
			name: "akamai edgegrid auth missing client secret",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAkamaiEdgeGrid,
				AkamaiEdgeGridConfig: AkamaiEdgeGridConfig{
					AccessToken: "test-access-token",
					ClientToken: "test-client-token",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "client_secret is required when auth_mode is akamai_edgegrid",
		},
		{
			name: "valid akamai edgegrid auth",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAkamaiEdgeGrid,
				AkamaiEdgeGridConfig: AkamaiEdgeGridConfig{
					AccessToken:  "test-access-token",
					ClientToken:  "test-client-token",
					ClientSecret: "test-client-secret",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "valid ndjson response format",
			config: &Config{
				URL:            "https://api.example.com/data",
				ResponseFormat: responseFormatNDJSON,
				AuthMode:       authModeNone,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "invalid response format",
			config: &Config{
				URL:            "https://api.example.com/data",
				ResponseFormat: ResponseFormat("xml"),
				AuthMode:       authModeNone,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid response_format: xml, must be one of: json, ndjson",
		},
		{
			name: "invalid pagination mode",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: PaginationMode("invalid"),
				},
			},
			expectedErr: "invalid pagination mode: invalid, must be one of: none, offset_limit, page_size, timestamp",
		},
		{
			name: "offset_limit pagination missing offset field name",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "",
						LimitFieldName:  "limit",
					},
				},
			},
			expectedErr: "offset_field_name is required when pagination.mode is offset_limit",
		},
		{
			name: "offset_limit pagination missing limit field name",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "offset",
						LimitFieldName:  "",
					},
				},
			},
			expectedErr: "limit_field_name is required when pagination.mode is offset_limit and next_offset_field_name is not set for token-based pagination",
		},
		{
			name: "valid offset_limit with token-based pagination omits limit_field_name",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName:     "cursor",
						NextOffsetFieldName: "next_cursor",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid offset_limit pagination",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "offset",
						LimitFieldName:  "limit",
						StartingOffset:  0,
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid offset_limit with header response_source",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Pagination: PaginationConfig{
					Mode:           paginationModeOffsetLimit,
					ResponseSource: responseSourceHeader,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName:     "offset",
						LimitFieldName:      "limit",
						NextOffsetFieldName: "X-Next-Offset",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "header response_source requires next_offset_field_name for offset_limit",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Pagination: PaginationConfig{
					Mode:           paginationModeOffsetLimit,
					ResponseSource: responseSourceHeader,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "offset",
						LimitFieldName:  "limit",
					},
				},
			},
			expectedErr: "next_offset_field_name is required when response_source is header",
		},
		{
			name: "valid page_size with header response_source",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Pagination: PaginationConfig{
					Mode:           paginationModePageSize,
					ResponseSource: responseSourceHeader,
					PageSize: PageSizePagination{
						PageNumFieldName:    "page",
						PageSizeFieldName:   "per_page",
						TotalPagesFieldName: "X-Total-Pages",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "page_size pagination missing page num field name",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModePageSize,
					PageSize: PageSizePagination{
						PageNumFieldName:  "",
						PageSizeFieldName: "page_size",
					},
				},
			},
			expectedErr: "page_num_field_name is required when pagination.mode is page_size",
		},
		{
			name: "page_size pagination missing page size field name",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModePageSize,
					PageSize: PageSizePagination{
						PageNumFieldName:  "page",
						PageSizeFieldName: "",
					},
				},
			},
			expectedErr: "page_size_field_name is required when pagination.mode is page_size",
		},
		{
			name: "valid page_size pagination",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModePageSize,
					PageSize: PageSizePagination{
						PageNumFieldName:  "page",
						PageSizeFieldName: "page_size",
						StartingPage:      1,
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "timestamp pagination missing param name",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "ts",
						PageSizeFieldName:  "perPage",
						PageSize:           100,
					},
				},
			},
			expectedErr: "start_time_param_name is required when pagination.mode is timestamp",
		},
		{
			name: "timestamp pagination missing timestamp field name",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "t0",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "",
						PageSizeFieldName:  "perPage",
						PageSize:           100,
					},
				},
			},
			expectedErr: "timestamp_field_name is required when pagination.mode is timestamp",
		},
		{
			name: "valid timestamp pagination",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "t0",
				StartTimeValue:     time.Now().Format(time.RFC3339),
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "ts",
						PageSizeFieldName:  "perPage",
						PageSize:           200,
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid timestamp pagination with custom format",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "min-date",
				TimestampFormat:    "2006-01-02 15:04:05",
				StartTimeValue:     "2025-01-01 00:00:00",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "invalid initial_timestamp format",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "min-date",
				TimestampFormat:    "2006-01-02 15:04:05",
				StartTimeValue:     "invalid-timestamp",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: `start_time_value "invalid-timestamp" could not be parsed`,
		},
		{
			name: "valid timestamp pagination with epoch_s format",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "since",
				TimestampFormat:    "epoch_s",
				StartTimeValue:     "1704067200",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid timestamp pagination with epoch_ms format",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "since",
				TimestampFormat:    "epoch_ms",
				StartTimeValue:     "1704067200000",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid timestamp pagination with epoch_s_frac format (milliseconds)",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "since",
				TimestampFormat:    "epoch_s_frac",
				StartTimeValue:     "1704067200.123",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid timestamp pagination with epoch_s_frac format (microseconds)",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "since",
				TimestampFormat:    "epoch_s_frac",
				StartTimeValue:     "1704067200.123456",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid timestamp pagination with epoch_s_frac format (whole seconds)",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "since",
				TimestampFormat:    "epoch_s_frac",
				StartTimeValue:     "1704067200",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "invalid epoch initial_timestamp (not numeric)",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "since",
				TimestampFormat:    "epoch_s",
				StartTimeValue:     "not-a-number",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "start_time_value \"not-a-number\" must be a numeric value when using epoch timestamp_format (epoch_s)",
		},
		{
			name: "negative min_poll_interval",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
				MinPollInterval: -1 * time.Second,
			},
			expectedErr: "min_poll_interval must be greater than or equal to 0",
		},
		{
			name: "negative max_poll_interval",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
				MaxPollInterval: -1 * time.Minute,
			},
			expectedErr: "max_poll_interval must be greater than or equal to 0",
		},
		{
			name: "min_poll_interval greater than max_poll_interval",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
				MinPollInterval: 10 * time.Minute,
				MaxPollInterval: 5 * time.Minute,
			},
			expectedErr: "min_poll_interval (10m0s) must be less than or equal to max_poll_interval (5m0s)",
		},
		{
			name: "valid min_poll_interval",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
				MinPollInterval: 30 * time.Second,
				MaxPollInterval: 5 * time.Minute,
			},
			expectedErr: "",
		},
		{
			name: "valid custom headers",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"X-Custom-Header": "some-value",
					"X-Tenant-ID":     "tenant-123",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "header value with CRLF injection",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"X-Custom": "value\r\nInjected-Header: evil",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid header value for \"X-Custom\": contains carriage return",
		},
		{
			name: "header value with newline",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"X-Custom": "value\nevil",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid header value for \"X-Custom\": contains newline",
		},
		{
			name: "header value with null byte",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"X-Custom": "value\x00evil",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid header value for \"X-Custom\": contains null byte",
		},
		{
			name: "header name with space",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"Bad Header": "value",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid header name \"Bad Header\": contains invalid character",
		},
		{
			name: "empty header name",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"": "value",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid header name \"\": header name must not be empty",
		},
		{
			name: "header name with colon",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"X-Bad:Header": "value",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid header name \"X-Bad:Header\": contains invalid character",
		},
		{
			name: "valid sensitive headers",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				SensitiveHeaders: map[string]configopaque.String{
					"X-Auth-Token": "secret-token-value",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "sensitive header name with space",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				SensitiveHeaders: map[string]configopaque.String{
					"Bad Header": "value",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid sensitive header name \"Bad Header\": contains invalid character",
		},
		{
			name: "sensitive header value with newline",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				SensitiveHeaders: map[string]configopaque.String{
					"X-Auth": "value\nevil",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid sensitive header value for \"X-Auth\": contains newline",
		},
		{
			name: "duplicate header in headers and sensitive_headers",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"X-Custom": "plain-value",
				},
				SensitiveHeaders: map[string]configopaque.String{
					"X-Custom": "secret-value",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "header \"X-Custom\" is defined in both headers and sensitive_headers",
		},
		{
			name: "duplicate header in headers and sensitive_headers case-insensitive",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"x-custom": "plain-value",
				},
				SensitiveHeaders: map[string]configopaque.String{
					"X-Custom": "secret-value",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "header \"x-custom\" is defined in both headers and sensitive_headers",
		},
		{
			name: "duplicate headers within headers map case-insensitive",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"X-Foo": "a",
					"x-foo": "b",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "are duplicates (HTTP headers are case-insensitive)",
		},
		{
			name: "duplicate headers within sensitive_headers map case-insensitive",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				SensitiveHeaders: map[string]configopaque.String{
					"X-Secret": "a",
					"x-secret": "b",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "are duplicates (HTTP headers are case-insensitive)",
		},
		{
			name: "valid mixed headers and sensitive_headers",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Headers: map[string]string{
					"X-Tenant-ID": "tenant-123",
				},
				SensitiveHeaders: map[string]configopaque.String{
					"X-Auth-Token": "secret-token",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "valid end_timestamp_value now",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "start_time",
				EndTimeParamName:   "end_time",
				EndTimeValue:       "now",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid end_timestamp_value RFC3339",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "start_time",
				EndTimeParamName:   "end_time",
				EndTimeValue:       "2025-06-01T00:00:00Z",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid end_timestamp_value epoch",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "since",
				TimestampFormat:    "epoch_s",
				EndTimeParamName:   "until",
				EndTimeValue:       "1748736000",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "invalid end_timestamp_value string format",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "start_time",
				EndTimeParamName:   "end_time",
				EndTimeValue:       "not-a-timestamp",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: `end_time_value "not-a-timestamp" could not be parsed`,
		},
		{
			name: "invalid end_timestamp_value epoch format",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeAPIKey,
				StartTimeParamName: "since",
				TimestampFormat:    "epoch_s",
				EndTimeParamName:   "until",
				EndTimeValue:       "not-a-number",
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeTimestamp,
					Timestamp: TimestampPagination{
						TimestampFieldName: "timestamp",
					},
				},
			},
			expectedErr: `end_time_value "not-a-number" must be a numeric value when using epoch timestamp_format`,
		},
		{
			name: "valid time-bound with offset_limit pagination",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeNone,
				StartTimeParamName: "from",
				StartTimeValue:     "2025-01-01T00:00:00Z",
				EndTimeParamName:   "to",
				EndTimeValue:       "2025-06-01T00:00:00Z",
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "offset",
						LimitFieldName:  "limit",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "valid time-bound with no pagination",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeNone,
				StartTimeParamName: "since",
				StartTimeValue:     "now",
				EndTimeParamName:   "until",
				EndTimeValue:       "now",
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "invalid start_time_value with no pagination",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeNone,
				StartTimeParamName: "from",
				StartTimeValue:     "bad-value",
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: `start_time_value "bad-value" could not be parsed`,
		},
		{
			name: "invalid end_time_value with offset_limit pagination",
			config: &Config{
				URL:              "https://api.example.com/data",
				AuthMode:         authModeNone,
				EndTimeParamName: "to",
				EndTimeValue:     "bad-value",
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "offset",
						LimitFieldName:  "limit",
					},
				},
			},
			expectedErr: `end_time_value "bad-value" could not be parsed`,
		},
		{
			name: "valid time-bound epoch with page_size pagination",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeNone,
				StartTimeParamName: "from",
				StartTimeValue:     "1704067200",
				EndTimeParamName:   "to",
				EndTimeValue:       "1748736000",
				TimestampFormat:    "epoch_s",
				Pagination: PaginationConfig{
					Mode: paginationModePageSize,
					PageSize: PageSizePagination{
						PageNumFieldName:  "page",
						PageSizeFieldName: "size",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "end_time_value before start_time_value",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeNone,
				StartTimeParamName: "from",
				StartTimeValue:     "2025-06-01T00:00:00Z",
				EndTimeParamName:   "to",
				EndTimeValue:       "2025-01-01T00:00:00Z",
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "start_time_value (2025-06-01T00:00:00Z) must be before end_time_value (2025-01-01T00:00:00Z)",
		},
		{
			name: "end_time_value before start_time_value epoch",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeNone,
				StartTimeParamName: "from",
				StartTimeValue:     "1748736000",
				EndTimeParamName:   "to",
				EndTimeValue:       "1704067200",
				TimestampFormat:    "epoch_s",
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "start_time_value (1748736000) must be before end_time_value (1704067200)",
		},
		{
			name: "valid post config with request body",
			config: &Config{
				URL:      "https://api.example.com/alerts",
				Method:   methodPOST,
				AuthMode: authModeNone,
				RequestBody: map[string]any{
					"filter": "status:'new'",
					"sort":   "created_timestamp|asc",
				},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "valid post config with param_location query",
			config: &Config{
				URL:           "https://api.example.com/alerts",
				Method:        methodPOST,
				ParamLocation: paramLocationQuery,
				AuthMode:      authModeNone,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
		{
			name: "invalid method",
			config: &Config{
				URL:      "https://api.example.com/data",
				Method:   Method("put"),
				AuthMode: authModeNone,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid method: put, must be one of: get, post",
		},
		{
			name: "invalid param_location",
			config: &Config{
				URL:           "https://api.example.com/data",
				ParamLocation: ParamLocation("header"),
				AuthMode:      authModeNone,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "invalid param_location: header, must be one of: query, body",
		},
		{
			name: "param_location body rejected with get",
			config: &Config{
				URL:           "https://api.example.com/data",
				Method:        methodGET,
				ParamLocation: paramLocationBody,
				AuthMode:      authModeNone,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "param_location must be query when method is get",
		},
		{
			name: "request_body rejected with get",
			config: &Config{
				URL:         "https://api.example.com/data",
				Method:      methodGET,
				AuthMode:    authModeNone,
				RequestBody: map[string]any{"filter": "x"},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "request_body is not supported when method is get",
		},
		{
			name: "request_body that cannot be encoded as JSON",
			config: &Config{
				URL:         "https://api.example.com/data",
				Method:      methodPOST,
				AuthMode:    authModeNone,
				RequestBody: map[string]any{"ch": make(chan int)},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "request_body cannot be encoded as JSON",
		},
		{
			name: "request_body key conflicts with limit_field_name",
			config: &Config{
				URL:         "https://api.example.com/alerts",
				Method:      methodPOST,
				AuthMode:    authModeNone,
				RequestBody: map[string]any{"limit": 100},
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "after",
						LimitFieldName:  "limit",
					},
				},
			},
			expectedErr: `request_body key "limit" conflicts with pagination.offset_limit.limit_field_name; set pagination.offset_limit.limit instead`,
		},
		{
			name: "request_body key conflicts with offset_field_name",
			config: &Config{
				URL:         "https://api.example.com/alerts",
				Method:      methodPOST,
				AuthMode:    authModeNone,
				RequestBody: map[string]any{"after": "pinned"},
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "after",
						LimitFieldName:  "limit",
					},
				},
			},
			expectedErr: `request_body key "after" conflicts with pagination.offset_limit.offset_field_name; the receiver sets this value on each request`,
		},
		{
			name: "request_body key conflicts with start_time_param_name",
			config: &Config{
				URL:                "https://api.example.com/alerts",
				Method:             methodPOST,
				AuthMode:           authModeNone,
				StartTimeParamName: "since",
				StartTimeValue:     "2025-01-01T00:00:00Z",
				RequestBody:        map[string]any{"since": "2020-01-01T00:00:00Z"},
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: `request_body key "since" conflicts with start_time_param_name`,
		},
		{
			name: "request_body key conflict ignored when param_location is query",
			config: &Config{
				URL:           "https://api.example.com/alerts",
				Method:        methodPOST,
				ParamLocation: paramLocationQuery,
				AuthMode:      authModeNone,
				RequestBody:   map[string]any{"limit": 100},
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "after",
						LimitFieldName:  "limit",
					},
				},
			},
			expectedErr: "",
		},
		{
			name: "negative offset_limit limit",
			config: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeNone,
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "offset",
						LimitFieldName:  "limit",
						Limit:           -1,
					},
				},
			},
			expectedErr: "limit must be greater than or equal to 0",
		},
		{
			name: "epoch start_time_value with a leading plus",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeNone,
				StartTimeParamName: "since",
				StartTimeValue:     "+1704067200",
				TimestampFormat:    epochSeconds,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: `start_time_value "+1704067200" is not a valid JSON number`,
		},
		{
			name: "epoch end_time_value with a trailing dot",
			config: &Config{
				URL:              "https://api.example.com/data",
				AuthMode:         authModeNone,
				EndTimeParamName: "until",
				EndTimeValue:     "1704067200.",
				TimestampFormat:  epochSecondsFractional,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: `end_time_value "1704067200." is not a valid JSON number`,
		},
		{
			name: "valid epoch fractional start_time_value",
			config: &Config{
				URL:                "https://api.example.com/data",
				AuthMode:           authModeNone,
				StartTimeParamName: "since",
				StartTimeValue:     "1704067200.123456",
				TimestampFormat:    epochSecondsFractional,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			},
			expectedErr: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := confmap.Validate(tc.config)
			if tc.expectedErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfig_DefaultValues(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig().(*Config)

	require.Equal(t, authModeNone, cfg.AuthMode)
	require.Equal(t, methodGET, cfg.Method)
	// Must stay empty so Validate() can derive it from the method; seeding it
	// would silently give a "method: post" config query-string pagination.
	require.Empty(t, cfg.ParamLocation)
	require.Equal(t, paginationModeNone, cfg.Pagination.Mode)
	require.Equal(t, 10*time.Second, cfg.MinPollInterval)
	require.Equal(t, 5*time.Minute, cfg.MaxPollInterval)
	require.Equal(t, 0, cfg.Pagination.PageLimit)
	require.False(t, cfg.Pagination.ZeroBasedIndex)
}

func TestLoadConfigFromYAML(t *testing.T) {
	// Load the YAML config file
	cm, err := confmaptest.LoadConf(filepath.Join("testdata", "test-config.yaml"))
	require.NoError(t, err)

	factory := NewFactory()
	cfg := factory.CreateDefaultConfig()

	// Get the receivers.restapi section
	receiversMap, err := cm.Sub("receivers")
	require.NoError(t, err)

	sub, err := receiversMap.Sub(component.NewID(metadata.Type).String())
	require.NoError(t, err)

	// Unmarshal the config
	err = sub.Unmarshal(cfg)
	require.NoError(t, err)

	// Validate the config
	restapiCfg := cfg.(*Config)
	err = confmap.Validate(restapiCfg)
	require.NoError(t, err)

	// Verify the config values were parsed correctly
	require.Equal(t, "https://api.example.com/data", restapiCfg.URL)
	require.Equal(t, "data", restapiCfg.ResponseField)
	require.Equal(t, 10*time.Second, restapiCfg.MinPollInterval)
	require.Equal(t, 5*time.Minute, restapiCfg.MaxPollInterval)
	require.Equal(t, authModeAPIKey, restapiCfg.AuthMode)
	require.Equal(t, configopaque.String("test-key"), restapiCfg.APIKeyConfig.Value)
	require.Equal(t, "X-API-Key", restapiCfg.APIKeyConfig.HeaderName)

	// The POST shape must survive the YAML -> confmap -> map[string]any round
	// trip with key case, nesting, and non-string scalar types preserved.
	require.Equal(t, methodPOST, restapiCfg.Method)
	require.Equal(t, paramLocationBody, restapiCfg.ParamLocation)
	require.Equal(t, "status:'new'", restapiCfg.RequestBody["filter"])
	require.Equal(t, "created_timestamp|asc", restapiCfg.RequestBody["sort"])

	options, ok := restapiCfg.RequestBody["options"].(map[string]any)
	require.True(t, ok, "nested request_body value should unmarshal as a map")
	require.Equal(t, true, options["include_hidden"])
	require.Equal(t, []any{"a", "b"}, options["tags"])
}

func TestConfig_MethodAndParamLocationDefaults(t *testing.T) {
	testCases := []struct {
		name                  string
		method                Method
		paramLocation         ParamLocation
		expectedMethod        Method
		expectedParamLocation ParamLocation
	}{
		{
			name:                  "unset defaults to get and query",
			expectedMethod:        methodGET,
			expectedParamLocation: paramLocationQuery,
		},
		{
			name:                  "post defaults to body",
			method:                methodPOST,
			expectedMethod:        methodPOST,
			expectedParamLocation: paramLocationBody,
		},
		{
			name:                  "explicit get keeps query",
			method:                methodGET,
			expectedMethod:        methodGET,
			expectedParamLocation: paramLocationQuery,
		},
		{
			name:                  "explicit query survives post",
			method:                methodPOST,
			paramLocation:         paramLocationQuery,
			expectedMethod:        methodPOST,
			expectedParamLocation: paramLocationQuery,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				URL:           "https://api.example.com/data",
				Method:        tc.method,
				ParamLocation: tc.paramLocation,
				AuthMode:      authModeNone,
				Pagination: PaginationConfig{
					Mode: paginationModeNone,
				},
			}
			require.NoError(t, cfg.Validate())
			require.Equal(t, tc.expectedMethod, cfg.Method)
			require.Equal(t, tc.expectedParamLocation, cfg.ParamLocation)
		})
	}
}

func TestMethod_UnmarshalText(t *testing.T) {
	testCases := []struct {
		input       string
		expected    Method
		expectedErr string
	}{
		{input: "get", expected: methodGET},
		{input: "post", expected: methodPOST},
		{input: "PATCH", expectedErr: "invalid method: PATCH, must be one of: get, post"},
		{input: "POST", expectedErr: "invalid method: POST, must be one of: get, post"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			var m Method
			err := m.UnmarshalText([]byte(tc.input))
			if tc.expectedErr != "" {
				require.EqualError(t, err, tc.expectedErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, m)
		})
	}
}

func TestMethod_HTTPMethod(t *testing.T) {
	require.Equal(t, http.MethodPost, methodPOST.httpMethod())
	require.Equal(t, http.MethodGet, methodGET.httpMethod())
	// An unset method must map to GET: the client tests and receiver tests build
	// Config literals that never run through Validate().
	require.Equal(t, http.MethodGet, Method("").httpMethod())
}

func TestParamLocation_UnmarshalText(t *testing.T) {
	testCases := []struct {
		input       string
		expected    ParamLocation
		expectedErr string
	}{
		{input: "query", expected: paramLocationQuery},
		{input: "body", expected: paramLocationBody},
		{input: "header", expectedErr: "invalid param_location: header, must be one of: query, body"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			var loc ParamLocation
			err := loc.UnmarshalText([]byte(tc.input))
			if tc.expectedErr != "" {
				require.EqualError(t, err, tc.expectedErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, loc)
		})
	}
}

func TestConfig_DeprecatedTimestampMigration(t *testing.T) {
	testCases := []struct {
		name               string
		rawConfig          map[string]any
		expectedStartParam string
		expectedStartValue string
		expectedEndParam   string
		expectedEndValue   string
		expectedFormat     string
		expectedWarnings   int
		warnContains       string
	}{
		{
			name: "migrates old pagination.timestamp fields to top-level",
			rawConfig: map[string]any{
				"url":       "https://api.example.com/data",
				"auth_mode": "none",
				"pagination": map[string]any{
					"mode": "timestamp",
					"timestamp": map[string]any{
						"param_name":           "since",
						"initial_timestamp":    "2025-01-01T00:00:00Z",
						"timestamp_format":     "2006-01-02T15:04:05Z07:00",
						"timestamp_field_name": "updated_at",
					},
				},
			},
			expectedStartParam: "since",
			expectedStartValue: "2025-01-01T00:00:00Z",
			expectedFormat:     "2006-01-02T15:04:05Z07:00",
			expectedWarnings:   3,
			warnContains:       "is deprecated",
		},
		{
			name: "new fields take precedence over old fields",
			rawConfig: map[string]any{
				"url":                   "https://api.example.com/data",
				"auth_mode":             "none",
				"start_time_param_name": "from",
				"start_time_value":      "2025-06-01T00:00:00Z",
				"pagination": map[string]any{
					"mode": "timestamp",
					"timestamp": map[string]any{
						"param_name":           "since",
						"initial_timestamp":    "2025-01-01T00:00:00Z",
						"timestamp_field_name": "updated_at",
					},
				},
			},
			expectedStartParam: "from",
			expectedStartValue: "2025-06-01T00:00:00Z",
			expectedWarnings:   2,
			warnContains:       "both deprecated",
		},
		{
			name: "no warnings when only new fields are used",
			rawConfig: map[string]any{
				"url":                   "https://api.example.com/data",
				"auth_mode":             "none",
				"start_time_param_name": "since",
				"start_time_value":      "2025-01-01T00:00:00Z",
				"pagination": map[string]any{
					"mode": "timestamp",
					"timestamp": map[string]any{
						"timestamp_field_name": "updated_at",
					},
				},
			},
			expectedStartParam: "since",
			expectedStartValue: "2025-01-01T00:00:00Z",
			expectedWarnings:   0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			conf := confmap.NewFromStringMap(tc.rawConfig)

			cfg := &Config{}
			err := cfg.Unmarshal(conf)
			require.NoError(t, err)

			if tc.expectedStartParam != "" {
				assert.Equal(t, tc.expectedStartParam, cfg.StartTimeParamName)
			}
			if tc.expectedStartValue != "" {
				assert.Equal(t, tc.expectedStartValue, cfg.StartTimeValue)
			}
			if tc.expectedEndParam != "" {
				assert.Equal(t, tc.expectedEndParam, cfg.EndTimeParamName)
			}
			if tc.expectedEndValue != "" {
				assert.Equal(t, tc.expectedEndValue, cfg.EndTimeValue)
			}
			if tc.expectedFormat != "" {
				assert.Equal(t, tc.expectedFormat, cfg.TimestampFormat)
			}

			assert.Len(t, cfg.deprecationWarnings, tc.expectedWarnings)
			if tc.warnContains != "" {
				found := false
				for _, w := range cfg.deprecationWarnings {
					if strings.Contains(w, tc.warnContains) {
						found = true
						break
					}
				}
				assert.True(t, found, "expected warning containing %q, got %v", tc.warnContains, cfg.deprecationWarnings)
			}
		})
	}
}

func TestIsValidJSONNumber(t *testing.T) {
	valid := []string{"0", "123", "-123", "1704067200123456789", "1704067200.123456", "1.5e3"}
	for _, v := range valid {
		require.True(t, isValidJSONNumber(v), "%q should be a valid JSON number", v)
	}

	invalid := []string{"", "+1704067200", "1704067200.", ".5", "0x10", "abc", `"123"`, "true", "1 2", "--1"}
	for _, v := range invalid {
		require.False(t, isValidJSONNumber(v), "%q should not be a valid JSON number", v)
	}
}

// TestEpochTimeBoundsMarshalIntoRequestBody guards the bug isValidJSONNumber
// closes: any epoch format accepted by Validate() must produce a body that
// actually marshals.
func TestEpochTimeBoundsMarshalIntoRequestBody(t *testing.T) {
	formats := map[string]string{
		epochSeconds:           "1704067200",
		epochMilliseconds:      "1704067200000",
		epochMicroseconds:      "1704067200000000",
		epochNanoseconds:       "1704067200000000000",
		epochSecondsFractional: "1704067200.123456",
	}

	for format, value := range formats {
		t.Run(format, func(t *testing.T) {
			cfg := &Config{
				URL:                "https://api.example.com/data",
				Method:             methodPOST,
				AuthMode:           authModeNone,
				StartTimeParamName: "since",
				StartTimeValue:     value,
				EndTimeParamName:   "until",
				EndTimeValue:       "now",
				TimestampFormat:    format,
				Pagination:         PaginationConfig{Mode: paginationModeNone},
			}
			require.NoError(t, cfg.Validate())

			body, err := marshalJSONBody(buildPaginationValues(cfg, newPaginationState(cfg)))
			require.NoError(t, err)
			// The bound must land as a bare JSON number, not a quoted string.
			require.Contains(t, string(body), `"since":`+value)
			require.NotContains(t, string(body), `"since":"`)
		})
	}
}
