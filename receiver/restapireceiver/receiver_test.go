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
	"strconv"
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
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestInitializePagination_NoCheckpoint_UsesConfig(t *testing.T) {
	// When no checkpoint is loaded (paginationState is nil), initializePagination
	// should create a fresh state from config, including the start_time_value.
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			StartTimeParamName: "since",
			StartTimeValue:     "2026-03-31T12:00:00Z",
			Pagination: PaginationConfig{
				Mode: paginationModeTimestamp,
				Timestamp: TimestampPagination{
					PageSize: 50,
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
	// specifies a start_time_value, the config value should be used. This prevents
	// the zero timestamp from causing the receiver to fetch all historical data.
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			StartTimeParamName: "since",
			StartTimeValue:     "2026-03-31T12:00:00Z",
			Pagination: PaginationConfig{
				Mode: paginationModeTimestamp,
				Timestamp: TimestampPagination{
					PageSize: 50,
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
		"zero checkpoint timestamp should be replaced by configured start_time_value")
	// Other checkpoint fields should be preserved
	require.Equal(t, 100, b.paginationState.PageSize,
		"non-timestamp checkpoint fields should be preserved")
}

func TestInitializePagination_CheckpointWithValidTimestamp_PreservesCheckpoint(t *testing.T) {
	// When a checkpoint has a valid (non-zero) timestamp, it represents real polling
	// progress and should be preserved, even if start_time_value is configured.
	checkpointTime := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			StartTimeParamName: "since",
			StartTimeValue:     "2026-03-31T12:00:00Z",
			Pagination: PaginationConfig{
				Mode:      paginationModeTimestamp,
				Timestamp: TimestampPagination{},
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
		"valid checkpoint timestamp should be preserved over configured start_time_value")
	require.True(t, b.paginationState.TimestampFromData,
		"TimestampFromData flag should be preserved")
}

func TestInitializePagination_CheckpointWithZeroTimestamp_NoStartTimeValue(t *testing.T) {
	// When a checkpoint has a zero timestamp and no start_time_value is configured,
	// the zero timestamp should remain — this is the "fetch from beginning" behavior.
	b := &baseReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			StartTimeParamName: "since",
			Pagination: PaginationConfig{
				Mode: paginationModeTimestamp,
				Timestamp: TimestampPagination{
					PageSize: 50,
				},
			},
		},
		paginationState: &paginationState{
			PageSize: 100,
		},
	}

	b.initializePagination()

	require.True(t, b.paginationState.CurrentTimestamp.IsZero(),
		"zero timestamp should remain when no start_time_value is configured")
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
		URL:                "https://api.example.com/events",
		StartTimeParamName: "since",
		StartTimeValue:     "2026-01-01T00:00:00Z",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				PageSize: 100,
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
		URL:                "https://api.example.com/events",
		StartTimeParamName: "since",
		StartTimeValue:     "2026-03-31T12:00:00Z",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				PageSize: 100,
			},
		},
	}
	baseFingerprint := configFingerprint(baseCfg)

	// Same config should produce the same fingerprint (stability check)
	require.Equal(t, baseFingerprint, configFingerprint(baseCfg))

	// Changing URL should change the fingerprint
	differentURL := &Config{
		URL:                "https://api.example.com/audit-logs",
		StartTimeParamName: baseCfg.StartTimeParamName,
		StartTimeValue:     baseCfg.StartTimeValue,
		Pagination:         baseCfg.Pagination,
	}
	require.NotEqual(t, baseFingerprint, configFingerprint(differentURL),
		"different URL should produce different fingerprint")

	// Changing start_time_value should change the fingerprint
	differentTimestamp := &Config{
		URL:                baseCfg.URL,
		StartTimeParamName: "since",
		StartTimeValue:     "2026-01-01T00:00:00Z",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				PageSize: 100,
			},
		},
	}
	require.NotEqual(t, baseFingerprint, configFingerprint(differentTimestamp),
		"different start_time_value should produce different fingerprint")

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
		URL:                baseCfg.URL,
		StartTimeParamName: baseCfg.StartTimeParamName,
		StartTimeValue:     baseCfg.StartTimeValue,
		Pagination:         baseCfg.Pagination,
		MinPollInterval:    5 * time.Second,
		MaxPollInterval:    60 * time.Second,
	}
	require.Equal(t, baseFingerprint, configFingerprint(differentPollInterval),
		"different poll interval should not change fingerprint")
}

