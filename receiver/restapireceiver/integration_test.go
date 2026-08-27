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

// TestIntegration_PostCursorPaginationInBody is the end-to-end proof for
// POST-based polling: it walks two pages of an opaque cursor carried in the JSON
// request body, modeled on CrowdStrike's POST /alerts/combined/alerts/v1.
func TestIntegration_PostCursorPaginationInBody(t *testing.T) {
	var mu sync.Mutex
	var seenBodies []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		// The static request_body survives on every page...
		require.Equal(t, "status:'new'", body["filter"])
		require.Equal(t, "created_timestamp|asc", body["sort"])
		// ...and the generated limit arrives as a JSON number, not a string.
		require.Equal(t, float64(2), body["limit"])

		mu.Lock()
		seenBodies = append(seenBodies, body)
		mu.Unlock()

		after, hasAfter := body["after"]
		if hasAfter {
			// An opaque cursor must come back as a string, never coerced.
			require.IsType(t, "", after)
		}

		w.Header().Set("Content-Type", "application/json")
		switch after {
		case nil:
			_, _ = w.Write([]byte(`{
				"resources": [{"id": "1", "message": "alert 1"}, {"id": "2", "message": "alert 2"}],
				"meta": {"pagination": {"after": "cursor-page-2"}}
			}`))
		case "cursor-page-2":
			_, _ = w.Write([]byte(`{
				"resources": [{"id": "3", "message": "alert 3"}, {"id": "4", "message": "alert 4"}],
				"meta": {"pagination": {"after": "cursor-page-3"}}
			}`))
		default:
			// A partial page ends the walk.
			_, _ = w.Write([]byte(`{"resources": [], "meta": {"pagination": {"after": ""}}}`))
		}
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		Method:        methodPOST,
		ParamLocation: paramLocationBody,
		ResponseField: "resources",
		AuthMode:      authModeNone,
		RequestBody: map[string]any{
			"filter": "status:'new'",
			"sort":   "created_timestamp|asc",
		},
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "after",
				LimitFieldName:      "limit",
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
	defer func() {
		require.NoError(t, rcvr.Shutdown(context.Background()))
	}()

	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 4
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(seenBodies), 2)
	// The first request of the run carries no cursor; the second carries the
	// opaque token read from meta.pagination.after.
	require.NotContains(t, seenBodies[0], "after")
	require.Equal(t, "cursor-page-2", seenBodies[1]["after"])
}

// TestIntegration_PostCursorPaginationInBody_Metrics covers the metrics poll
// loop, which is a hand-copy of the logs one.
func TestIntegration_PostCursorPaginationInBody_Metrics(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		require.Equal(t, http.MethodPost, r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "cpu", body["metric_filter"])

		w.Header().Set("Content-Type", "application/json")
		if body["after"] == nil {
			_, _ = w.Write([]byte(`{
				"resources": [{"name": "cpu.usage", "value": 42}, {"name": "cpu.idle", "value": 58}],
				"meta": {"pagination": {"after": "cursor-page-2"}}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{"resources": [], "meta": {"pagination": {"after": ""}}}`))
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		Method:        methodPOST,
		ParamLocation: paramLocationBody,
		ResponseField: "resources",
		AuthMode:      authModeNone,
		RequestBody:   map[string]any{"metric_filter": "cpu"},
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "after",
				LimitFieldName:      "limit",
				Limit:               2,
				NextOffsetFieldName: "meta.pagination.after",
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
	defer func() {
		require.NoError(t, rcvr.Shutdown(context.Background()))
	}()

	require.Eventually(t, func() bool {
		return len(sink.AllMetrics()) > 0
	}, 5*time.Second, 10*time.Millisecond)

	require.GreaterOrEqual(t, requestCount.Load(), int32(2))
}

// TestIntegration_PostWithQueryParamLocation covers a POST that paginates via
// the query string while sending a static JSON body.
func TestIntegration_PostWithQueryParamLocation(t *testing.T) {
	var mu sync.Mutex
	var seenQueries []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		// Pagination goes in the query string, so the body holds only the static
		// request_body.
		require.Equal(t, map[string]any{"filter": "status:'new'"}, body)

		mu.Lock()
		seenQueries = append(seenQueries, r.URL.RawQuery)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "0" {
			_, _ = w.Write([]byte(`{"data": [{"id": "1"}, {"id": "2"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data": []}`))
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		Method:        methodPOST,
		ParamLocation: paramLocationQuery,
		ResponseField: "data",
		AuthMode:      authModeNone,
		RequestBody:   map[string]any{"filter": "status:'new'"},
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
				Limit:           2,
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
	defer func() {
		require.NoError(t, rcvr.Shutdown(context.Background()))
	}()

	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 2
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, seenQueries[0], "offset=0")
	require.Contains(t, seenQueries[0], "limit=2")
}
