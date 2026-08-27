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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v13/pkg/edgegrid"
	jsoniter "github.com/json-iterator/go"
	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

var _ restAPIClient = (*defaultRESTAPIClient)(nil)

// apiRequest describes a single outbound polling request.
type apiRequest struct {
	// URL is the request URL. Any query string it already carries is preserved.
	URL string
	// Query holds parameters merged into the URL's existing query string.
	Query url.Values
	// Body is the rendered JSON request body, ignored when the configured method
	// takes no body.
	Body []byte
}

// restAPIClient is an interface for making REST API requests.
// This interface allows for easier testing by enabling mock implementations.
type restAPIClient interface {
	// FetchFullResponse fetches the full JSON response for the given request.
	// Returns the full response as map[string]any for pagination parsing, plus
	// response headers.
	FetchFullResponse(ctx context.Context, req apiRequest) (map[string]any, http.Header, error)
	// FetchNDJSON fetches an NDJSON response for the given request.
	// When metadataInBody is true the last line is treated as pagination metadata;
	// when false all lines are treated as data (metadata comes from headers instead).
	FetchNDJSON(ctx context.Context, req apiRequest, metadataInBody bool) (data []map[string]any, metadata map[string]any, headers http.Header, err error)
	// Shutdown shuts down the REST API client.
	Shutdown() error
}

// defaultRESTAPIClient is the default implementation of restAPIClient.
type defaultRESTAPIClient struct {
	client        *http.Client
	cfg           *Config
	logger        *zap.Logger
	responseField string
	tokenSource   oauth2.TokenSource
}

// newRESTAPIClient creates a new REST API client.
func newRESTAPIClient(
	ctx context.Context,
	settings component.TelemetrySettings,
	cfg *Config,
	host component.Host,
) (restAPIClient, error) {
	httpClient, err := cfg.ClientConfig.ToClient(ctx, host.GetExtensions(), settings)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	client := &defaultRESTAPIClient{
		client:        httpClient,
		cfg:           cfg,
		logger:        settings.Logger,
		responseField: cfg.ResponseField,
	}

	// Initialize OAuth2 token source if OAuth2 auth mode is configured
	if cfg.AuthMode == authModeOAuth2 {
		tokenSource, err := client.createOAuth2TokenSource(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create OAuth2 token source: %w", err)
		}
		client.tokenSource = tokenSource
	}

	return client, nil
}

// Shutdown shuts down the REST API client.
func (c *defaultRESTAPIClient) Shutdown() error {
	c.client.CloseIdleConnections()
	return nil
}

// buildRequest constructs the outgoing request: URL with merged query
// parameters, JSON body when the method takes one, auth, and headers.
//
// The body is attached before applyAuth because Akamai EdgeGrid hashes the POST
// body into its signing string. A *bytes.Reader gives ContentLength and GetBody,
// keeping the request replayable across redirects and retries; EdgeGrid swaps
// req.Body for a NopCloser but leaves both intact.
func (c *defaultRESTAPIClient) buildRequest(ctx context.Context, r apiRequest) (*http.Request, error) {
	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	// Add query parameters
	if len(r.Query) > 0 {
		existingParams := u.Query()
		for key, values := range r.Query {
			for _, value := range values {
				existingParams.Add(key, value)
			}
		}
		u.RawQuery = existingParams.Encode()
	}

	var bodyReader io.Reader
	hasBody := false
	if c.cfg.Method == methodPOST {
		body := r.Body
		if len(body) == 0 {
			// A POST always carries a body; send an empty object when the config
			// supplies no request_body template.
			body = []byte("{}")
		}
		bodyReader = bytes.NewReader(body)
		hasBody = true
	}

	req, err := http.NewRequestWithContext(ctx, c.cfg.Method.httpMethod(), u.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set default headers
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}

	// Apply authentication (after the body is attached, so EdgeGrid can hash it)
	if err := c.applyAuth(req); err != nil {
		return nil, fmt.Errorf("failed to apply authentication: %w", err)
	}

	// Apply custom headers (may override defaults)
	c.applyHeaders(req)

	return req, nil
}

// do issues the request and returns the raw response body and headers.
func (c *defaultRESTAPIClient) do(ctx context.Context, r apiRequest) ([]byte, http.Header, error) {
	req, err := c.buildRequest(ctx, r)
	if err != nil {
		return nil, nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Check for HTTP errors
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(body))
	}

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return body, resp.Header, nil
}