func TestLoadCheckpoint_InvalidatesOnConfigChange(t *testing.T) {
	// Simulates the user's scenario: receiver ran successfully with one config,
	// then the user changes start_time_value and restarts. The stored checkpoint
	// should be discarded because the config fingerprint has changed.

	oldCfg := &Config{
		URL:                "https://api.example.com/events",
		StartTimeParamName: "since",
		StartTimeValue:     "2026-04-01T00:00:00Z",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				PageSize: 100,
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

	// New config: user changed start_time_value to fetch older data
	newCfg := &Config{
		URL:                "https://api.example.com/events",
		StartTimeParamName: "since",
		StartTimeValue:     "2026-03-01T00:00:00Z",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				PageSize: 100,
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
		"after config change, fresh state should use the new start_time_value")
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
		URL:                "https://api.example.com/events",
		StartTimeParamName: "since",
		StartTimeValue:     "2026-01-01T00:00:00Z",
		Pagination: PaginationConfig{
			Mode:      paginationModeTimestamp,
			Timestamp: TimestampPagination{},
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

	// Poll until the receiver emits at least one batch, then shut down.
	require.Eventually(t, func() bool {
		return len(sink.AllLogs()) > 0
	}, 5*time.Second, 10*time.Millisecond)

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

	// Poll until the receiver emits at least one batch, then shut down.
	require.Eventually(t, func() bool {
		return len(sink.AllMetrics()) > 0
	}, 5*time.Second, 10*time.Millisecond)

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

	// Poll until all pages of the first cycle have been collected, then shut down.
	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 4
	}, 5*time.Second, 10*time.Millisecond)

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
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
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

	// Poll until the receiver has attempted at least two polls, proving it keeps
	// polling after a server error instead of crashing or stopping.
	require.Eventually(t, func() bool {
		return requestCount.Load() >= 2
	}, 5*time.Second, 10*time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Receiver should still be running (errors logged but don't crash)
	require.GreaterOrEqual(t, int(requestCount.Load()), 2)
}

func TestRESTAPILogsReceiver_EmptyResponse(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
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

	// Poll until at least one poll cycle has hit the endpoint, then shut down.
	require.Eventually(t, func() bool {
		return requestCount.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	// Empty responses should be handled gracefully
	// May or may not have logs depending on implementation
	require.GreaterOrEqual(t, int(requestCount.Load()), 1)
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

	// Poll until the nested field has yielded records, then shut down.
	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 2
	}, 5*time.Second, 10*time.Millisecond)

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

	// Poll until the deeply nested field has yielded records, then shut down.
	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 3
	}, 5*time.Second, 10*time.Millisecond)

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

func TestRESTAPILogsReceiver_ArrayIndexedResponseField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]any{
			"intervals": []map[string]any{
				{
					"readings": []map[string]any{
						{"id": "1", "message": "indexed 1"},
						{"id": "2", "message": "indexed 2"},
					},
				},
				{
					"readings": []map[string]any{
						{"id": "ignored", "message": "should not emit"},
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
		ResponseField: "intervals[0].readings",
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

	// Poll until intervals[0].readings has yielded its records, then shut down.
	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 2
	}, 5*time.Second, 10*time.Millisecond)

	err = receiver.Shutdown(ctx)
	require.NoError(t, err)

	allLogs := sink.AllLogs()
	require.Greater(t, len(allLogs), 0)

	totalRecords := 0
	for _, logs := range allLogs {
		totalRecords += logs.LogRecordCount()
	}
	// Exactly the 2 readings under intervals[0]; intervals[1] must not be emitted.
	require.GreaterOrEqual(t, totalRecords, 2)
	for _, logs := range allLogs {
		for i := 0; i < logs.ResourceLogs().Len(); i++ {
			rl := logs.ResourceLogs().At(i)
			for j := 0; j < rl.ScopeLogs().Len(); j++ {
				sl := rl.ScopeLogs().At(j)
				for k := 0; k < sl.LogRecords().Len(); k++ {
					body := sl.LogRecords().At(k).Body().AsString()
					require.NotContains(t, body, "should not emit")
				}
			}
		}
	}
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

	// Poll until more than one request has occurred (initial poll plus at least one
	// backoff cycle), then shut down.
	require.Eventually(t, func() bool {
		return requestCount.Load() > 1
	}, 5*time.Second, 10*time.Millisecond)

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

	// Poll until multiple requests have occurred and logs have arrived, then shut down.
	require.Eventually(t, func() bool {
		return requestCount.Load() > 1 && len(sink.AllLogs()) > 0
	}, 5*time.Second, 10*time.Millisecond)

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

	// Poll until many requests have occurred (page limit hit each cycle keeps the
	// interval at min), then shut down.
	require.Eventually(t, func() bool {
		return requestCount.Load() > 4
	}, 5*time.Second, 10*time.Millisecond)

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

	// Poll until the NDJSON pages have yielded their records, then shut down.
	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 2
	}, 5*time.Second, 10*time.Millisecond)

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

	// Poll until both pages have yielded their records, then shut down.
	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 11 && requestCount.Load() >= 2
	}, 5*time.Second, 10*time.Millisecond)

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

	// Poll until both pages have yielded their records, then shut down.
	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 4
	}, 5*time.Second, 10*time.Millisecond)

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
		{
			name: "array index selects element",
			data: map[string]any{
				"intervals": []any{
					map[string]any{"readings": []any{"a", "b"}},
					map[string]any{"readings": []any{"c"}},
				},
			},
			path:     "intervals[0].readings",
			expected: []any{"a", "b"},
			found:    true,
		},
		{
			name: "array index then second index",
			data: map[string]any{
				"intervals": []any{
					map[string]any{"readings": []any{"a", "b"}},
					map[string]any{"readings": []any{"c"}},
				},
			},
			path:     "intervals[1].readings",
			expected: []any{"c"},
			found:    true,
		},
		{
			name: "chained indices on same segment",
			data: map[string]any{
				"matrix": []any{
					[]any{"a", "b"},
					[]any{"c", "d"},
				},
			},
			path:     "matrix[1][0]",
			expected: "c",
			found:    true,
		},
		{
			name: "terminal index selects single element",
			data: map[string]any{
				"data": []any{"first", "second"},
			},
			path:     "data[0]",
			expected: "first",
			found:    true,
		},
		{
			name: "index out of bounds returns not found",
			data: map[string]any{
				"intervals": []any{map[string]any{"readings": []any{"a"}}},
			},
			path:     "intervals[5].readings",
			expected: nil,
			found:    false,
		},
		{
			name: "indexing non-array returns not found",
			data: map[string]any{
				"field": "string value",
			},
			path:     "field[0]",
			expected: nil,
			found:    false,
		},
		{
			name: "negative index is rejected as malformed",
			data: map[string]any{
				"data": []any{"a", "b"},
			},
			path:     "data[-1]",
			expected: nil,
			found:    false,
		},
		{
			name: "missing closing bracket returns not found",
			data: map[string]any{
				"data": []any{"a"},
			},
			path:     "data[0",
			expected: nil,
			found:    false,
		},
		{
			name: "non-integer index returns not found",
			data: map[string]any{
				"data": []any{"a"},
			},
			path:     "data[abc]",
			expected: nil,
			found:    false,
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

// logRecordCount returns the total number of log records the sink has collected
// across all batches. It is used as an Eventually condition to wait for a poll
// cycle to deliver the expected records instead of sleeping a fixed duration.
func logRecordCount(sink *consumertest.LogsSink) int {
	total := 0
	for _, logs := range sink.AllLogs() {
		total += logs.LogRecordCount()
	}
	return total
}

// TestConfigFingerprint_UnchangedByPOSTFieldsWhenUnused stops a future edit to
// configFingerprint from silently invalidating every checkpoint in the field.
//
// Changing the expected digest discards every deployed checkpoint, making each
// receiver re-fetch from its configured start and emit duplicate data. Do not
// update it to make a test pass.
func TestConfigFingerprint_UnchangedByPOSTFieldsWhenUnused(t *testing.T) {
	cfg := &Config{
		URL:                "https://api.example.com/events",
		StartTimeParamName: "since",
		StartTimeValue:     "2026-03-31T12:00:00Z",
		Pagination: PaginationConfig{
			Mode:      paginationModeTimestamp,
			Timestamp: TimestampPagination{PageSize: 100},
		},
	}

	// sha256 of the pre-existing byte sequence:
	//   url \0 mode \0 start_time_value \0 end_time_value \0 starting_offset \0 starting_page
	const expected = "4c7dfc88eb97947ab68db7bea33617d560af8f4aaeaee784afedf509821e4a02"
	require.Equal(t, expected, configFingerprint(cfg))

	// An explicitly defaulted method must not perturb it either — Validate()
	// sets Method to "get" on every config, including ones that predate it.
	withDefaults := *cfg
	withDefaults.Method = methodGET
	require.Equal(t, expected, configFingerprint(&withDefaults))
}

func TestConfigFingerprint_POSTFields(t *testing.T) {
	baseCfg := &Config{
		URL:    "https://api.crowdstrike.com/alerts/combined/alerts/v1",
		Method: methodPOST,
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "after",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "meta.pagination.after",
			},
		},
		RequestBody: `{"filter":"status:'new'","sort":"created_timestamp|asc"}`,
	}
	baseFingerprint := configFingerprint(baseCfg)
	require.Equal(t, baseFingerprint, configFingerprint(baseCfg))

	// request_body IS query-defining: a different filter selects different
	// records, so a cursor from the old filter must not be replayed.
	differentFilter := *baseCfg
	differentFilter.RequestBody = `{"filter":"status:'closed'","sort":"created_timestamp|asc"}`
	require.NotEqual(t, baseFingerprint, configFingerprint(&differentFilter),
		"different request_body should produce different fingerprint")

	// offset_limit.limit is a throughput knob, like page_size and page_limit.
	differentLimit := *baseCfg
	differentLimit.Pagination.OffsetLimit.Limit = 500
	require.Equal(t, baseFingerprint, configFingerprint(&differentLimit),
		"offset_limit.limit should not affect the fingerprint")
}

