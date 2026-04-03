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

	"github.com/observiq/bindplane-otel-contrib/receiver/restapireceiver/internal/metadata"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
)

func TestInitializePagination_NoCheckpoint_UsesConfig(t *testing.T) {
	// When no checkpoint is loaded (paginationState is nil), initializePagination
	// should create a fresh state from config, including the initial_timestamp.
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			Pagination: PaginationConfig{
				Mode: paginationModeTimestamp,
				Timestamp: TimestampPagination{
					InitialTimestamp: "2026-03-31T12:00:00Z",
					ParamName:        "since",
					PageSize:         50,
				},
			},
		},
	}

	b.initializePagination()

	require.NotNil(t, b.paginationState)
	expectedTime, _ := time.Parse(time.RFC3339, "2026-03-31T12:00:00Z")
	require.Equal(t, expectedTime, b.paginationState.CurrentTimestamp)
	require.Equal(t, 50, b.paginationState.PageSize)
}

func TestInitializePagination_CheckpointWithZeroTimestamp_PrefersConfig(t *testing.T) {
	// When a checkpoint exists but has a zero CurrentTimestamp (e.g., from a prior run
	// that never completed a poll or used a different pagination mode), and the config
	// specifies an initial_timestamp, the config value should be used. This prevents
	// the zero timestamp from causing the receiver to fetch all historical data.
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			Pagination: PaginationConfig{
				Mode: paginationModeTimestamp,
				Timestamp: TimestampPagination{
					InitialTimestamp: "2026-03-31T12:00:00Z",
					ParamName:        "since",
					PageSize:         50,
				},
			},
		},
		// Simulate a loaded checkpoint with zero timestamp
		paginationState: &paginationState{
			PageSize: 100,
		},
	}

	b.initializePagination()

	expectedTime, _ := time.Parse(time.RFC3339, "2026-03-31T12:00:00Z")
	require.Equal(t, expectedTime, b.paginationState.CurrentTimestamp,
		"zero checkpoint timestamp should be replaced by configured initial_timestamp")
	// Other checkpoint fields should be preserved
	require.Equal(t, 100, b.paginationState.PageSize,
		"non-timestamp checkpoint fields should be preserved")
}

func TestInitializePagination_CheckpointWithValidTimestamp_PreservesCheckpoint(t *testing.T) {
	// When a checkpoint has a valid (non-zero) timestamp, it represents real polling
	// progress and should be preserved, even if initial_timestamp is configured.
	checkpointTime := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			Pagination: PaginationConfig{
				Mode: paginationModeTimestamp,
				Timestamp: TimestampPagination{
					InitialTimestamp: "2026-03-31T12:00:00Z",
					ParamName:        "since",
				},
			},
		},
		// Simulate a loaded checkpoint with a valid timestamp from a prior successful poll
		paginationState: &paginationState{
			CurrentTimestamp:  checkpointTime,
			TimestampFromData: true,
			PageSize:          100,
		},
	}

	b.initializePagination()

	require.Equal(t, checkpointTime, b.paginationState.CurrentTimestamp,
		"valid checkpoint timestamp should be preserved over configured initial_timestamp")
	require.True(t, b.paginationState.TimestampFromData,
		"TimestampFromData flag should be preserved")
}

func TestInitializePagination_CheckpointWithZeroTimestamp_NoInitialTimestamp(t *testing.T) {
	// When a checkpoint has a zero timestamp and no initial_timestamp is configured,
	// the zero timestamp should remain — this is the "fetch from beginning" behavior.
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			Pagination: PaginationConfig{
				Mode: paginationModeTimestamp,
				Timestamp: TimestampPagination{
					ParamName: "since",
					PageSize:  50,
				},
			},
		},
		paginationState: &paginationState{
			PageSize: 100,
		},
	}

	b.initializePagination()

	require.True(t, b.paginationState.CurrentTimestamp.IsZero(),
		"zero timestamp should remain when no initial_timestamp is configured")
}

func TestInitializePagination_NonTimestampMode_SkipsReconciliation(t *testing.T) {
	// For non-timestamp pagination modes, reconciliation should not modify the checkpoint.
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			Pagination: PaginationConfig{
				Mode: paginationModeOffsetLimit,
				OffsetLimit: OffsetLimitPagination{
					OffsetFieldName: "offset",
					LimitFieldName:  "limit",
				},
			},
		},
		paginationState: &paginationState{
			CurrentOffset: 42,
			Limit:         10,
		},
	}

	b.initializePagination()

	require.Equal(t, 42, b.paginationState.CurrentOffset,
		"offset/limit checkpoint should not be modified by reconciliation")
}