// GetJSON fetches JSON data and extracts the array of items from it.
//
// Not part of restAPIClient and unused by the receiver, which goes through
// FetchFullResponse and extractDataFromResponse. Retained as the vehicle for
// the client's authentication-mode test coverage.
func (c *defaultRESTAPIClient) GetJSON(ctx context.Context, r apiRequest) ([]map[string]any, error) {
	body, _, err := c.do(ctx, r)
	if err != nil {
		return nil, err
	}

	// Parse JSON
	var jsonData any
	if err := jsoniter.Unmarshal(body, &jsonData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	// Extract the array from the response
	var dataArray []any
	if c.responseField != "" {
		// Response has a field containing the array
		responseMap, ok := jsonData.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("response is not a JSON object when response_field is set")
		}
		fieldValue, ok := responseMap[c.responseField]
		if !ok {
			return nil, fmt.Errorf("response field '%s' not found in response", c.responseField)
		}
		dataArray, ok = fieldValue.([]any)
		if !ok {
			return nil, fmt.Errorf("response field '%s' is not an array", c.responseField)
		}
	} else {
		// Response is directly an array
		var ok bool
		dataArray, ok = jsonData.([]any)
		if !ok {
			return nil, fmt.Errorf("response is not a JSON array")
		}
	}

	// Convert []any to []map[string]any
	result := make([]map[string]any, 0, len(dataArray))
	for _, item := range dataArray {
		itemMap, ok := item.(map[string]any)
		if !ok {
			c.logger.Warn("skipping non-object item in array", zap.Any("item", item))
			continue
		}
		result = append(result, itemMap)
	}

	return result, nil
}

// FetchFullResponse fetches the full JSON response for the given request.
func (c *defaultRESTAPIClient) FetchFullResponse(ctx context.Context, r apiRequest) (map[string]any, http.Header, error) {
	body, headers, err := c.do(ctx, r)
	if err != nil {
		return nil, nil, err
	}

	// Parse JSON
	var jsonData any
	if err := jsoniter.Unmarshal(body, &jsonData); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	// Return as map
	responseMap, ok := jsonData.(map[string]any)
	if !ok {
		// If response is an array, wrap it in a map
		if arr, ok := jsonData.([]any); ok {
			return map[string]any{"data": arr}, headers, nil
		}
		return nil, nil, fmt.Errorf("response is not a JSON object or array")
	}

	return responseMap, headers, nil
}

// FetchNDJSON fetches an NDJSON response for the given request.
// Each line of the response is a separate JSON object. When metadataInBody is
// true the last line is treated as metadata (e.g., containing pagination cursors
// like an offset token) and all other lines are returned as data objects.
func (c *defaultRESTAPIClient) FetchNDJSON(ctx context.Context, r apiRequest, metadataInBody bool) ([]map[string]any, map[string]any, http.Header, error) {
	body, headers, err := c.do(ctx, r)
	if err != nil {
		return nil, nil, nil, err
	}

	data, metadata, err := parseNDJSON(body, metadataInBody, c.logger)
	if err != nil {
		return nil, nil, nil, err
	}
	return data, metadata, headers, nil
}

// parseNDJSON parses an NDJSON response body into data objects and a metadata object.
// When metadataInBody is true, the last non-empty line is treated as metadata and all
// preceding lines are data objects. When false, all lines are treated as data and
// metadata is returned as nil (the caller is expected to source metadata elsewhere,
// e.g. from response headers).
// Empty lines are skipped.
func parseNDJSON(body []byte, metadataInBody bool, logger *zap.Logger) ([]map[string]any, map[string]any, error) {
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")

	// Filter out empty lines
	var nonEmptyLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			nonEmptyLines = append(nonEmptyLines, trimmed)
		}
	}

	if len(nonEmptyLines) == 0 {
		return []map[string]any{}, map[string]any{}, nil
	}

	var metadataLine string
	dataLines := nonEmptyLines

	if metadataInBody {
		// Last line is metadata, everything else is data
		metadataLine = nonEmptyLines[len(nonEmptyLines)-1]
		dataLines = nonEmptyLines[:len(nonEmptyLines)-1]
	}

	// Parse metadata
	var metadata map[string]any
	if metadataLine != "" {
		if err := jsoniter.UnmarshalFromString(metadataLine, &metadata); err != nil {
			return nil, nil, fmt.Errorf("failed to parse NDJSON metadata line: %w", err)
		}
	}

	// Parse data lines
	data := make([]map[string]any, 0, len(dataLines))
	for i, line := range dataLines {
		var obj map[string]any
		if err := jsoniter.UnmarshalFromString(line, &obj); err != nil {
			logger.Warn("skipping invalid NDJSON line",
				zap.Int("line_number", i+1),
				zap.Error(err))
			continue
		}
		data = append(data, obj)
	}

	return data, metadata, nil
}