func TestBuildAPIRequest(t *testing.T) {
	newReceiver := func(t *testing.T, cfg *Config) *baseReceiver {
		t.Helper()
		b := &baseReceiver{
			cfg:             cfg,
			logger:          zap.NewNop(),
			paginationState: newPaginationState(cfg),
		}
		require.NoError(t, b.initializeRequestBody())
		return b
	}

	t.Run("renders the body from the template", func(t *testing.T) {
		cfg := &Config{
			URL:         "https://api.example.com/alerts",
			Method:      methodPOST,
			RequestBody: `{"filter":"status:'new'","limit":{{ .Limit }}{{ if .Cursor }},"after":{{ json .Cursor }}{{ end }}}`,
			Pagination: PaginationConfig{
				Mode:        paginationModeOffsetLimit,
				OffsetLimit: OffsetLimitPagination{Limit: 100, NextOffsetFieldName: "meta.after"},
			},
		}
		r := newReceiver(t, cfg)

		// First page: the cursor guard suppresses the field entirely.
		req, err := r.buildAPIRequest()
		require.NoError(t, err)
		require.Equal(t, cfg.URL, req.URL)
		require.JSONEq(t, `{"filter":"status:'new'","limit":100}`, string(req.Body))

		// Continuation: the cursor appears.
		r.paginationState.CurrentOffsetToken = "cursor-1"
		req, err = r.buildAPIRequest()
		require.NoError(t, err)
		require.JSONEq(t, `{"filter":"status:'new'","limit":100,"after":"cursor-1"}`, string(req.Body))
	})

	t.Run("no template means no body", func(t *testing.T) {
		cfg := &Config{
			URL:        "https://api.example.com/data",
			Pagination: PaginationConfig{Mode: paginationModeNone},
		}
		req, err := newReceiver(t, cfg).buildAPIRequest()
		require.NoError(t, err)
		require.Empty(t, req.Body)
	})

	t.Run("query parameters are unaffected by the body", func(t *testing.T) {
		cfg := &Config{
			URL:         "https://api.example.com/data",
			Method:      methodPOST,
			RequestBody: `{"filter":"status:'new'"}`,
			Pagination: PaginationConfig{
				Mode: paginationModeOffsetLimit,
				OffsetLimit: OffsetLimitPagination{
					OffsetFieldName: "offset",
					LimitFieldName:  "limit",
					Limit:           25,
				},
			},
		}
		req, err := newReceiver(t, cfg).buildAPIRequest()
		require.NoError(t, err)
		require.Equal(t, "0", req.Query.Get("offset"))
		require.Equal(t, "25", req.Query.Get("limit"))
		require.JSONEq(t, `{"filter":"status:'new'"}`, string(req.Body))
	})

	t.Run("each request gets its own body", func(t *testing.T) {
		cfg := &Config{
			URL:         "https://api.example.com/alerts",
			Method:      methodPOST,
			RequestBody: `{"after":{{ json .Cursor }}}`,
			Pagination: PaginationConfig{
				Mode:        paginationModeOffsetLimit,
				OffsetLimit: OffsetLimitPagination{NextOffsetFieldName: "meta.after"},
			},
		}
		r := newReceiver(t, cfg)

		r.paginationState.CurrentOffsetToken = "cursor-1"
		first, err := r.buildAPIRequest()
		require.NoError(t, err)
		r.paginationState.CurrentOffsetToken = "cursor-2"
		second, err := r.buildAPIRequest()
		require.NoError(t, err)

		require.JSONEq(t, `{"after":"cursor-1"}`, string(first.Body))
		require.JSONEq(t, `{"after":"cursor-2"}`, string(second.Body))
	})
}

// TestReconcileCheckpointWithConfig_AppliesConfiguredLimit covers limit being
// excluded from configFingerprint while loadCheckpoint restores the whole state:
// without reconciliation a limit change would never take effect once the
// receiver has checkpointed.
func TestReconcileCheckpointWithConfig_AppliesConfiguredLimit(t *testing.T) {
	testCases := []struct {
		name            string
		configuredLimit int
		checkpointLimit int
		expected        int
	}{
		{name: "raised limit takes effect", configuredLimit: 100, checkpointLimit: 10, expected: 100},
		{name: "lowered limit takes effect", configuredLimit: 5, checkpointLimit: 10, expected: 5},
		{name: "unset limit falls back to the default", configuredLimit: 0, checkpointLimit: 250, expected: 10},
		{name: "matching limit is left alone", configuredLimit: 25, checkpointLimit: 25, expected: 25},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				URL: "https://api.example.com/alerts",
				Pagination: PaginationConfig{
					Mode: paginationModeOffsetLimit,
					OffsetLimit: OffsetLimitPagination{
						OffsetFieldName: "after",
						LimitFieldName:  "limit",
						Limit:           tc.configuredLimit,
					},
				},
			}
			b := &baseReceiver{
				cfg:    cfg,
				logger: zap.NewNop(),
				// A checkpoint restored by loadCheckpoint, carrying real progress.
				paginationState: &paginationState{Limit: tc.checkpointLimit, CurrentOffset: 30},
			}

			b.reconcileCheckpointWithConfig()

			require.Equal(t, tc.expected, b.paginationState.Limit)
			// Polling progress must survive reconciliation untouched.
			require.Equal(t, 30, b.paginationState.CurrentOffset)
			// The value sent and the full-page threshold stay the same variable.
			require.Equal(t, strconv.Itoa(tc.expected), buildPaginationParams(cfg, b.paginationState).Get("limit"))
		})
	}
}