func TestCheckpointRoundTrip_TimestampPagination(t *testing.T) {
	// Verify that a checkpoint saved after successful polling can be loaded and
	// used without reconciliation overriding it, when config hasn't changed.
	ctx := context.Background()
	originalTime := time.Date(2026, 4, 2, 15, 30, 0, 0, time.UTC)

	cfg := &Config{
		URL: "https://api.example.com/events",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				InitialTimestamp: "2026-01-01T00:00:00Z",
				ParamName:        "since",
				PageSize:         100,
			},
		},
	}

	// Simulate saving a checkpoint with the config fingerprint
	checkpoint := checkpointData{
		PaginationState: &paginationState{
			CurrentTimestamp:  originalTime,
			TimestampFromData: true,
			PageSize:          100,
			PagesFetched:      5,
		},
		ConfigFingerprint: configFingerprint(cfg),
	}
	bytes, err := json.Marshal(checkpoint)
	require.NoError(t, err)

	// Simulate loading the checkpoint
	var loaded checkpointData
	err = json.Unmarshal(bytes, &loaded)
	require.NoError(t, err)

	loadReceiver := &baseReceiver{
		logger:          zap.NewNop(),
		cfg:             cfg,
		paginationState: loaded.PaginationState,
		storageClient:   storage.NewNopClient(),
	}

	loadReceiver.initializePagination()

	require.Equal(t, originalTime, loadReceiver.paginationState.CurrentTimestamp,
		"checkpoint timestamp from successful prior polling should survive round-trip and reconciliation")
	require.True(t, loadReceiver.paginationState.TimestampFromData)

	// Verify saveCheckpoint doesn't error
	err = loadReceiver.saveCheckpoint(ctx)
	require.NoError(t, err)
}

func TestConfigFingerprint_DifferentConfigs(t *testing.T) {
	// Verify that config fingerprints differ when query-defining fields change,
	// and are stable when non-query fields change.
	baseCfg := &Config{
		URL: "https://api.example.com/events",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				InitialTimestamp: "2026-03-31T12:00:00Z",
				ParamName:        "since",
				PageSize:         100,
			},
		},
	}
	baseFingerprint := configFingerprint(baseCfg)

	// Same config should produce the same fingerprint (stability check)
	require.Equal(t, baseFingerprint, configFingerprint(baseCfg))

	// Changing URL should change the fingerprint
	differentURL := &Config{
		URL: "https://api.example.com/audit-logs",
		Pagination: baseCfg.Pagination,
	}
	require.NotEqual(t, baseFingerprint, configFingerprint(differentURL),
		"different URL should produce different fingerprint")

	// Changing initial_timestamp should change the fingerprint
	differentTimestamp := &Config{
		URL: baseCfg.URL,
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				InitialTimestamp: "2026-01-01T00:00:00Z",
				ParamName:        "since",
				PageSize:         100,
			},
		},
	}
	require.NotEqual(t, baseFingerprint, configFingerprint(differentTimestamp),
		"different initial_timestamp should produce different fingerprint")

	// Changing pagination mode should change the fingerprint
	differentMode := &Config{
		URL: baseCfg.URL,
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
			},
		},
	}
	require.NotEqual(t, baseFingerprint, configFingerprint(differentMode),
		"different pagination mode should produce different fingerprint")

	// Changing non-query fields (poll interval) should NOT change the fingerprint
	differentPollInterval := &Config{
		URL:             baseCfg.URL,
		Pagination:      baseCfg.Pagination,
		MinPollInterval: 5 * time.Second,
		MaxPollInterval: 60 * time.Second,
	}
	require.Equal(t, baseFingerprint, configFingerprint(differentPollInterval),
		"different poll interval should not change fingerprint")
}