// signEdgeGridRequest applies Akamai EdgeGrid authentication to the request
// using the official Akamai EdgeGrid Go library, which handles signing-string
// construction, URL escaping, content hashing for request bodies, and adding
// the accountSwitchKey query parameter when configured.
func (c *defaultRESTAPIClient) signEdgeGridRequest(req *http.Request) {
	egCfg := edgegrid.Config{
		ClientToken:  string(c.cfg.AkamaiEdgeGridConfig.ClientToken),
		ClientSecret: string(c.cfg.AkamaiEdgeGridConfig.ClientSecret),
		AccessToken:  string(c.cfg.AkamaiEdgeGridConfig.AccessToken),
		AccountKey:   c.cfg.AkamaiEdgeGridConfig.AccountKey,
		MaxBody:      edgegrid.MaxBodySize,
	}
	egCfg.SignRequest(req)
}

// createOAuth2TokenSource creates an OAuth2 token source for client credentials flow.
func (c *defaultRESTAPIClient) createOAuth2TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	oauthConfig := clientcredentials.Config{
		ClientID:       c.cfg.OAuth2Config.ClientID,
		ClientSecret:   string(c.cfg.OAuth2Config.ClientSecret),
		TokenURL:       c.cfg.OAuth2Config.TokenURL,
		Scopes:         c.cfg.OAuth2Config.Scopes,
		EndpointParams: url.Values{},
	}

	// Add any additional endpoint parameters
	for key, value := range c.cfg.OAuth2Config.EndpointParams {
		oauthConfig.EndpointParams.Add(key, value)
	}

	// Use the existing HTTP client for OAuth2 requests
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.client)

	return oauthConfig.TokenSource(ctx), nil
}

// applyHeaders applies custom headers from configuration to the request.
// Custom headers are applied after default headers (Accept) and
// authentication headers, allowing them to override any previously set values.
// Sensitive headers are applied last, so they take precedence over regular headers.
func (c *defaultRESTAPIClient) applyHeaders(req *http.Request) {
	for key, value := range c.cfg.Headers {
		req.Header.Set(key, value)
	}
	for key, value := range c.cfg.SensitiveHeaders {
		req.Header.Set(key, string(value))
	}
}

// applyAuth applies authentication headers to the request based on the configured auth mode.
func (c *defaultRESTAPIClient) applyAuth(req *http.Request) error {
	switch c.cfg.AuthMode {

	case authModeNone:
		// No authentication required
		return nil

	case authModeAPIKey:
		// API key authentication
		if c.cfg.APIKeyConfig.HeaderName == "" || string(c.cfg.APIKeyConfig.Value) == "" {
			return fmt.Errorf("API key header name and value are required")
		}
		req.Header.Set(c.cfg.APIKeyConfig.HeaderName, string(c.cfg.APIKeyConfig.Value))
		return nil

	case authModeBearer:
		// Bearer token authentication
		if string(c.cfg.BearerConfig.Token) == "" {
			return fmt.Errorf("bearer token is required")
		}
		req.Header.Set("Authorization", authHeaderPrefix(c.cfg.BearerConfig.HeaderPrefix)+string(c.cfg.BearerConfig.Token))
		return nil

	case authModeBasic:
		// Basic authentication
		if c.cfg.BasicConfig.Username == "" || string(c.cfg.BasicConfig.Password) == "" {
			return fmt.Errorf("basic auth username and password are required")
		}
		req.SetBasicAuth(c.cfg.BasicConfig.Username, string(c.cfg.BasicConfig.Password))
		return nil

	case authModeOAuth2:
		// OAuth2 client credentials authentication
		if c.tokenSource == nil {
			return fmt.Errorf("OAuth2 token source not initialized")
		}
		token, err := c.tokenSource.Token()
		if err != nil {
			return fmt.Errorf("failed to get OAuth2 token: %w", err)
		}
		req.Header.Set("Authorization", authHeaderPrefix(c.cfg.OAuth2Config.HeaderPrefix)+token.AccessToken)
		return nil

	case authModeAkamaiEdgeGrid:
		// Akamai EdgeGrid authentication
		if string(c.cfg.AkamaiEdgeGridConfig.AccessToken) == "" || string(c.cfg.AkamaiEdgeGridConfig.ClientToken) == "" || string(c.cfg.AkamaiEdgeGridConfig.ClientSecret) == "" {
			return fmt.Errorf("akamai edgegrid access token, client token, and client secret are required")
		}
		c.signEdgeGridRequest(req)
		return nil

	default:
		return fmt.Errorf("unsupported auth mode: %s", c.cfg.AuthMode)
	}
}