// TestInitializePagination_ChangedLimitSurvivesCheckpointLoad walks the real
// path: checkpoint written with one limit, config changed, reload must use the
// new value.
func TestInitializePagination_ChangedLimitSurvivesCheckpointLoad(t *testing.T) {
	cfg := &Config{
		URL: "https://api.example.com/alerts",
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "after",
				LimitFieldName:  "limit",
				Limit:           100,
			},
		},
	}

	// Simulate loadCheckpoint having restored a state persisted when limit was unset.
	b := &baseReceiver{
		cfg:             cfg,
		logger:          zap.NewNop(),
		paginationState: &paginationState{Limit: 10, CurrentOffsetToken: "cursor-7"},
	}
	b.initializePagination()

	require.Equal(t, 100, b.paginationState.Limit)
	require.Equal(t, "cursor-7", b.paginationState.CurrentOffsetToken,
		"the stored cursor must be preserved")
}

// TestReconcileCheckpointWithConfig_AppliesConfiguredPageSize mirrors the limit
// case: page_size is excluded from configFingerprint, so a change to it must be
// re-applied over the value restored from a checkpoint.
func TestReconcileCheckpointWithConfig_AppliesConfiguredPageSize(t *testing.T) {
	testCases := []struct {
		name       string
		configured int
		checkpoint int
		expected   int
	}{
		{name: "raised page size takes effect", configured: 100, checkpoint: 20, expected: 100},
		{name: "lowered page size takes effect", configured: 5, checkpoint: 20, expected: 5},
		{name: "unset falls back to the default", configured: 0, checkpoint: 250, expected: 20},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				URL: "https://api.example.com/data",
				Pagination: PaginationConfig{
					Mode: paginationModePageSize,
					PageSize: PageSizePagination{
						PageNumFieldName:  "page",
						PageSizeFieldName: "size",
						PageSize:          tc.configured,
					},
				},
			}
			b := &baseReceiver{
				cfg:             cfg,
				logger:          zap.NewNop(),
				paginationState: &paginationState{PageSize: tc.checkpoint, CurrentPage: 7},
			}

			b.reconcileCheckpointWithConfig()

			require.Equal(t, tc.expected, b.paginationState.PageSize)
			// Polling progress survives reconciliation.
			require.Equal(t, 7, b.paginationState.CurrentPage)
		})
	}
}

func TestConfigFingerprint_PageSizeExcluded(t *testing.T) {
	cfg := &Config{
		URL: "https://api.example.com/data",
		Pagination: PaginationConfig{
			Mode:     paginationModePageSize,
			PageSize: PageSizePagination{PageNumFieldName: "page", PageSizeFieldName: "size"},
		},
	}
	base := configFingerprint(cfg)

	changed := *cfg
	changed.Pagination.PageSize.PageSize = 500
	require.Equal(t, base, configFingerprint(&changed),
		"page_size is a throughput knob and must not invalidate a checkpoint")
}

// memStorageClient is an in-memory storage.Client that records what the receiver
// persists. storage.NewNopClient discards writes, which would make any assertion
// about checkpointing vacuous.
type memStorageClient struct {
	mu   sync.Mutex
	data map[string][]byte
	sets int
}

func newMemStorageClient() *memStorageClient {
	return &memStorageClient{data: make(map[string][]byte)}
}

func (m *memStorageClient) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[key], nil
}

func (m *memStorageClient) Set(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	m.sets++
	return nil
}

func (m *memStorageClient) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memStorageClient) Batch(_ context.Context, _ ...*storage.Operation) error { return nil }

func (m *memStorageClient) Close(_ context.Context) error { return nil }

func (m *memStorageClient) setCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sets
}

// TestPoll_TailPreservesAndPersistsCursor is the end-to-end guarantee behind the
// cursor fix: a poll cycle that ends without a next token keeps the cursor it was
// using AND writes it to storage, so the next poll resumes rather than restarting
// the stream. Shutdown is deliberately not called — it saves a checkpoint of its
// own, which would hide whether the poll cycle persisted anything.
func TestPoll_TailPreservesAndPersistsCursor(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		// A tail response: data, but no next_cursor field at all.
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "1"}},
		}))
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		AuthMode:      authModeNone,
		ResponseField: "data",
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "cursor",
				NextOffsetFieldName: "next_cursor",
				OffsetType:          offsetTypeOpaque,
				Limit:               10,
			},
		},
		MinPollInterval:   10 * time.Second,
		MaxPollInterval:   5 * time.Minute,
		BackoffMultiplier: 2.0,
		ClientConfig:      confighttp.ClientConfig{},
	}

	sink := new(consumertest.LogsSink)
	r, err := newRESTAPILogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, r.initializeClient(ctx, componenttest.NewNopHost()))
	store := newMemStorageClient()
	r.storageClient = store
	r.initializePagination()

	// Stand in for a run that already holds a cursor from an earlier poll.
	r.paginationState.CurrentOffsetToken = "cursor-from-earlier-poll"

	_, err = r.poll(ctx)
	require.NoError(t, err)

	require.Equal(t, int64(1), requests.Load(), "a tail response must end the cycle after one page")
	require.Equal(t, "cursor-from-earlier-poll", r.paginationState.CurrentOffsetToken,
		"a tail response must not clear the in-memory cursor")

	// One page means the mid-loop save never ran, so any checkpoint present came
	// from the end-of-cycle save.
	require.Equal(t, 1, store.setCount(), "the end of a poll cycle must persist exactly one checkpoint")

	raw, err := store.Get(ctx, checkpointStorageKey)
	require.NoError(t, err)
	require.NotNil(t, raw, "the poll cycle must persist a checkpoint without waiting for shutdown")

	var saved checkpointData
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.Equal(t, "cursor-from-earlier-poll", saved.PaginationState.CurrentOffsetToken,
		"the persisted checkpoint must carry the cursor the next poll resumes from")
}