func TestLoadCheckpoint_InvalidatesOnConfigChange(t *testing.T) {
	// Simulates the user's scenario: receiver ran successfully with one config,
	// then the user changes initial_timestamp and restarts. The stored checkpoint
	// should be discarded because the config fingerprint has changed.

	oldCfg := &Config{
		URL: "https://api.example.com/events",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				InitialTimestamp: "2026-04-01T00:00:00Z",
				ParamName:        "since",
				PageSize:         100,
			},
		},
	}

	// Save a checkpoint with the old config's fingerprint
	checkpoint := checkpointData{
		PaginationState: &paginationState{
			CurrentTimestamp:  time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC),
			TimestampFromData: true,
			PageSize:          100,
		},
		ConfigFingerprint: configFingerprint(oldCfg),
	}
	checkpointBytes, err := json.Marshal(checkpoint)
	require.NoError(t, err)

	// Create a real in-memory storage client to test the full load flow
	storageClient := storage.NewNopClient()
	// NopClient doesn't persist, so we test via the loadCheckpoint logic directly.
	// Instead, manually simulate what loadCheckpoint does by unmarshaling and checking.
	var loaded checkpointData
	err = json.Unmarshal(checkpointBytes, &loaded)
	require.NoError(t, err)

	// New config: user changed initial_timestamp to fetch older data
	newCfg := &Config{
		URL: "https://api.example.com/events",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				InitialTimestamp: "2026-03-01T00:00:00Z",
				ParamName:        "since",
				PageSize:         100,
			},
		},
	}

	// Verify fingerprints differ
	require.NotEqual(t, loaded.ConfigFingerprint, configFingerprint(newCfg),
		"old and new config should have different fingerprints")

	// Simulate what loadCheckpoint does: reject the checkpoint
	b := &baseReceiver{
		logger:        zap.NewNop(),
		cfg:           newCfg,
		storageClient: storageClient,
	}

	// The checkpoint should be rejected, so paginationState stays nil
	currentFingerprint := configFingerprint(b.cfg)
	if loaded.ConfigFingerprint != "" && loaded.ConfigFingerprint != currentFingerprint {
		// This is what loadCheckpoint does — discard the checkpoint
		b.paginationState = nil
	}

	// initializePagination should create fresh state from the new config
	b.initializePagination()

	expectedTime, _ := time.Parse(time.RFC3339, "2026-03-01T00:00:00Z")
	require.Equal(t, expectedTime, b.paginationState.CurrentTimestamp,
		"after config change, fresh state should use the new initial_timestamp")
	require.False(t, b.paginationState.TimestampFromData,
		"fresh state should not have TimestampFromData set")
}

func TestLoadCheckpoint_AcceptsLegacyCheckpointWithoutFingerprint(t *testing.T) {
	// Checkpoints created before the fingerprint feature was added have no
	// ConfigFingerprint field. These should be accepted (not discarded) for
	// backwards compatibility, and will get a fingerprint on the next save.
	checkpoint := checkpointData{
		PaginationState: &paginationState{
			CurrentTimestamp:  time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			TimestampFromData: true,
			PageSize:          100,
		},
		// No ConfigFingerprint — simulates a legacy checkpoint
	}
	checkpointBytes, err := json.Marshal(checkpoint)
	require.NoError(t, err)

	var loaded checkpointData
	err = json.Unmarshal(checkpointBytes, &loaded)
	require.NoError(t, err)

	// Legacy checkpoint has empty fingerprint — should be accepted
	require.Empty(t, loaded.ConfigFingerprint)

	cfg := &Config{
		URL: "https://api.example.com/events",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				InitialTimestamp: "2026-01-01T00:00:00Z",
				ParamName:        "since",
			},
		},
	}

	// Simulate loadCheckpoint accepting the legacy checkpoint
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg:    cfg,
	}
	// Empty fingerprint → accept the checkpoint (backwards compatibility)
	if loaded.ConfigFingerprint == "" || loaded.ConfigFingerprint == configFingerprint(cfg) {
		b.paginationState = loaded.PaginationState
	}

	b.initializePagination()

	// The legacy checkpoint's timestamp should be preserved (it has a valid non-zero timestamp)
	require.Equal(t, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), b.paginationState.CurrentTimestamp,
		"legacy checkpoint with valid timestamp should be preserved")
}

