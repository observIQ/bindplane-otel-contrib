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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/observiq/bindplane-otel-contrib/receiver/restapireceiver/internal/metadata"
)

// TestIntegration_EndToEnd_Logs tests a complete end-to-end scenario for logs collection.
func TestIntegration_EndToEnd_Logs(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		response := map[string]any{
			"logs": []map[string]any{
				{"id": "1", "level": "info", "message": "test log 1", "timestamp": time.Now().Format(time.RFC3339)},
				{"id": "2", "level": "error", "message": "test log 2", "timestamp": time.Now().Format(time.RFC3339)},
			},
			"total": 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		ResponseField: "logs",
		AuthMode:      authModeAPIKey,
		APIKeyConfig: APIKeyConfig{
			HeaderName: "X-API-Key",
			Value:      "test-key",
		},
		Pagination: PaginationConfig{
			Mode: paginationModeNone,
		},
		MaxPollInterval: 100 * time.Millisecond,
		ClientConfig:    confighttp.ClientConfig{},
	}

	sink := new(consumertest.LogsSink)
	params := receivertest.NewNopSettings(metadata.Type)
	receiver, err := newRESTAPILogsReceiver(params, cfg, sink)
	require.NoError(t, err)

	host := componenttest.NewNopHost()
	ctx := context.Background()

	err = receiver.Start(ctx, host)
	require.NoError(t, err)

	// Poll until multiple requests have occurred and data has arrived, then shut down.
	require.Eventually(t, func() bool {
		return requestCount.Load() > 1 && len(sink.AllLogs()) > 0
	}, 5*time.Second, 10*time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Verify data was collected
	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)

	// Verify multiple requests were made
	require.Greater(t, int(requestCount.Load()), 1)
}

// TestIntegration_EndToEnd_Metrics tests a complete end-to-end scenario for metrics collection.
func TestIntegration_EndToEnd_Metrics(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		response := []map[string]any{
			{"metric": "cpu_usage", "value": 75.5, "timestamp": time.Now().Format(time.RFC3339)},
			{"metric": "memory_usage", "value": 60.2, "timestamp": time.Now().Format(time.RFC3339)},
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
		Pagination: PaginationConfig{
			Mode: paginationModeNone,
		},
		MaxPollInterval: 100 * time.Millisecond,
		ClientConfig:    confighttp.ClientConfig{},
		Metrics: MetricsConfig{
			NameField: "metric",
		},
	}

	sink := new(consumertest.MetricsSink)
	params := receivertest.NewNopSettings(metadata.Type)
	receiver, err := newRESTAPIMetricsReceiver(params, cfg, sink)
	require.NoError(t, err)

	host := componenttest.NewNopHost()
	ctx := context.Background()

	err = receiver.Start(ctx, host)
	require.NoError(t, err)

	// Poll until multiple requests have occurred and data has arrived, then shut down.
	require.Eventually(t, func() bool {
		return requestCount.Load() > 1 && len(sink.AllMetrics()) > 0
	}, 5*time.Second, 10*time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Verify data was collected
	allMetrics := sink.AllMetrics()
	require.Greater(t, len(allMetrics), 0)

	// Verify multiple requests were made
	require.Greater(t, int(requestCount.Load()), 1)
}

// TestIntegration_WithPaginationAndAuth tests a complete scenario with pagination and authentication.
func TestIntegration_WithPaginationAndAuth(t *testing.T) {
	var pageCount atomic.Int32
	expectedAuthHeader := "Bearer test-token-123"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify authentication
		authHeader := r.Header.Get("Authorization")
		require.Equal(t, expectedAuthHeader, authHeader)

		offset := r.URL.Query().Get("offset")
		_ = r.URL.Query().Get("limit") // limit parameter

		var response map[string]any
		if offset == "0" || offset == "" {
			response = map[string]any{
				"data": []map[string]any{
					{"id": "1", "event": "event1"},
					{"id": "2", "event": "event2"},
				},
				"total": 4,
			}
		} else if offset == "2" {
			response = map[string]any{
				"data": []map[string]any{
					{"id": "3", "event": "event3"},
					{"id": "4", "event": "event4"},
				},
				"total": 4,
			}
		} else {
			response = map[string]any{
				"data":  []map[string]any{},
				"total": 4,
			}
		}

		pageCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		ResponseField: "data",
		AuthMode:      authModeBearer,
		BearerConfig: BearerConfig{
			Token: "test-token-123",
		},
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
				StartingOffset:  0,
			},
			TotalRecordCountField: "total",
		},
		MaxPollInterval: 100 * time.Millisecond,
		ClientConfig:    confighttp.ClientConfig{},
	}

	sink := new(consumertest.LogsSink)
	params := receivertest.NewNopSettings(metadata.Type)
	receiver, err := newRESTAPILogsReceiver(params, cfg, sink)
	require.NoError(t, err)

	host := componenttest.NewNopHost()
	ctx := context.Background()

	err = receiver.Start(ctx, host)
	require.NoError(t, err)

	// Poll until all pages of the first cycle have been collected, then shut down.
	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 4
	}, 5*time.Second, 10*time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Verify data was collected from multiple pages
	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)

	// Count total log records
	totalRecords := 0
	for _, logs := range allLogs {
		totalRecords += logs.LogRecordCount()
	}
	// Should have received logs from multiple pages (at least 2 pages = 4 records)
	require.GreaterOrEqual(t, totalRecords, 4)
}

