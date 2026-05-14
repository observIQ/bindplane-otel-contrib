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

package lookupprocessor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	apiDefaultMaxRetries      = 3
	apiDefaultInitialDelay    = 100 * time.Millisecond
	apiDefaultRetryMultiplier = 2
	apiDefaultRequestTimeout  = 10 * time.Second
	apiDefaultLookupTimeout   = 5 * time.Second

	// apiMaxResponseBytes caps how much of a response body the source will read
	// to protect against misbehaving or hostile endpoints returning huge payloads.
	apiMaxResponseBytes = 1 << 20 // 1 MiB

	// apiErrorBodyMax caps how many bytes of a failing response body are
	// embedded in an error string. Without a cap, a burst of large failing
	// lookups would allocate megabytes per error and bloat logs.
	apiErrorBodyMax = 256
)

// nonRetryableStatusError signals an HTTP status that should not be retried.
type nonRetryableStatusError struct {
	status int
	body   string
}

func (e *nonRetryableStatusError) Error() string {
	return fmt.Sprintf("API returned non-retryable status %d: %s", e.status, e.body)
}

// APISource implements LookupSource for REST API endpoints.
type APISource struct {
	urlTemplate     string
	method          string
	headers         map[string]string
	timeout         time.Duration
	lookupTimeout   time.Duration
	maxRetries      int
	initialDelay    time.Duration
	retryMultiplier int
	responseMapping map[string]string
	client          *http.Client
	logger          *zap.Logger
}

// NewAPISource creates a new APISource.
func NewAPISource(cfg *APIConfig, logger *zap.Logger) (*APISource, error) {
	method := cfg.Method
	if method == "" {
		method = http.MethodGet
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = apiDefaultRequestTimeout
	}

	lookupTimeout := cfg.LookupTimeout
	if lookupTimeout <= 0 {
		lookupTimeout = apiDefaultLookupTimeout
	}

	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = apiDefaultMaxRetries
	}

	initialDelay := cfg.InitialDelay
	if initialDelay <= 0 {
		initialDelay = apiDefaultInitialDelay
	}

	retryMultiplier := cfg.RetryMultiplier
	if retryMultiplier <= 0 {
		retryMultiplier = apiDefaultRetryMultiplier
	}

	client := &http.Client{Timeout: timeout}

	return &APISource{
		urlTemplate:     cfg.URL,
		method:          method,
		headers:         cfg.Headers,
		timeout:         timeout,
		lookupTimeout:   lookupTimeout,
		maxRetries:      maxRetries,
		initialDelay:    initialDelay,
		retryMultiplier: retryMultiplier,
		responseMapping: cfg.ResponseMapping,
		client:          client,
		logger:          logger,
	}, nil
}

// Lookup makes an API call with the key substituted in the URL. Honors the
// caller's context and applies an overall deadline (lookup_timeout) so a chain
// of retried slow requests cannot exceed it. Non-retryable HTTP statuses abort
// immediately; cancellation aborts pending retry sleeps promptly.
func (a *APISource) Lookup(ctx context.Context, key string) (map[string]string, error) {
	requestURL := a.substituteURL(key)

	if _, err := url.Parse(requestURL); err != nil {
		return nil, fmt.Errorf("invalid URL after substitution: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, a.lookupTimeout)
	defer cancel()

	var lastErr error
	delay := a.initialDelay

	for attempt := 0; attempt < a.maxRetries; attempt++ {
		if attempt > 0 {
			a.logger.Debug("retrying API request", zap.Int("attempt", attempt), zap.Duration("delay", delay))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			delay *= time.Duration(a.retryMultiplier)
		}

		data, err := a.makeRequest(ctx, requestURL)
		if err == nil {
			return data, nil
		}

		// Non-retryable status codes (e.g. 400, 401, 403, 404) abort immediately.
		var nrse *nonRetryableStatusError
		if errors.As(err, &nrse) {
			return nil, err
		}

		lastErr = err
		a.logger.Debug("API request failed", zap.Error(err), zap.Int("attempt", attempt+1))
	}

	return nil, fmt.Errorf("API request failed after %d attempts: %w", a.maxRetries, lastErr)
}

// Load is a no-op for API source.
func (a *APISource) Load() error {
	return nil
}

// Close cleans up idle connections.
func (a *APISource) Close() error {
	if a.client != nil {
		a.client.CloseIdleConnections()
	}
	return nil
}

func (a *APISource) substituteURL(key string) string {
	encodedKey := url.QueryEscape(key)
	result := a.urlTemplate
	result = strings.ReplaceAll(result, "${fieldValue}", encodedKey)
	result = strings.ReplaceAll(result, "$fieldValue", encodedKey)
	result = strings.ReplaceAll(result, "${key}", encodedKey)
	result = strings.ReplaceAll(result, "$key", encodedKey)
	return result
}

// truncateForError returns a printable, length-capped snippet of a response
// body suitable for embedding in an error string. Callers may pass arbitrary
// (potentially binary or huge) bytes.
func truncateForError(body []byte) string {
	if len(body) <= apiErrorBodyMax {
		return string(body)
	}
	return string(body[:apiErrorBodyMax]) + "...(truncated)"
}

// isRetryableStatus reports whether an HTTP status warrants a retry. 4xx is
// generally a deterministic client error and not retried, except 408 (request
// timeout) and 429 (too many requests) which can succeed if tried again.
func isRetryableStatus(status int) bool {
	switch {
	case status >= 500:
		return true
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func (a *APISource) makeRequest(parent context.Context, requestURL string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(parent, a.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, a.method, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	for key, value := range a.headers {
		req.Header.Set(key, value)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap how much of the response we will read to defend against misbehaving
	// or hostile endpoints.
	body, err := io.ReadAll(io.LimitReader(resp.Body, apiMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		snippet := truncateForError(body)
		if !isRetryableStatus(resp.StatusCode) {
			return nil, &nonRetryableStatusError{status: resp.StatusCode, body: snippet}
		}
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, snippet)
	}

	var jsonData map[string]interface{}
	if err := json.Unmarshal(body, &jsonData); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	if len(a.responseMapping) > 0 {
		return a.applyResponseMapping(jsonData), nil
	}

	return a.flattenJSON(jsonData), nil
}

func (a *APISource) applyResponseMapping(jsonData map[string]interface{}) map[string]string {
	result := make(map[string]string)

	for fieldName, jsonPath := range a.responseMapping {
		value, err := a.extractJSONPath(jsonData, jsonPath)
		if err != nil {
			a.logger.Debug("failed to extract JSON path", zap.String("path", jsonPath), zap.Error(err))
			continue
		}
		result[fieldName] = value
	}

	return result
}

func (a *APISource) extractJSONPath(data map[string]interface{}, path string) (string, error) {
	parts := strings.Split(path, ".")
	var current interface{} = data

	for _, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			next, ok := v[part]
			if !ok {
				return "", fmt.Errorf("path segment '%s' not found", part)
			}
			current = next
		default:
			return "", fmt.Errorf("cannot navigate through non-object at '%s'", part)
		}
	}

	return a.valueToString(current), nil
}

func (a *APISource) flattenJSON(data map[string]interface{}) map[string]string {
	result := make(map[string]string)
	for key, value := range data {
		result[key] = a.valueToString(value)
	}
	return result
}

func (a *APISource) valueToString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%v", v)
	case int:
		return fmt.Sprintf("%d", v)
	case bool:
		return fmt.Sprintf("%t", v)
	case nil:
		return ""
	default:
		if bytes, err := json.Marshal(v); err == nil {
			return string(bytes)
		}
		return fmt.Sprintf("%v", v)
	}
}