// TestMetricsPoll_TailPreservesAndPersistsCursor is the metrics-receiver twin of
// TestPoll_TailPreservesAndPersistsCursor. Both signals now share
// baseReceiver.poll, so this asserts the metrics receiver actually reaches that
// loop — that its constructor wired up a consumeFunc and that records flow
// through to the sink — rather than re-testing the pagination itself.
func TestMetricsPoll_TailPreservesAndPersistsCursor(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		// A tail response: data, but no next_cursor field at all.
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"name": "cpu.utilization", "value": 42.0}},
		}))
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		AuthMode:      authModeNone,
		ResponseField: "data",
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "cursor",
				NextOffsetFieldName: "next_cursor",
				OffsetType:          offsetTypeOpaque,
				Limit:               10,
			},
		},
		MinPollInterval:   10 * time.Second,
		MaxPollInterval:   5 * time.Minute,
		BackoffMultiplier: 2.0,
		ClientConfig:      confighttp.ClientConfig{},
		Metrics:           MetricsConfig{NameField: "name"},
	}

	sink := new(consumertest.MetricsSink)
	r, err := newRESTAPIMetricsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, r.initializeClient(ctx, componenttest.NewNopHost()))
	store := newMemStorageClient()
	r.storageClient = store
	r.initializePagination()

	// Stand in for a run that already holds a cursor from an earlier poll.
	r.paginationState.CurrentOffsetToken = "cursor-from-earlier-poll"

	result, err := r.poll(ctx)
	require.NoError(t, err)

	// Prove a real cycle ran rather than a no-op poll that would checkpoint anyway.
	require.Equal(t, int64(1), requests.Load(), "a tail response must end the cycle after one page")
	require.Equal(t, 1, result.recordCount)
	require.Len(t, sink.AllMetrics(), 1)

	require.Equal(t, "cursor-from-earlier-poll", r.paginationState.CurrentOffsetToken,
		"a tail response must not clear the in-memory cursor")
	require.Equal(t, 1, store.setCount(), "the end of a poll cycle must persist exactly one checkpoint")

	raw, err := store.Get(ctx, checkpointStorageKey)
	require.NoError(t, err)
	require.NotNil(t, raw, "the poll cycle must persist a checkpoint without waiting for shutdown")

	var saved checkpointData
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.Equal(t, "cursor-from-earlier-poll", saved.PaginationState.CurrentOffsetToken,
		"the persisted checkpoint must carry the cursor the next poll resumes from")
}

// TestRESTAPILogsReceiver_HasMoreDrainsBacklogInOneCycle is the end-to-end case
// pagination.has_more_field_name exists for: limit is deliberately set far above
// the page size the API actually returns, which under the count heuristic makes
// every page look partial and stops pagination after page one. With the API's
// own has_more honored, the whole backlog drains within a single poll cycle.
//
// The poll intervals are long enough that only the initial poll can run inside
// the assertion window, so three requests means three pages in one cycle rather
// than one page across three cycles.
func TestRESTAPILogsReceiver_HasMoreDrainsBacklogInOneCycle(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		cursor := r.URL.Query().Get("cursor")

		// Two records per page — well under the configured limit of 100.
		var response map[string]any
		switch cursor {
		case "":
			response = map[string]any{
				"data":        []map[string]any{{"id": "1"}, {"id": "2"}},
				"next_cursor": "cursor-2",
				"has_more":    true,
			}
		case "cursor-2":
			response = map[string]any{
				"data":        []map[string]any{{"id": "3"}, {"id": "4"}},
				"next_cursor": "cursor-3",
				"has_more":    true,
			}
		default:
			response = map[string]any{
				"data":        []map[string]any{{"id": "5"}, {"id": "6"}},
				"next_cursor": "cursor-4",
				"has_more":    false,
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &Config{
		URL:           server.URL,
		AuthMode:      authModeNone,
		ResponseField: "data",
		Pagination: PaginationConfig{
			Mode:             paginationModeOffsetLimit,
			HasMoreFieldName: "has_more",
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "cursor",
				LimitFieldName:      "limit",
				Limit:               100,
				NextOffsetFieldName: "next_cursor",
				OffsetType:          offsetTypeOpaque,
			},
		},
		MinPollInterval: 30 * time.Second,
		MaxPollInterval: 30 * time.Second,
		ClientConfig:    confighttp.ClientConfig{},
	}

	// Validate applies the production defaults, notably backoff_multiplier. Left
	// at its zero value the backoff would drive the poll interval to 0 and
	// busy-loop, making the request counts below a race.
	require.NoError(t, cfg.Validate())

	sink := new(consumertest.LogsSink)
	params := receivertest.NewNopSettings(metadata.Type)
	receiver, err := newRESTAPILogsReceiver(params, cfg, sink)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, receiver.Start(ctx, componenttest.NewNopHost()))
	defer func() {
		require.NoError(t, receiver.Shutdown(ctx))
	}()

	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 6
	}, 5*time.Second, 10*time.Millisecond)

	// Exactly three requests: the cycle followed has_more twice and stopped when
	// it went false, rather than paging on against a valid cursor.
	require.Equal(t, int32(3), requestCount.Load())
	require.Equal(t, 6, logRecordCount(sink))
}

