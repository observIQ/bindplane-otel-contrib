package googlesecopsexporter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/protos/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func validCustomerID() string {
	return uuid.New().String()
}

func newTestConfig() *Config {
	return &Config{
		CustomerID:      validCustomerID(),
		MetricsInterval: time.Minute,
		Namespace:       "test-namespace",
		CollectorID:     uuid.New().String(),
	}
}

func noopSend(_ context.Context, _ *api.BatchCreateEventsRequest) error {
	return nil
}

func TestNewMetricsReporter(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		cfg := newTestConfig()
		set := componenttest.NewNopTelemetrySettings()

		mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
		require.NoError(t, err)
		require.NotNil(t, mr)

		assert.Equal(t, cfg.MetricsInterval, mr.interval)
		assert.Equal(t, "test-exporter", mr.exporterID)
		assert.NotNil(t, mr.source)
		assert.Equal(t, cfg.Namespace, mr.source.Namespace)
		assert.Equal(t, []byte(cfg.CollectorID), mr.source.CollectorId)
		assert.NotNil(t, mr.startTime)
		assert.NotNil(t, mr.agentStats)
		assert.Len(t, mr.agentStats.ExporterStats, 1)
		assert.Equal(t, "test-exporter", mr.agentStats.ExporterStats[0].Name)
	})

	t.Run("invalid customer ID", func(t *testing.T) {
		cfg := newTestConfig()
		cfg.CustomerID = "not-a-uuid"
		set := componenttest.NewNopTelemetrySettings()

		mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
		require.Error(t, err)
		require.Nil(t, mr)
		assert.Contains(t, err.Error(), "parse customer ID")
	})

	t.Run("customer ID set as source", func(t *testing.T) {
		cfg := newTestConfig()
		customerUUID, _ := uuid.Parse(cfg.CustomerID)
		set := componenttest.NewNopTelemetrySettings()

		mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
		require.NoError(t, err)
		assert.Equal(t, customerUUID[:], mr.source.CustomerId)
	})
}

func TestMetricsReporterResetWindow(t *testing.T) {
	cfg := newTestConfig()
	set := componenttest.NewNopTelemetrySettings()

	mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
	require.NoError(t, err)

	// Record some stats first
	mr.recordSent(10)
	mr.recordDropped(5)
	assert.Equal(t, int64(10), mr.agentStats.ExporterStats[0].AcceptedSpans)
	assert.Equal(t, int64(5), mr.agentStats.ExporterStats[0].RefusedSpans)

	// Reset window should clear stats
	newWindow := timestamppb.Now()
	mr.resetWindow(newWindow)

	assert.Equal(t, newWindow, mr.agentStats.WindowStartTime)
	assert.Equal(t, mr.startTime, mr.agentStats.StartTime)
	assert.Equal(t, mr.agentID, mr.agentStats.AgentId)
	assert.Len(t, mr.agentStats.ExporterStats, 1)
	assert.Equal(t, "test-exporter", mr.agentStats.ExporterStats[0].Name)
	assert.Equal(t, int64(0), mr.agentStats.ExporterStats[0].AcceptedSpans)
	assert.Equal(t, int64(0), mr.agentStats.ExporterStats[0].RefusedSpans)
}

func TestMetricsReporterRecordSent(t *testing.T) {
	cfg := newTestConfig()
	set := componenttest.NewNopTelemetrySettings()

	mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
	require.NoError(t, err)

	mr.recordSent(5)
	assert.Equal(t, int64(5), mr.agentStats.ExporterStats[0].AcceptedSpans)
	assert.NotNil(t, mr.agentStats.LastSuccessfulUploadTime)

	mr.recordSent(3)
	assert.Equal(t, int64(8), mr.agentStats.ExporterStats[0].AcceptedSpans)
}

func TestMetricsReporterRecordSentEmptyExporterStats(t *testing.T) {
	cfg := newTestConfig()
	set := componenttest.NewNopTelemetrySettings()

	mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
	require.NoError(t, err)

	// Clear exporter stats to test the guard clause
	mr.agentStats.ExporterStats = nil

	mr.recordSent(5)
	require.Len(t, mr.agentStats.ExporterStats, 1)
	assert.Equal(t, "test-exporter", mr.agentStats.ExporterStats[0].Name)
	assert.Equal(t, int64(5), mr.agentStats.ExporterStats[0].AcceptedSpans)
}

func TestMetricsReporterRecordDropped(t *testing.T) {
	cfg := newTestConfig()
	set := componenttest.NewNopTelemetrySettings()

	mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
	require.NoError(t, err)

	mr.recordDropped(7)
	assert.Equal(t, int64(7), mr.agentStats.ExporterStats[0].RefusedSpans)

	mr.recordDropped(2)
	assert.Equal(t, int64(9), mr.agentStats.ExporterStats[0].RefusedSpans)
}