// TestIntegration_CustomAuthHeaderPrefix verifies that a custom Authorization
// header prefix is applied to every request across a paginated poll cycle.
func TestIntegration_CustomAuthHeaderPrefix(t *testing.T) {
	var requestCount atomic.Int32
	expectedAuthHeader := "CwsAuth Bearer=test-token-123"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, expectedAuthHeader, r.Header.Get("Authorization"))
		requestCount.Add(1)

		var data []map[string]any
		switch r.URL.Query().Get("offset") {
		case "0", "":
			data = []map[string]any{{"id": "1"}, {"id": "2"}}
		case "2":
			data = []map[string]any{{"id": "3"}, {"id": "4"}}
		default:
			data = []map[string]any{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": data, "total": 4})
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		ResponseField: "data",
		AuthMode:      authModeBearer,
		BearerConfig: BearerConfig{
			Token:        "test-token-123",
			HeaderPrefix: "CwsAuth Bearer=",
		},
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
				StartingOffset:  0,
			},
			TotalRecordCountField: "total",
		},
		MaxPollInterval: 100 * time.Millisecond,
		ClientConfig:    confighttp.ClientConfig{},
	}

	sink := new(consumertest.LogsSink)
	receiver, err := newRESTAPILogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, receiver.Start(ctx, componenttest.NewNopHost()))

	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 4
	}, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, receiver.Shutdown(ctx))

	// More than one request means the prefix survived pagination, not just the first call.
	require.Greater(t, requestCount.Load(), int32(1))
}

// TestIntegration_TimestampPagination tests timestamp-based pagination.
func TestIntegration_TimestampPagination(t *testing.T) {
	var mu sync.Mutex
	var lastTimestamp string
	var pageSize string
	var pageCount atomic.Int32
	initialTime := time.Now().Add(-1 * time.Hour)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the timestamp and page size parameters
		mu.Lock()
		lastTimestamp = r.URL.Query().Get("t0")
		pageSize = r.URL.Query().Get("perPage")
		mu.Unlock()

		var response []map[string]any
		if pageCount.Load() == 0 {
			// First page - return full page
			response = []map[string]any{
				{"id": "1", "message": "test1", "ts": time.Now().Add(-30 * time.Minute).Format(time.RFC3339)},
				{"id": "2", "message": "test2", "ts": time.Now().Add(-20 * time.Minute).Format(time.RFC3339)},
			}
		} else {
			// Second page - return partial page to stop pagination
			response = []map[string]any{
				{"id": "3", "message": "test3", "ts": time.Now().Add(-10 * time.Minute).Format(time.RFC3339)},
			}
		}
		pageCount.Add(1)
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
		StartTimeParamName: "t0",
		StartTimeValue:     initialTime.Format(time.RFC3339),
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSizeFieldName:  "perPage",
				PageSize:           200,
			},
		},
		MaxPollInterval: 100 * time.Millisecond,
		ClientConfig:    confighttp.ClientConfig{},
	}

	sink := new(consumertest.LogsSink)
	params := receivertest.NewNopSettings(metadata.Type)
	receiver, err := newRESTAPILogsReceiver(params, cfg, sink)
	require.NoError(t, err)

	host := componenttest.NewNopHost()
	ctx := context.Background()

	err = receiver.Start(ctx, host)
	require.NoError(t, err)

	// Poll until more than one page has been fetched, then shut down.
	require.Eventually(t, func() bool {
		return pageCount.Load() > 1
	}, 5*time.Second, 10*time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Verify timestamp parameter was used
	mu.Lock()
	ts := lastTimestamp
	ps := pageSize
	mu.Unlock()
	require.NotEmpty(t, ts)
	require.Contains(t, ts, "T") // RFC3339 format check
	// Verify page size parameter was used
	require.Equal(t, "200", ps)
	// Verify multiple pages were fetched
	require.Greater(t, int(pageCount.Load()), 1)
}