// TestRESTAPILogsReceiver_HasMoreFromHeader covers has_more_field_name under
// response_source: header, where the value arrives as the string "true" and has
// to be injected alongside the cursor header for the pagination logic to see it.
func TestRESTAPILogsReceiver_HasMoreFromHeader(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		cursor := r.URL.Query().Get("cursor")

		w.Header().Set("Content-Type", "application/json")
		if cursor == "" {
			w.Header().Set("X-Next-Cursor", "page-2")
			w.Header().Set("X-Has-More", "true")
			json.NewEncoder(w).Encode([]map[string]any{{"id": "1"}, {"id": "2"}})
			return
		}

		w.Header().Set("X-Next-Cursor", "page-3")
		w.Header().Set("X-Has-More", "false")
		json.NewEncoder(w).Encode([]map[string]any{{"id": "3"}})
	}))
	defer server.Close()

	cfg := &Config{
		URL:      server.URL,
		AuthMode: authModeNone,
		Pagination: PaginationConfig{
			Mode:             paginationModeOffsetLimit,
			ResponseSource:   responseSourceHeader,
			HasMoreFieldName: "X-Has-More",
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "cursor",
				LimitFieldName:      "limit",
				Limit:               50,
				NextOffsetFieldName: "X-Next-Cursor",
				OffsetType:          offsetTypeOpaque,
			},
		},
		MinPollInterval: 30 * time.Second,
		MaxPollInterval: 30 * time.Second,
		ClientConfig:    confighttp.ClientConfig{},
	}

	// Validate applies the production defaults, notably backoff_multiplier. Left
	// at its zero value the backoff would drive the poll interval to 0 and
	// busy-loop, making the request counts below a race.
	require.NoError(t, cfg.Validate())

	sink := new(consumertest.LogsSink)
	params := receivertest.NewNopSettings(metadata.Type)
	receiver, err := newRESTAPILogsReceiver(params, cfg, sink)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, receiver.Start(ctx, componenttest.NewNopHost()))
	defer func() {
		require.NoError(t, receiver.Shutdown(ctx))
	}()

	require.Eventually(t, func() bool {
		return logRecordCount(sink) >= 3
	}, 5*time.Second, 10*time.Millisecond)

	// The string "true" in the header carried page one to page two; "false"
	// stopped it there, despite a short page under a limit of 50.
	require.Equal(t, int32(2), requestCount.Load())
	require.Equal(t, 3, logRecordCount(sink))
}

// TestPoll_PageLimitResetsEveryCycle guards against page_limit latching. The
// counter it is checked against is per cycle, so a receiver that stops on the
// limit must fetch the same number of pages next cycle and keep advancing,
// never re-request the page it stopped on.
func TestPoll_PageLimitResetsEveryCycle(t *testing.T) {
	const pageLimit = 2

	testCases := []struct {
		name       string
		pagination PaginationConfig
		pageParam  string // query parameter that identifies the requested page
	}{
		{
			name: "numeric offset_limit",
			pagination: PaginationConfig{
				Mode: paginationModeOffsetLimit,
				OffsetLimit: OffsetLimitPagination{
					OffsetFieldName: "offset",
					LimitFieldName:  "limit",
					Limit:           2,
				},
				PageLimit: pageLimit,
			},
			pageParam: "offset",
		},
		{
			name: "page_size",
			pagination: PaginationConfig{
				Mode: paginationModePageSize,
				PageSize: PageSizePagination{
					PageNumFieldName:  "page",
					PageSizeFieldName: "per_page",
					StartingPage:      1,
					PageSize:          2,
				},
				PageLimit: pageLimit,
			},
			pageParam: "page",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var requested []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page, err := strconv.Atoi(r.URL.Query().Get(tc.pageParam))
				require.NoError(t, err)
				mu.Lock()
				requested = append(requested, page)
				mu.Unlock()

				// Always a full page, so only page_limit ever ends a cycle.
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{{"id": "a"}, {"id": "b"}},
				}))
			}))
			defer server.Close()

			cfg := &Config{
				URL:               server.URL,
				AuthMode:          authModeNone,
				ResponseField:     "data",
				Pagination:        tc.pagination,
				MinPollInterval:   10 * time.Second,
				MaxPollInterval:   5 * time.Minute,
				BackoffMultiplier: 2.0,
				ClientConfig:      confighttp.ClientConfig{},
			}

			sink := new(consumertest.LogsSink)
			r, err := newRESTAPILogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
			require.NoError(t, err)

			ctx := context.Background()
			require.NoError(t, r.initializeClient(ctx, componenttest.NewNopHost()))
			r.storageClient = newMemStorageClient()
			r.initializePagination()

			pagesInCycle := func() []int {
				mu.Lock()
				defer mu.Unlock()
				pages := requested
				requested = nil
				return pages
			}

			result, err := r.poll(ctx)
			require.NoError(t, err)
			require.True(t, result.lastPageFull, "a cycle ended by page_limit must report a full last page")
			require.Equal(t, 0, r.paginationState.PagesFetched, "the page counter must be reset at the end of a cycle")
			first := pagesInCycle()
			require.Len(t, first, pageLimit, "page_limit caps the pages fetched in one cycle")

			result, err = r.poll(ctx)
			require.NoError(t, err)
			require.True(t, result.lastPageFull, "a cycle ended by page_limit must report a full last page")
			require.Equal(t, 0, r.paginationState.PagesFetched, "the page counter must be reset at the end of a cycle")
			second := pagesInCycle()

			require.Len(t, second, len(first), "every cycle gets the same page budget")
			require.Greater(t, second[0], first[len(first)-1],
				"the second cycle must resume past the page the first one stopped on, not re-request it")
			for i := 1; i < len(second); i++ {
				require.Greater(t, second[i], second[i-1], "pages within a cycle must advance")
			}
		})
	}
}

func TestCheckpoint_ExcludesPagesFetched(t *testing.T) {
	// PagesFetched is a per-cycle counter, so it must not be persisted: a
	// restart would otherwise resume with a partly or fully spent page budget.
	checkpoint := checkpointData{
		PaginationState: &paginationState{CurrentOffset: 40, PagesFetched: 5},
	}
	raw, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "pages_fetched")

	// A checkpoint written before the field was excluded is loaded with the
	// counter zeroed rather than carried forward.
	legacy := []byte(`{"pagination_state":{"current_offset":40,"pages_fetched":5}}`)
	var loaded checkpointData
	require.NoError(t, json.Unmarshal(legacy, &loaded))
	require.Equal(t, 40, loaded.PaginationState.CurrentOffset)
	require.Equal(t, 0, loaded.PaginationState.PagesFetched)
}