func TestMetricsReporterRecordDroppedEmptyExporterStats(t *testing.T) {
	cfg := newTestConfig()
	set := componenttest.NewNopTelemetrySettings()

	mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
	require.NoError(t, err)

	// Clear exporter stats to test the guard clause
	mr.agentStats.ExporterStats = nil

	mr.recordDropped(3)
	require.Len(t, mr.agentStats.ExporterStats, 1)
	assert.Equal(t, "test-exporter", mr.agentStats.ExporterStats[0].Name)
	assert.Equal(t, int64(3), mr.agentStats.ExporterStats[0].RefusedSpans)
}

func TestMetricsReporterBuildRequest(t *testing.T) {
	cfg := newTestConfig()
	set := componenttest.NewNopTelemetrySettings()

	mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
	require.NoError(t, err)

	mr.recordSent(10)
	mr.recordDropped(2)

	req := mr.buildRequest()
	require.NotNil(t, req)
	require.NotNil(t, req.Batch)

	batch := req.Batch
	assert.Len(t, batch.Id, 16) // UUID is 16 bytes
	assert.Equal(t, mr.source, batch.Source)
	assert.Equal(t, api.EventBatch_AGENT_STATS, batch.Type)
	assert.Equal(t, mr.startTime, batch.StartTime)

	require.Len(t, batch.Events, 1)
	event := batch.Events[0]
	assert.NotNil(t, event.Timestamp)
	assert.NotNil(t, event.CollectionTime)
	assert.Equal(t, mr.source, event.Source)

	agentStats := event.GetAgentStats()
	require.NotNil(t, agentStats)
	assert.Equal(t, int64(10), agentStats.ExporterStats[0].AcceptedSpans)
	assert.Equal(t, int64(2), agentStats.ExporterStats[0].RefusedSpans)
}

func TestMetricsReporterCollectHostMetrics(t *testing.T) {
	cfg := newTestConfig()
	set := componenttest.NewNopTelemetrySettings()

	mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
	require.NoError(t, err)

	err = mr.collectHostMetrics()
	require.NoError(t, err)

	assert.GreaterOrEqual(t, mr.agentStats.ProcessCpuSeconds, int64(0))
	assert.Greater(t, mr.agentStats.ProcessMemoryRss, int64(0))
	assert.Greater(t, mr.agentStats.ProcessUptime, int64(0))
}

func TestMetricsReporterStartAndShutdown(t *testing.T) {
	cfg := newTestConfig()
	cfg.MetricsInterval = 50 * time.Millisecond
	set := componenttest.NewNopTelemetrySettings()

	var mu sync.Mutex
	var sendCount int
	send := func(_ context.Context, _ *api.BatchCreateEventsRequest) error {
		mu.Lock()
		sendCount++
		mu.Unlock()
		return nil
	}

	mr, err := newMetricsReporter(cfg, set, "test-exporter", send)
	require.NoError(t, err)

	mr.start()

	// Wait long enough for at least one tick
	time.Sleep(200 * time.Millisecond)

	mr.shutdown()

	mu.Lock()
	count := sendCount
	mu.Unlock()
	assert.Greater(t, count, 0, "expected at least one send call")
}

func TestMetricsReporterShutdownWithoutStart(t *testing.T) {
	cfg := newTestConfig()
	set := componenttest.NewNopTelemetrySettings()

	mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
	require.NoError(t, err)

	// Should not panic when cancel is nil
	mr.shutdown()
}

func TestMetricsReporterStartSendError(t *testing.T) {
	cfg := newTestConfig()
	cfg.MetricsInterval = 50 * time.Millisecond
	set := componenttest.NewNopTelemetrySettings()

	var mu sync.Mutex
	var sendCount int
	send := func(_ context.Context, _ *api.BatchCreateEventsRequest) error {
		mu.Lock()
		sendCount++
		mu.Unlock()
		return errors.New("send failed")
	}

	mr, err := newMetricsReporter(cfg, set, "test-exporter", send)
	require.NoError(t, err)

	mr.start()
	time.Sleep(200 * time.Millisecond)
	mr.shutdown()

	mu.Lock()
	count := sendCount
	mu.Unlock()
	// Should still have attempted to send despite errors
	assert.Greater(t, count, 0)
}

func TestMetricsReporterConcurrency(t *testing.T) {
	cfg := newTestConfig()
	set := componenttest.NewNopTelemetrySettings()

	mr, err := newMetricsReporter(cfg, set, "test-exporter", noopSend)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			mr.recordSent(1)
		}()
		go func() {
			defer wg.Done()
			mr.recordDropped(1)
		}()
		go func() {
			defer wg.Done()
			_ = mr.buildRequest()
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(10), mr.agentStats.ExporterStats[0].AcceptedSpans)
	assert.Equal(t, int64(10), mr.agentStats.ExporterStats[0].RefusedSpans)
}