// TestIntegration_ErrorRecovery tests that the receiver continues polling after errors.
func TestIntegration_ErrorRecovery(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		if count == 1 {
			// First request returns error
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Internal Server Error"))
			return
		}
		// Subsequent requests succeed
		response := []map[string]any{
			{"id": "1", "message": "success"},
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
		Pagination: PaginationConfig{
			Mode: paginationModeNone,
		},
		MaxPollInterval: 100 * time.Millisecond,
		ClientConfig:    confighttp.ClientConfig{},
	}

	sink := new(consumertest.LogsSink)
	params := receivertest.NewNopSettings(metadata.Type)
	receiver, err := newRESTAPILogsReceiver(params, cfg, sink)
	require.NoError(t, err)

	host := componenttest.NewNopHost()
	ctx := context.Background()

	err = receiver.Start(ctx, host)
	require.NoError(t, err)

	// Poll until the receiver has recovered from the first error and collected data,
	// then shut down.
	require.Eventually(t, func() bool {
		return requestCount.Load() > 1 && len(sink.AllLogs()) > 0
	}, 5*time.Second, 10*time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Verify receiver continued polling after error
	require.Greater(t, int(requestCount.Load()), 1)

	// Verify some data was eventually collected
	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)
}

// The three tests below are the point of request-body templating: the same
// receiver polls APIs whose bodies differ structurally, not merely in field
// names. Each walks two pages of a cursor and asserts the exact bytes sent.

// TestIntegration_PostTemplate_CrowdStrike covers a flat body whose cursor sits
// at the top level and must be absent on the first request.
func TestIntegration_PostTemplate_CrowdStrike(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "status:'new'", body["filter"])
		// limit must arrive as a JSON number, not a quoted string.
		require.Equal(t, float64(2), body["limit"])

		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch body["after"] {
		case nil:
			_, _ = w.Write([]byte(`{"resources":[{"id":"1"},{"id":"2"}],"meta":{"pagination":{"after":"cursor-2"}}}`))
		case "cursor-2":
			_, _ = w.Write([]byte(`{"resources":[{"id":"3"},{"id":"4"}],"meta":{"pagination":{"after":"cursor-3"}}}`))
		default:
			_, _ = w.Write([]byte(`{"resources":[],"meta":{"pagination":{"after":""}}}`))
		}
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		Method:        methodPOST,
		ResponseField: "resources",
		AuthMode:      authModeNone,
		RequestBody: `{
			"filter": "status:'new'",
			"limit": {{ .Limit }}{{ if .Cursor }},
			"after": "{{ .Cursor }}"{{ end }}
		}`,
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				LimitFieldName:      "",
				Limit:               2,
				NextOffsetFieldName: "meta.pagination.after",
			},
		},
		MinPollInterval:   100 * time.Millisecond,
		MaxPollInterval:   time.Second,
		BackoffMultiplier: 2.0,
		ClientConfig:      confighttp.ClientConfig{},
	}
	require.NoError(t, cfg.Validate())

	sink := new(consumertest.LogsSink)
	rcvr, err := newRESTAPILogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, rcvr.Start(context.Background(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, rcvr.Shutdown(context.Background())) }()

	require.Eventually(t, func() bool { return logRecordCount(sink) >= 4 }, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(bodies), 2)
	require.NotContains(t, bodies[0], "after", "the first request must omit the cursor entirely")
	require.Equal(t, "cursor-2", bodies[1]["after"])
}

// TestIntegration_PostTemplate_Datadog covers a nested body: the cursor and page
// size live under "page", the query under "filter".
func TestIntegration_PostTemplate_Datadog(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		filter, ok := body["filter"].(map[string]any)
		require.True(t, ok, "filter should be a nested object")
		require.Equal(t, "env:prod status:error", filter["query"])

		page, ok := body["page"].(map[string]any)
		require.True(t, ok, "page should be a nested object")
		require.Equal(t, float64(2), page["limit"])

		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if page["cursor"] == nil {
			_, _ = w.Write([]byte(`{"data":[{"id":"1"},{"id":"2"}],"meta":{"page":{"after":"dd-cursor-2"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page":{"after":""}}}`))
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		Method:        methodPOST,
		ResponseField: "data",
		AuthMode:      authModeNone,
		RequestBody: `{
			"filter": {"query": "env:prod status:error"},
			"sort": "-timestamp",
			"page": {
				"limit": {{ .Limit }}{{ if .Cursor }},
				"cursor": "{{ .Cursor }}"{{ end }}
			}
		}`,
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				Limit:               2,
				NextOffsetFieldName: "meta.page.after",
			},
		},
		MinPollInterval:   100 * time.Millisecond,
		MaxPollInterval:   time.Second,
		BackoffMultiplier: 2.0,
		ClientConfig:      confighttp.ClientConfig{},
	}
	require.NoError(t, cfg.Validate())

	sink := new(consumertest.LogsSink)
	rcvr, err := newRESTAPILogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, rcvr.Start(context.Background(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, rcvr.Shutdown(context.Background())) }()

	require.Eventually(t, func() bool { return logRecordCount(sink) >= 2 }, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(bodies), 2)
	require.Equal(t, "dd-cursor-2", bodies[1]["page"].(map[string]any)["cursor"])
}