// TestExtractOriginals covers locating the record array in the undecoded body
// across the response shapes extractDataFromResponse handles.
func TestExtractOriginals(t *testing.T) {
	testCases := []struct {
		name          string
		body          string
		responseField string
		want          []string
	}{
		{
			name: "top-level array",
			body: `[{"id":"1"},{"id":"2"}]`,
			want: []string{`{"id":"1"}`, `{"id":"2"}`},
		},
		{
			name: "data field",
			body: `{"data":[{"id":"1"}],"next":"abc"}`,
			want: []string{`{"id":"1"}`},
		},
		{
			name:          "explicit response field",
			body:          `{"results":[{"id":"1"},{"id":"2"}]}`,
			responseField: "results",
			want:          []string{`{"id":"1"}`, `{"id":"2"}`},
		},
		{
			name:          "nested response field",
			body:          `{"response":{"data":[{"id":"1"}]}}`,
			responseField: "response.data",
			want:          []string{`{"id":"1"}`},
		},
		{
			name:          "response field with array index",
			body:          `{"intervals":[{"readings":[{"id":"1"}]}]}`,
			responseField: "intervals[0].readings",
			want:          []string{`{"id":"1"}`},
		},
		{
			name:          "non-object items are skipped, matching the parsed walk",
			body:          `{"results":[{"id":"1"},"scalar",42,{"id":"2"}]}`,
			responseField: "results",
			want:          []string{`{"id":"1"}`, `{"id":"2"}`},
		},
		{
			name:          "single array field is unambiguous without a response field",
			body:          `{"items":[{"id":"1"}],"count":1}`,
			responseField: "",
			want:          []string{`{"id":"1"}`},
		},
		{
			// Choosing between these means ranging over a Go map, which has no
			// defined order, so the two walks could disagree. Give up instead.
			name:          "ambiguous multiple arrays yield no originals",
			body:          `{"items":[{"id":"1"}],"others":[{"id":"2"}]}`,
			responseField: "",
			want:          nil,
		},
		{
			name:          "missing response field yields no originals",
			body:          `{"results":[{"id":"1"}]}`,
			responseField: "absent",
			want:          nil,
		},
		{
			name:          "response field that is not an array yields no originals",
			body:          `{"results":{"id":"1"}}`,
			responseField: "results",
			want:          nil,
		},
		{
			name: "empty body yields no originals",
			body: "   ",
			want: nil,
		},
		{
			name: "body that is not JSON yields no originals",
			body: `not json`,
			want: nil,
		},
		{
			name: "body that is a JSON scalar yields no originals",
			body: `42`,
			want: nil,
		},
		{
			name:          "malformed path segment yields no originals",
			body:          `{"results":[{"id":"1"}]}`,
			responseField: "results[abc]",
			want:          nil,
		},
		{
			name:          "unterminated bracket yields no originals",
			body:          `{"results":[{"id":"1"}]}`,
			responseField: "results[0",
			want:          nil,
		},
		{
			name:          "walking into a scalar yields no originals",
			body:          `{"results":"not an object"}`,
			responseField: "results.nested",
			want:          nil,
		},
		{
			name:          "indexing a non-array yields no originals",
			body:          `{"results":{"id":"1"}}`,
			responseField: "results[0]",
			want:          nil,
		},
		{
			name:          "index out of range yields no originals",
			body:          `{"intervals":[{"readings":[{"id":"1"}]}]}`,
			responseField: "intervals[9].readings",
			want:          nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractOriginals([]byte(tc.body), tc.responseField, zap.NewNop())

			if tc.want == nil {
				require.Nil(t, got)
				return
			}
			as := make([]string, 0, len(got))
			for _, b := range got {
				as = append(as, string(b))
			}
			require.Equal(t, tc.want, as)
		})
	}
}

// TestExtractOriginalsIsByteExact is why the receiver re-walks the body instead
// of re-encoding parsed records: re-marshaling a map[string]any reorders keys and
// pushes large integers through float64.
func TestExtractOriginalsIsByteExact(t *testing.T) {
	const body = `{"data":[{"z":1,"a":2,"id":12345678901234567890,"nested":{"b":1,"a":2}}]}`

	originals := extractOriginals([]byte(body), "data", zap.NewNop())
	require.Len(t, originals, 1)
	require.Equal(t, `{"z":1,"a":2,"id":12345678901234567890,"nested":{"b":1,"a":2}}`, string(originals[0]))

	// The same record after a parse/re-encode round trip differs.
	parsed := extractDataFromResponse(
		map[string]any{"data": []any{map[string]any{"z": 1.0, "a": 2.0}}}, "data", zap.NewNop())
	reencoded, err := json.Marshal(parsed[0])
	require.NoError(t, err)
	require.NotEqual(t, string(originals[0]), string(reencoded))
}

// TestExtractOriginalsAlignsWithParsedRecords pins the invariant the receiver
// relies on: originals[i] is the text data[i] was decoded from.
func TestExtractOriginalsAlignsWithParsedRecords(t *testing.T) {
	bodies := []struct {
		body          string
		responseField string
	}{
		{`[{"id":"1"},{"id":"2"},{"id":"3"}]`, ""},
		{`{"data":[{"id":"1"},{"id":"2"}]}`, ""},
		{`{"results":[{"id":"1"},"skipme",{"id":"2"}]}`, "results"},
		{`{"response":{"data":[{"id":"1"}]}}`, "response.data"},
		{`{"data":[]}`, ""},
	}

	for _, b := range bodies {
		t.Run(b.body, func(t *testing.T) {
			// Mirror FetchFullResponse, which wraps a top-level array as {"data": ...}.
			var decoded any
			require.NoError(t, json.Unmarshal([]byte(b.body), &decoded))
			response, ok := decoded.(map[string]any)
			if !ok {
				response = map[string]any{"data": decoded.([]any)}
			}

			parsed := extractDataFromResponse(response, b.responseField, zap.NewNop())
			originals := extractOriginals([]byte(b.body), b.responseField, zap.NewNop())

			require.Len(t, originals, len(parsed))
			for i := range parsed {
				var fromOriginal map[string]any
				require.NoError(t, json.Unmarshal(originals[i], &fromOriginal))
				require.Equal(t, parsed[i], fromOriginal)
			}
		})
	}
}

