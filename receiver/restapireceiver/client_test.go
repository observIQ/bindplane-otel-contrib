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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/configopaque"
)

func TestNewRESTAPIClient(t *testing.T) {
	testCases := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{
			name: "valid config with apikey auth",
			cfg: &Config{
				URL:      "https://api.example.com/data",
				AuthMode: authModeAPIKey,
				APIKeyConfig: APIKeyConfig{
					HeaderName: "X-API-Key",
					Value:      "test-key",
				},
				ClientConfig: confighttp.ClientConfig{},
			},
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			host := componenttest.NewNopHost()
			settings := componenttest.NewNopTelemetrySettings()

			client, err := newRESTAPIClient(ctx, settings, tc.cfg, host)
			if tc.wantErr {
				require.Error(t, err)
				require.Nil(t, client)
			} else {
				require.NoError(t, err)
				require.NotNil(t, client)
			}
		})
	}
}

func TestRESTAPIClient_GetJSON_NoAuth(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify API key header is set
		require.Equal(t, "test-key", r.Header.Get("X-API-Key"))

		// Return JSON array
		response := []map[string]any{
			{"id": "1", "name": "test1"},
			{"id": "2", "name": "test2"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeAPIKey,
		APIKeyConfig: APIKeyConfig{
			HeaderName: "X-API-Key",
			Value:      "test-key",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 2)
	require.Equal(t, "1", data[0]["id"])
	require.Equal(t, "test1", data[0]["name"])
	require.Equal(t, "2", data[1]["id"])
	require.Equal(t, "test2", data[1]["name"])
}

func TestRESTAPIClient_GetJSON_NoAuthMode(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify no Authorization header is set
		require.Empty(t, r.Header.Get("Authorization"))

		// Return JSON array
		response := []map[string]any{
			{"id": "1", "name": "public-data"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:          server.URL,
		AuthMode:     authModeNone,
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
	require.Equal(t, "1", data[0]["id"])
	require.Equal(t, "public-data", data[0]["name"])
}

func TestRESTAPIClient_GetJSON_APIKeyAuth(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify API key header
		require.Equal(t, "test-api-key", r.Header.Get("X-API-Key"))

		response := []map[string]any{
			{"id": "1"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeAPIKey,
		APIKeyConfig: APIKeyConfig{
			HeaderName: "X-API-Key",
			Value:      "test-api-key",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
}

func TestRESTAPIClient_GetJSON_BearerAuth(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify bearer token
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		response := []map[string]any{
			{"id": "1"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeBearer,
		BearerConfig: BearerConfig{
			Token: "test-token",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
}

func TestRESTAPIClient_GetJSON_BasicAuth(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify basic auth
		username, password, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "testuser", username)
		require.Equal(t, "testpass", password)

		response := []map[string]any{
			{"id": "1"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeBasic,
		BasicConfig: BasicConfig{
			Username: "testuser",
			Password: "testpass",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
}

func TestRESTAPIClient_GetJSON_OAuth2Auth(t *testing.T) {
	// Create a mock OAuth2 token server
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's a token request
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/token", r.URL.Path)

		// Parse form data
		err := r.ParseForm()
		require.NoError(t, err)

		// Verify grant type (client credentials can be sent in Authorization header or form)
		require.Equal(t, "client_credentials", r.Form.Get("grant_type"))

		// Verify client credentials - they can be in form or Authorization header
		clientID := r.Form.Get("client_id")
		clientSecret := r.Form.Get("client_secret")
		if clientID == "" {
			// Check Authorization header for Basic auth
			username, password, ok := r.BasicAuth()
			require.True(t, ok)
			clientID = username
			clientSecret = password
		}
		require.Equal(t, "test-client-id", clientID)
		require.Equal(t, "test-client-secret", clientSecret)

		// Return access token
		response := map[string]any{
			"access_token": "test-oauth2-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer tokenServer.Close()

	// Create API test server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify OAuth2 bearer token
		require.Equal(t, "Bearer test-oauth2-token", r.Header.Get("Authorization"))

		response := []map[string]any{
			{"id": "1"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer apiServer.Close()

	cfg := &Config{
		URL:      apiServer.URL,
		AuthMode: authModeOAuth2,
		OAuth2Config: OAuth2Config{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			TokenURL:     tokenServer.URL + "/token",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, apiServer.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
}

func TestRESTAPIClient_GetJSON_OAuth2Auth_WithScopes(t *testing.T) {
	// Create a mock OAuth2 token server
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse form data
		err := r.ParseForm()
		require.NoError(t, err)

		// Verify scopes are included
		require.Equal(t, "read write", r.Form.Get("scope"))

		// Return access token
		response := map[string]any{
			"access_token": "test-oauth2-token-with-scopes",
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer tokenServer.Close()

	// Create API test server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify OAuth2 bearer token
		require.Equal(t, "Bearer test-oauth2-token-with-scopes", r.Header.Get("Authorization"))

		response := []map[string]any{
			{"id": "1"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer apiServer.Close()

	cfg := &Config{
		URL:      apiServer.URL,
		AuthMode: authModeOAuth2,
		OAuth2Config: OAuth2Config{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			TokenURL:     tokenServer.URL + "/token",
			Scopes:       []string{"read", "write"},
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, apiServer.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
}

func TestRESTAPIClient_GetJSON_AkamaiEdgeGridAuth(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Akamai EdgeGrid auth header format
		authHeader := r.Header.Get("Authorization")
		require.NotEmpty(t, authHeader)
		require.Contains(t, authHeader, "EG1-HMAC-SHA256")
		require.Contains(t, authHeader, "client_token=test-client-token")
		require.Contains(t, authHeader, "access_token=test-access-token")
		require.Contains(t, authHeader, "timestamp=")
		require.Contains(t, authHeader, "nonce=")
		require.Contains(t, authHeader, "signature=")

		response := []map[string]any{
			{"id": "1"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeAkamaiEdgeGrid,
		AkamaiEdgeGridConfig: AkamaiEdgeGridConfig{
			AccessToken:  "test-access-token",
			ClientToken:  "test-client-token",
			ClientSecret: "test-client-secret",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
}

func TestRESTAPIClient_GetJSON_WithQueryParams(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify query parameters
		require.Equal(t, "value1", r.URL.Query().Get("param1"))
		require.Equal(t, "value2", r.URL.Query().Get("param2"))

		response := []map[string]any{
			{"id": "1"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeAPIKey,
		APIKeyConfig: APIKeyConfig{
			HeaderName: "X-API-Key",
			Value:      "test-key",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	params.Set("param1", "value1")
	params.Set("param2", "value2")
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
}

func TestRESTAPIClient_GetJSON_ResponseField(t *testing.T) {
	// Create a test server that returns nested JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]any{
			"data": []map[string]any{
				{"id": "1", "name": "test1"},
				{"id": "2", "name": "test2"},
			},
			"meta": map[string]any{
				"count": 2,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		ResponseField: "data",
		AuthMode:      authModeAPIKey,
		APIKeyConfig: APIKeyConfig{
			HeaderName: "X-API-Key",
			Value:      "test-key",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 2)
	require.Equal(t, "1", data[0]["id"])
	require.Equal(t, "test1", data[0]["name"])
}

func TestRESTAPIClient_GetJSON_HTTPError(t *testing.T) {
	// Create a test server that returns error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeAPIKey,
		APIKeyConfig: APIKeyConfig{
			HeaderName: "X-API-Key",
			Value:      "test-key",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.Error(t, err)
	require.Nil(t, data)
	require.Contains(t, err.Error(), "500")
}

func TestRESTAPIClient_GetJSON_InvalidJSON(t *testing.T) {
	// Create a test server that returns invalid JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeAPIKey,
		APIKeyConfig: APIKeyConfig{
			HeaderName: "X-API-Key",
			Value:      "test-key",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.Error(t, err)
	require.Nil(t, data)
}

func TestRESTAPIClient_GetJSON_CustomHeaders(t *testing.T) {
	// Create a test server that verifies custom headers
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify custom headers are set
		require.Equal(t, "custom-value", r.Header.Get("X-Custom-Header"))
		require.Equal(t, "tenant-123", r.Header.Get("X-Tenant-ID"))
		// Verify custom header can override defaults
		require.Equal(t, "text/plain", r.Header.Get("Accept"))

		response := []map[string]any{
			{"id": "1"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeNone,
		Headers: map[string]string{
			"X-Custom-Header": "custom-value",
			"X-Tenant-ID":     "tenant-123",
			"Accept":          "text/plain",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
}

func TestRESTAPIClient_GetFullResponse_CustomHeaders(t *testing.T) {
	// Create a test server that verifies custom headers on GetFullResponse
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "custom-value", r.Header.Get("X-Custom-Header"))

		response := map[string]any{
			"data": []map[string]any{
				{"id": "1"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeNone,
		Headers: map[string]string{
			"X-Custom-Header": "custom-value",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetFullResponse(ctx, server.URL, params)
	require.NoError(t, err)
	require.NotNil(t, data)
}

func TestRESTAPIClient_GetJSON_SensitiveHeaders(t *testing.T) {
	// Create a test server that verifies sensitive headers are sent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify sensitive header is set
		require.Equal(t, "secret-token", r.Header.Get("X-Auth-Token"))
		// Verify regular header is also set
		require.Equal(t, "tenant-123", r.Header.Get("X-Tenant-ID"))

		response := []map[string]any{
			{"id": "1"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeNone,
		Headers: map[string]string{
			"X-Tenant-ID": "tenant-123",
		},
		SensitiveHeaders: map[string]configopaque.String{
			"X-Auth-Token": "secret-token",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 1)
}

func TestRESTAPIClient_GetFullResponse_SensitiveHeaders(t *testing.T) {
	// Create a test server that verifies sensitive headers on GetFullResponse
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "secret-value", r.Header.Get("X-Secret"))

		response := map[string]any{
			"data": []map[string]any{
				{"id": "1"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeNone,
		SensitiveHeaders: map[string]configopaque.String{
			"X-Secret": "secret-value",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetFullResponse(ctx, server.URL, params)
	require.NoError(t, err)
	require.NotNil(t, data)
}

func TestRESTAPIClient_GetNDJSON(t *testing.T) {
	// Create a test server that returns NDJSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-key", r.Header.Get("X-API-Key"))

		// NDJSON: data lines + metadata line (last)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"id":"1","message":"event1"}` + "\n"))
		w.Write([]byte(`{"id":"2","message":"event2"}` + "\n"))
		w.Write([]byte(`{"id":"3","message":"event3"}` + "\n"))
		w.Write([]byte(`{"offset":"abc123","total":3}` + "\n"))
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeAPIKey,
		APIKeyConfig: APIKeyConfig{
			HeaderName: "X-API-Key",
			Value:      "test-key",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, metadata, err := client.GetNDJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.Len(t, data, 3)
	require.Equal(t, "1", data[0]["id"])
	require.Equal(t, "event1", data[0]["message"])
	require.Equal(t, "2", data[1]["id"])
	require.Equal(t, "3", data[2]["id"])
	require.Equal(t, "abc123", metadata["offset"])
	require.Equal(t, float64(3), metadata["total"])
}

func TestRESTAPIClient_GetNDJSON_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		// Empty response body
	}))
	defer server.Close()

	cfg := &Config{
		URL:          server.URL,
		AuthMode:     authModeNone,
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	data, metadata, err := client.GetNDJSON(ctx, server.URL, url.Values{})
	require.NoError(t, err)
	require.Len(t, data, 0)
	require.Empty(t, metadata)
}

func TestRESTAPIClient_GetNDJSON_MetadataOnly(t *testing.T) {
	// Single line = metadata only, no data
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"offset":"xyz","total":0}` + "\n"))
	}))
	defer server.Close()

	cfg := &Config{
		URL:          server.URL,
		AuthMode:     authModeNone,
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	data, metadata, err := client.GetNDJSON(ctx, server.URL, url.Values{})
	require.NoError(t, err)
	require.Len(t, data, 0)
	require.Equal(t, "xyz", metadata["offset"])
	require.Equal(t, float64(0), metadata["total"])
}

func TestRESTAPIClient_GetNDJSON_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("Forbidden"))
	}))
	defer server.Close()

	cfg := &Config{
		URL:          server.URL,
		AuthMode:     authModeNone,
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	data, metadata, err := client.GetNDJSON(ctx, server.URL, url.Values{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
	require.Nil(t, data)
	require.Nil(t, metadata)
}

func TestParseNDJSON(t *testing.T) {
	logger := componenttest.NewNopTelemetrySettings().Logger

	testCases := []struct {
		name             string
		body             string
		expectedData     int
		expectedMetadata map[string]any
		expectErr        bool
	}{
		{
			name:             "empty body",
			body:             "",
			expectedData:     0,
			expectedMetadata: map[string]any{},
		},
		{
			name:             "metadata only",
			body:             `{"offset":"abc"}`,
			expectedData:     0,
			expectedMetadata: map[string]any{"offset": "abc"},
		},
		{
			name:         "data and metadata",
			body:         "{\"id\":\"1\"}\n{\"id\":\"2\"}\n{\"offset\":\"next\",\"total\":2}",
			expectedData: 2,
			expectedMetadata: map[string]any{
				"offset": "next",
				"total":  float64(2),
			},
		},
		{
			name:         "with blank lines",
			body:         "{\"id\":\"1\"}\n\n{\"id\":\"2\"}\n\n{\"offset\":\"next\"}\n",
			expectedData: 2,
			expectedMetadata: map[string]any{
				"offset": "next",
			},
		},
		{
			name:      "invalid metadata line",
			body:      "{\"id\":\"1\"}\nnot-json",
			expectErr: true,
		},
		{
			name:         "invalid data line is skipped",
			body:         "not-json\n{\"id\":\"1\"}\n{\"offset\":\"abc\"}",
			expectedData: 1,
			expectedMetadata: map[string]any{
				"offset": "abc",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, metadata, err := parseNDJSON([]byte(tc.body), logger)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, data, tc.expectedData)
			if tc.expectedMetadata != nil {
				for k, v := range tc.expectedMetadata {
					require.Equal(t, v, metadata[k])
				}
			}
		})
	}
}

func TestRESTAPIClient_GetJSON_EmptyArray(t *testing.T) {
	// Create a test server that returns empty array
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := []map[string]any{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeAPIKey,
		APIKeyConfig: APIKeyConfig{
			HeaderName: "X-API-Key",
			Value:      "test-key",
		},
		ClientConfig: confighttp.ClientConfig{},
	}

	ctx := context.Background()
	host := componenttest.NewNopHost()
	settings := componenttest.NewNopTelemetrySettings()

	client, err := newRESTAPIClient(ctx, settings, cfg, host)
	require.NoError(t, err)

	params := url.Values{}
	data, err := client.GetJSON(ctx, server.URL, params)
	require.NoError(t, err)
	require.NotNil(t, data)
	require.Len(t, data, 0)
}