// TestIntegration_PostTemplate_Mimecast covers a body that nests pagination
// under meta.pagination and wraps the query in a "data" array — the shape no
// top-level or dotted-path injection could reach.
func TestIntegration_PostTemplate_Mimecast(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		meta, ok := body["meta"].(map[string]any)
		require.True(t, ok)
		pagination, ok := meta["pagination"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, float64(2), pagination["pageSize"])

		data, ok := body["data"].([]any)
		require.True(t, ok, "data should be a JSON array")
		require.Len(t, data, 1)
		require.Equal(t, "2025-01-01T00:00:00Z", data[0].(map[string]any)["start"])

		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if pagination["pageToken"] == nil {
			_, _ = w.Write([]byte(`{"data":[{"id":"1"},{"id":"2"}],"meta":{"pagination":{"next":"mc-token-2"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{"pagination":{"next":""}}}`))
	}))
	defer server.Close()

	cfg := &Config{
		URL:                server.URL,
		Method:             methodPOST,
		ResponseField:      "data",
		AuthMode:           authModeNone,
		StartTimeParamName: "",
		StartTimeValue:     "2025-01-01T00:00:00Z",
		RequestBody: `{
			"meta": {"pagination": {
				"pageSize": {{ .Limit }}{{ if .Cursor }},
				"pageToken": "{{ .Cursor }}"{{ end }}
			}},
			"data": [{"start": "{{ .StartTime }}", "query": "attachment"}]
		}`,
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				Limit:               2,
				NextOffsetFieldName: "meta.pagination.next",
			},
		},
		MinPollInterval:   100 * time.Millisecond,
		MaxPollInterval:   time.Second,
		BackoffMultiplier: 2.0,
		ClientConfig:      confighttp.ClientConfig{},
	}
	require.NoError(t, cfg.Validate())

	sink := new(consumertest.LogsSink)
	rcvr, err := newRESTAPILogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, rcvr.Start(context.Background(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, rcvr.Shutdown(context.Background())) }()

	require.Eventually(t, func() bool { return logRecordCount(sink) >= 2 }, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(bodies), 2)
	secondPagination := bodies[1]["meta"].(map[string]any)["pagination"].(map[string]any)
	require.Equal(t, "mc-token-2", secondPagination["pageToken"])
}

// TestIntegration_PostTemplate_Metrics covers the metrics poll loop, which is a
// hand-copy of the logs one.
func TestIntegration_PostTemplate_Metrics(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		require.Equal(t, http.MethodPost, r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "cpu", body["metric_filter"])

		w.Header().Set("Content-Type", "application/json")
		if body["after"] == nil {
			_, _ = w.Write([]byte(`{"resources":[{"name":"cpu.usage","value":42},{"name":"cpu.idle","value":58}],"meta":{"after":"m-2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"resources":[],"meta":{"after":""}}`))
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		Method:        methodPOST,
		ResponseField: "resources",
		AuthMode:      authModeNone,
		RequestBody: `{"metric_filter": "cpu", "limit": {{ .Limit }}` +
			`{{ if .Cursor }}, "after": "{{ .Cursor }}"{{ end }}}`,
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				Limit:               2,
				NextOffsetFieldName: "meta.after",
			},
		},
		Metrics:           MetricsConfig{NameField: "name"},
		MinPollInterval:   100 * time.Millisecond,
		MaxPollInterval:   time.Second,
		BackoffMultiplier: 2.0,
		ClientConfig:      confighttp.ClientConfig{},
	}
	require.NoError(t, cfg.Validate())

	sink := new(consumertest.MetricsSink)
	rcvr, err := newRESTAPIMetricsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, rcvr.Start(context.Background(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, rcvr.Shutdown(context.Background())) }()

	require.Eventually(t, func() bool { return len(sink.AllMetrics()) > 0 }, 5*time.Second, 10*time.Millisecond)
	require.GreaterOrEqual(t, requestCount.Load(), int32(2))
}