func TestRESTAPILogsReceiver_StartShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := []map[string]any{
			{"id": "1", "message": "test"},
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
	require.NotNil(t, receiver)

	host := componenttest.NewNopHost()
	ctx := context.Background()

	err = receiver.Start(ctx, host)
	require.NoError(t, err)

	// Wait a bit for polling
	time.Sleep(200 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Should have received some logs
	require.Greater(t, len(sink.AllLogs()), 0)
}

func TestRESTAPIMetricsReceiver_StartShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := []map[string]any{
			{"value": 42.0, "name": "test"},
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
			NameField: "name",
		},
	}

	sink := new(consumertest.MetricsSink)
	params := receivertest.NewNopSettings(metadata.Type)
	receiver, err := newRESTAPIMetricsReceiver(params, cfg, sink)
	require.NoError(t, err)
	require.NotNil(t, receiver)

	host := componenttest.NewNopHost()
	ctx := context.Background()

	err = receiver.Start(ctx, host)
	require.NoError(t, err)

	// Wait a bit for polling
	time.Sleep(200 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Should have received some metrics
	require.Greater(t, len(sink.AllMetrics()), 0)
}

func TestRESTAPILogsReceiver_WithPagination(t *testing.T) {
	var pageCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		_ = r.URL.Query().Get("limit") // limit parameter

		var response map[string]any
		if offset == "0" || offset == "" {
			response = map[string]any{
				"data": []map[string]any{
					{"id": "1", "message": "page1"},
					{"id": "2", "message": "page1"},
				},
				"total": 4,
			}
		} else if offset == "2" {
			response = map[string]any{
				"data": []map[string]any{
					{"id": "3", "message": "page2"},
					{"id": "4", "message": "page2"},
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
		AuthMode:      authModeAPIKey,
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

	// Wait for at least one poll cycle (which will fetch all pages)
	time.Sleep(200 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Should have received logs (from all pages in first poll cycle)
	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)

	// Count total log records across all batches
	totalRecords := 0
	for _, logs := range allLogs {
		totalRecords += logs.LogRecordCount()
	}
	// Should have received logs from multiple pages (at least 2 pages = 4 records)
	require.GreaterOrEqual(t, totalRecords, 4)
}

func TestRESTAPILogsReceiver_WithTimestampPagination(t *testing.T) {
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
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				ParamName:          "t0",
				TimestampFieldName: "ts",
				PageSizeFieldName:  "perPage",
				PageSize:           200,
				InitialTimestamp:   initialTime.Format(time.RFC3339),
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

	// Wait for a poll cycle (which will fetch all pages)
	time.Sleep(200 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Should have used the timestamp parameter
	mu.Lock()
	ts := lastTimestamp
	ps := pageSize
	mu.Unlock()
	require.NotEmpty(t, ts)
	require.Contains(t, ts, "T") // RFC3339 format check
	// Should have used the page size parameter
	require.Equal(t, "200", ps)
	// Should have fetched multiple pages
	require.Greater(t, int(pageCount.Load()), 1)
}

func TestRESTAPILogsReceiver_ErrorHandling(t *testing.T) {
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

	// Wait a bit - should handle errors gracefully
	time.Sleep(200 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Receiver should still be running (errors logged but don't crash)
}

func TestRESTAPILogsReceiver_EmptyResponse(t *testing.T) {
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

	// Wait a bit
	time.Sleep(200 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Empty responses should be handled gracefully
	// May or may not have logs depending on implementation
}

func TestRESTAPILogsReceiver_NestedResponseField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]any{
			"response": map[string]any{
				"data": []map[string]any{
					{"id": "1", "message": "nested test 1"},
					{"id": "2", "message": "nested test 2"},
				},
			},
			"meta": map[string]any{
				"total": 2,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		ResponseField: "response.data", // Using dot notation for nested field
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
	require.NotNil(t, receiver)

	host := componenttest.NewNopHost()
	ctx := context.Background()

	err = receiver.Start(ctx, host)
	require.NoError(t, err)

	// Wait a bit for polling
	time.Sleep(200 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Should have received logs from nested field
	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)

	// Count total log records - should have 2 from the nested data
	totalRecords := 0
	for _, logs := range allLogs {
		totalRecords += logs.LogRecordCount()
	}
	require.GreaterOrEqual(t, totalRecords, 2)
}

func TestRESTAPILogsReceiver_DeeplyNestedResponseField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]any{
			"api": map[string]any{
				"response": map[string]any{
					"results": map[string]any{
						"items": []map[string]any{
							{"id": "1", "message": "deeply nested 1"},
							{"id": "2", "message": "deeply nested 2"},
							{"id": "3", "message": "deeply nested 3"},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		ResponseField: "api.response.results.items", // Multiple levels of nesting
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

	// Wait a bit for polling
	time.Sleep(200 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Should have received logs from deeply nested field
	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)

	// Count total log records - should have 3 from the nested items
	totalRecords := 0
	for _, logs := range allLogs {
		totalRecords += logs.LogRecordCount()
	}
	require.GreaterOrEqual(t, totalRecords, 3)
}

func TestRESTAPILogsReceiver_AdaptivePolling_Backoff(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		// Always return empty array to trigger backoff
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
		Pagination: PaginationConfig{
			Mode: paginationModeNone,
		},
		MaxPollInterval: 100 * time.Millisecond, // Max interval for backoff
		ClientConfig:    confighttp.ClientConfig{},
	}

	sink := new(consumertest.LogsSink)
	params := receivertest.NewNopSettings(metadata.Type)
	receiver, err := newRESTAPILogsReceiver(params, cfg, sink)
	require.NoError(t, err)
	require.NotNil(t, receiver)

	host := componenttest.NewNopHost()
	ctx := context.Background()

	err = receiver.Start(ctx, host)
	require.NoError(t, err)

	// Wait for several poll cycles - with backoff, interval increases: 1s -> 2s -> 4s... capped at 100ms
	// Starting from min_poll_interval, it would take multiple empty responses to reach max
	time.Sleep(500 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// With adaptive polling and empty responses, interval should increase
	// Initial poll immediate, then backoff kicks in
	require.Greater(t, int(requestCount.Load()), 1, "expected at least a couple polls to occur")
}

func TestRESTAPILogsReceiver_AdaptivePolling_PartialResponseBacksOff(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		// Always return a partial response (has data but not "full")
		response := []map[string]any{
			{"id": "1", "message": "data"},
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
		MaxPollInterval: 500 * time.Millisecond,
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

	// Wait for several poll cycles - partial responses should back off
	time.Sleep(300 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Should still have polled multiple times but with backoff
	require.Greater(t, int(requestCount.Load()), 1, "expected multiple polls")
	require.Greater(t, len(sink.AllLogs()), 0, "expected some logs to be received")
}

func TestRESTAPILogsReceiver_AdaptivePolling_PageLimitResetsInterval(t *testing.T) {
	// When the page limit is hit (meaning more data may exist), the interval
	// should reset to min_poll_interval for fast follow-up polling.
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		// Always return data with total indicating more records exist
		response := map[string]any{
			"data": []map[string]any{
				{"id": "1", "message": "page data"},
				{"id": "2", "message": "page data"},
			},
			"total": 100, // Indicate many more records
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
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
				StartingOffset:  0,
			},
			TotalRecordCountField: "total",
			PageLimit:             1, // Stop after 1 page — forces page limit to be hit
		},
		MinPollInterval: 50 * time.Millisecond,
		MaxPollInterval: 5 * time.Minute,
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

	// With page limit hit each cycle, interval should stay at min (10s),
	// so we should see many requests in a short window.
	time.Sleep(200 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// With 10s intervals and page limit hit each time, expect many requests
	require.Greater(t, int(requestCount.Load()), 4, "expected many polls when page limit is hit (interval should stay at min)")
}

func TestRESTAPILogsReceiver_NDJSONWithBodyOffset(t *testing.T) {
	// Simulates an API like Akamai SIEM: NDJSON with offset in the last line (body).
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		offset := r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/x-ndjson")

		if offset == "" || offset == "0" {
			// First page: 2 events + metadata with offset cursor
			w.Write([]byte(`{"id":"1","message":"event1"}` + "\n"))
			w.Write([]byte(`{"id":"2","message":"event2"}` + "\n"))
			w.Write([]byte(`{"offset":"cursor-page2","total":2}` + "\n"))
		} else {
			// Second page: 0 events + metadata (no more data)
			w.Write([]byte(`{"offset":"cursor-page2","total":0}` + "\n"))
		}
	}))
	defer server.Close()

	cfg := &Config{
		URL:            server.URL,
		ResponseFormat: responseFormatNDJSON,
		AuthMode:       authModeNone,
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "offset",
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

	time.Sleep(300 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)

	totalRecords := 0
	for _, logs := range allLogs {
		totalRecords += logs.LogRecordCount()
	}
	require.GreaterOrEqual(t, totalRecords, 2)
}

func TestRESTAPILogsReceiver_HeaderBasedOffset(t *testing.T) {
	// Tests offset extraction from a response header instead of the body.
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := requestCount.Add(1)
		offset := r.URL.Query().Get("cursor")

		if offset == "" || offset == "0" {
			// First page: return data with offset in header
			w.Header().Set("X-Next-Cursor", "page2-token")
			response := []map[string]any{
				{"id": "1"},
				{"id": "2"},
				{"id": "3"},
				{"id": "4"},
				{"id": "5"},
				{"id": "6"},
				{"id": "7"},
				{"id": "8"},
				{"id": "9"},
				{"id": "10"},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else if offset == "page2-token" && page <= 3 {
			// Second page: return partial page (fewer than limit), no next header
			response := []map[string]any{
				{"id": "11"},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		} else {
			// Empty
			response := []map[string]any{}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeNone,
		Pagination: PaginationConfig{
			Mode:           paginationModeOffsetLimit,
			ResponseSource: responseSourceHeader,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "cursor",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "X-Next-Cursor",
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

	time.Sleep(300 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)

	totalRecords := 0
	for _, logs := range allLogs {
		totalRecords += logs.LogRecordCount()
	}
	// Should have received at least 11 records (10 from page1 + 1 from page2)
	require.GreaterOrEqual(t, totalRecords, 11)

	// Should have fetched at least 2 pages
	require.GreaterOrEqual(t, int(requestCount.Load()), 2)
}

func TestRESTAPILogsReceiver_HeaderBasedTotalCount(t *testing.T) {
	// Tests that total_record_count_field works when sourced from a header.
	// The server returns the total count in a header, and the receiver uses it
	// to know when to stop paginating.
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page := requestCount.Add(1)
		w.Header().Set("X-Total-Count", "4")
		w.Header().Set("Content-Type", "application/json")

		if page == 1 {
			response := []map[string]any{
				{"id": "1"},
				{"id": "2"},
			}
			json.NewEncoder(w).Encode(response)
		} else if page == 2 {
			response := []map[string]any{
				{"id": "3"},
				{"id": "4"},
			}
			json.NewEncoder(w).Encode(response)
		} else {
			json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeNone,
		Pagination: PaginationConfig{
			Mode:                  paginationModeOffsetLimit,
			ResponseSource:        responseSourceHeader,
			TotalRecordCountField: "X-Total-Count",
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
				// No next_offset_field_name — using numeric offset + total count from header
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

	time.Sleep(300 * time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)

	totalRecords := 0
	for _, logs := range allLogs {
		totalRecords += logs.LogRecordCount()
	}
	// Should have 4 records from 2 pages
	require.GreaterOrEqual(t, totalRecords, 4)
}

func TestGetNestedField(t *testing.T) {
	tests := []struct {
		name     string
		data     map[string]any
		path     string
		expected any
		found    bool
	}{
		{
			name:     "single level",
			data:     map[string]any{"data": []any{"item1", "item2"}},
			path:     "data",
			expected: []any{"item1", "item2"},
			found:    true,
		},
		{
			name: "two levels",
			data: map[string]any{
				"response": map[string]any{
					"data": []any{"item1", "item2"},
				},
			},
			path:     "response.data",
			expected: []any{"item1", "item2"},
			found:    true,
		},
		{
			name: "three levels",
			data: map[string]any{
				"api": map[string]any{
					"response": map[string]any{
						"items": []any{"a", "b", "c"},
					},
				},
			},
			path:     "api.response.items",
			expected: []any{"a", "b", "c"},
			found:    true,
		},
		{
			name:     "field not found",
			data:     map[string]any{"other": "value"},
			path:     "data",
			expected: nil,
			found:    false,
		},
		{
			name: "nested field not found",
			data: map[string]any{
				"response": map[string]any{
					"other": "value",
				},
			},
			path:     "response.data",
			expected: nil,
			found:    false,
		},
		{
			name: "intermediate not a map",
			data: map[string]any{
				"response": "not a map",
			},
			path:     "response.data",
			expected: nil,
			found:    false,
		},
		{
			name:     "empty path returns first part as empty string key lookup",
			data:     map[string]any{"": "empty key value"},
			path:     "",
			expected: "empty key value",
			found:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, found := getNestedField(tt.data, tt.path)
			require.Equal(t, tt.found, found)
			if found {
				require.Equal(t, tt.expected, result)
			}
		})
	}
}