// TestPoll_BodyOptionsEndToEnd drives the real poll path for both response
// formats, proving the original text survives to the emitted records — including
// the key order and large integer a re-encode would destroy.
func TestPoll_BodyOptionsEndToEnd(t *testing.T) {
	const jsonRecord = `{"z":"last","id":12345678901234567890,"a":"first"}`

	testCases := []struct {
		name           string
		responseFormat ResponseFormat
		responseField  string
		payload        string
		contentType    string
	}{
		{
			name:           "json",
			responseFormat: responseFormatJSON,
			responseField:  "data",
			payload:        `{"data":[` + jsonRecord + `]}`,
			contentType:    "application/json",
		},
		{
			name:           "ndjson",
			responseFormat: responseFormatNDJSON,
			payload:        jsonRecord + "\n" + `{"done":true}`,
			contentType:    "application/x-ndjson",
		},
	}

	for _, tc := range testCases {
		t.Run(string(tc.name), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(tc.payload))
			}))
			defer server.Close()

			cfg := &Config{
				URL:                      server.URL,
				AuthMode:                 authModeNone,
				ResponseFormat:           tc.responseFormat,
				ResponseField:            tc.responseField,
				Raw:                      true,
				IncludeLogRecordOriginal: true,
				Pagination:               PaginationConfig{Mode: paginationModeNone},
				MinPollInterval:          10 * time.Second,
				MaxPollInterval:          5 * time.Minute,
				BackoffMultiplier:        2.0,
			}

			sink := new(consumertest.LogsSink)
			r, err := newRESTAPILogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
			require.NoError(t, err)

			ctx := context.Background()
			require.NoError(t, r.initializeClient(ctx, componenttest.NewNopHost()))
			r.storageClient = newMemStorageClient()
			r.initializePagination()

			result, err := r.poll(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, result.recordCount)
			require.Len(t, sink.AllLogs(), 1)

			record := sink.AllLogs()[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)

			require.Equal(t, pcommon.ValueTypeStr, record.Body().Type())
			require.Equal(t, jsonRecord, record.Body().Str(),
				"raw mode must emit the exact bytes the API returned")

			original, ok := record.Attributes().Get(logRecordOriginalAttribute)
			require.True(t, ok)
			require.Equal(t, jsonRecord, original.Str())
		})
	}
}

// TestPoll_BodyOptionsDisabledByDefault pins that a config that does not opt in
// behaves exactly as it did before the options existed.
func TestPoll_BodyOptionsDisabledByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"1"}]}`))
	}))
	defer server.Close()

	cfg := &Config{
		URL:               server.URL,
		AuthMode:          authModeNone,
		ResponseField:     "data",
		Pagination:        PaginationConfig{Mode: paginationModeNone},
		MinPollInterval:   10 * time.Second,
		MaxPollInterval:   5 * time.Minute,
		BackoffMultiplier: 2.0,
	}

	sink := new(consumertest.LogsSink)
	r, err := newRESTAPILogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, r.initializeClient(ctx, componenttest.NewNopHost()))
	r.storageClient = newMemStorageClient()
	r.initializePagination()

	_, err = r.poll(ctx)
	require.NoError(t, err)
	require.Len(t, sink.AllLogs(), 1)

	record := sink.AllLogs()[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	require.Equal(t, pcommon.ValueTypeMap, record.Body().Type())
	require.Equal(t, "1", record.Body().Map().AsRaw()["id"])
	require.Equal(t, 0, record.Attributes().Len())
}

// stubRESTAPIClient returns canned responses so tests can drive fetchDataPage
// with data the real client would never produce.
type stubRESTAPIClient struct {
	data      []map[string]any
	originals [][]byte
	metadata  map[string]any
}

func (s *stubRESTAPIClient) FetchFullResponse(context.Context, apiRequest) (map[string]any, []byte, http.Header, error) {
	return s.metadata, nil, http.Header{}, nil
}

func (s *stubRESTAPIClient) FetchNDJSON(context.Context, apiRequest, bool) ([]map[string]any, [][]byte, map[string]any, http.Header, error) {
	return s.data, s.originals, s.metadata, http.Header{}, nil
}

func (s *stubRESTAPIClient) Shutdown() error { return nil }

// TestFetchDataPage_DropsMisalignedOriginals covers the guard in fetchDataPage.
// Records and original text come from two separate walks of the response, and
// pairing them when the counts disagree would attach one record's text to
// another. No real response reaches this state — with response_field set both
// walks follow the same path, and an ambiguous array yields nil originals rather
// than a short slice — so a stub client supplies the mismatch directly.
func TestFetchDataPage_DropsMisalignedOriginals(t *testing.T) {
	testCases := []struct {
		name          string
		data          []map[string]any
		originals     [][]byte
		wantOriginals bool
	}{
		{
			name:          "aligned originals are kept",
			data:          []map[string]any{{"id": "1"}, {"id": "2"}},
			originals:     [][]byte{[]byte(`{"id":"1"}`), []byte(`{"id":"2"}`)},
			wantOriginals: true,
		},
		{
			name:      "fewer originals than records",
			data:      []map[string]any{{"id": "1"}, {"id": "2"}, {"id": "3"}},
			originals: [][]byte{[]byte(`{"id":"1"}`), []byte(`{"id":"2"}`)},
		},
		{
			name:      "more originals than records",
			data:      []map[string]any{{"id": "1"}},
			originals: [][]byte{[]byte(`{"id":"1"}`), []byte(`{"id":"2"}`)},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)

			cfg := &Config{
				URL:               "https://api.example.com/data",
				AuthMode:          authModeNone,
				ResponseFormat:    responseFormatNDJSON,
				Raw:               true,
				Pagination:        PaginationConfig{Mode: paginationModeNone},
				MinPollInterval:   10 * time.Second,
				MaxPollInterval:   5 * time.Minute,
				BackoffMultiplier: 2.0,
			}

			b := &baseReceiver{
				cfg:    cfg,
				logger: zap.New(core),
				client: &stubRESTAPIClient{data: tc.data, originals: tc.originals},
			}

			_, data, originals, err := b.fetchDataPage(context.Background(), apiRequest{URL: cfg.URL})
			require.NoError(t, err)
			require.Equal(t, tc.data, data, "the records themselves must survive either way")

			if tc.wantOriginals {
				require.Equal(t, tc.originals, originals)
				require.Zero(t, logs.Len(), "an aligned page must not warn")
				return
			}

			require.Nil(t, originals, "a mismatched page must drop the originals entirely")
			require.Equal(t, 1, logs.Len(), "dropping the originals must be reported")
			require.Contains(t, logs.All()[0].Message, "does not line up")
		})
	}
}

// TestConvertJSONToLogs_DroppedOriginalsFallBack pairs with the guard above: once
// fetchDataPage drops the originals, records fall back to the parsed body rather
// than silently losing the option.
func TestConvertJSONToLogs_DroppedOriginalsFallBack(t *testing.T) {
	data := []map[string]any{{"id": "1"}, {"id": "2"}}
	cfg := &Config{Raw: true, IncludeLogRecordOriginal: true}

	logs := convertJSONToLogs(data, nil, cfg, zap.NewNop())

	records := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())
	for i := 0; i < records.Len(); i++ {
		require.Equal(t, pcommon.ValueTypeMap, records.At(i).Body().Type())
		require.Zero(t, records.At(i).Attributes().Len())
	}
}
