// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package chronicleexporter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/observiq/bindplane-otel-contrib/exporter/chronicleexporter/protos/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func newTestConfig(opts ...func(*Config)) *Config {
	cfg := &Config{
		CustomerID:      uuid.New().String(),
		MetricsInterval: time.Minute,
		Namespace:       "test-namespace",
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

func noopSend(_ context.Context, _ *api.BatchCreateEventsRequest) error {
	return nil
}

// telemetryWithResource builds a TelemetrySettings backed by an observable logger and
// a resource pre-populated with the provided attributes. Returns the logs observer so
// tests can assert on emitted log records.
func telemetryWithResource(attrs map[string]string) (component.TelemetrySettings, *observer.ObservedLogs) {
	set := componenttest.NewNopTelemetrySettings()
	core, logs := observer.New(zap.ErrorLevel)
	set.Logger = zap.New(core)
	res := pcommon.NewResource()
	for k, v := range attrs {
		res.Attributes().PutStr(k, v)
	}
	set.Resource = res
	return set, logs
}

func TestNewMetricsReporter(t *testing.T) {
	t.Run("invalid customer ID", func(t *testing.T) {
		cfg := newTestConfig(func(c *Config) { c.CustomerID = "not-a-uuid" })

		mr, err := newMetricsReporter(cfg, componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
		require.Error(t, err)
		require.Nil(t, mr)
		assert.Contains(t, err.Error(), "parse customer ID")
	})

	t.Run("populates source from config", func(t *testing.T) {
		cfg := newTestConfig()
		customerUUID, _ := uuid.Parse(cfg.CustomerID)

		mr, err := newMetricsReporter(cfg, componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
		require.NoError(t, err)

		assert.Equal(t, cfg.MetricsInterval, mr.interval)
		assert.Equal(t, "test-exporter", mr.exporterID)
		assert.Equal(t, customerUUID[:], mr.source.CustomerId)
		assert.Equal(t, cfg.Namespace, mr.source.Namespace)
		assert.NotNil(t, mr.startTime)
		assert.NotNil(t, mr.agentStats)
		assert.Len(t, mr.agentStats.ExporterStats, 1)
		assert.Equal(t, "test-exporter", mr.agentStats.ExporterStats[0].Name)
	})

	t.Run("collector ID is derived from license type", func(t *testing.T) {
		cases := []struct {
			licenseType string
			expected    []byte
		}{
			{"", defaultCollectorID[:]},
			{"unknown", defaultCollectorID[:]},
			{licenseTypeGoogle, googleCollectorID[:]},
			{licenseTypeGoogleEnterprise, googleEnterpriseCollectorID[:]},
			{licenseTypeEnterprise, enterpriseCollectorID[:]},
		}
		for _, tc := range cases {
			t.Run(tc.licenseType, func(t *testing.T) {
				cfg := newTestConfig(func(c *Config) { c.LicenseType = tc.licenseType })

				mr, err := newMetricsReporter(cfg, componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
				require.NoError(t, err)
				assert.Equal(t, tc.expected, mr.source.CollectorId)
			})
		}
	})

	t.Run("agent ID uses service.instance.id when valid", func(t *testing.T) {
		serviceID := uuid.New()
		set, _ := telemetryWithResource(map[string]string{
			string(semconv.ServiceInstanceIDKey): serviceID.String(),
		})

		mr, err := newMetricsReporter(newTestConfig(), set, "test-exporter", noopSend)
		require.NoError(t, err)
		assert.Equal(t, serviceID[:], mr.agentID)
		assert.Equal(t, serviceID[:], mr.agentStats.AgentId)
	})

	t.Run("agent ID falls back to random when service.instance.id is invalid", func(t *testing.T) {
		set, logs := telemetryWithResource(map[string]string{
			string(semconv.ServiceInstanceIDKey): "not-a-uuid",
		})

		mr, err := newMetricsReporter(newTestConfig(), set, "test-exporter", noopSend)
		require.NoError(t, err)
		require.Len(t, mr.agentID, 16)

		errLogs := logs.FilterMessageSnippet("Failed to parse service instance ID").All()
		require.Len(t, errLogs, 1)
	})

	t.Run("agent ID is random when service.instance.id is absent", func(t *testing.T) {
		mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
		require.NoError(t, err)
		require.Len(t, mr.agentID, 16)
	})
}

func TestMetricsReporterResetWindow(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
	require.NoError(t, err)

	mr.recordSent(10)
	mr.recordDropped(5)

	newWindow := timestamppb.Now()
	mr.resetWindow(newWindow)

	assert.Equal(t, newWindow, mr.agentStats.WindowStartTime)
	assert.Equal(t, mr.startTime, mr.agentStats.StartTime)
	assert.Equal(t, mr.agentID, mr.agentStats.AgentId)
	require.Len(t, mr.agentStats.ExporterStats, 1)
	assert.Equal(t, "test-exporter", mr.agentStats.ExporterStats[0].Name)
	assert.Equal(t, int64(0), mr.agentStats.ExporterStats[0].AcceptedSpans)
	assert.Equal(t, int64(0), mr.agentStats.ExporterStats[0].RefusedSpans)
}

func TestMetricsReporterRecordSent(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
	require.NoError(t, err)

	mr.recordSent(5)
	assert.Equal(t, int64(5), mr.agentStats.ExporterStats[0].AcceptedSpans)
	assert.NotNil(t, mr.agentStats.LastSuccessfulUploadTime)

	mr.recordSent(3)
	assert.Equal(t, int64(8), mr.agentStats.ExporterStats[0].AcceptedSpans)
}

func TestMetricsReporterRecordSentEmptyExporterStats(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
	require.NoError(t, err)

	mr.agentStats.ExporterStats = nil

	mr.recordSent(5)
	require.Len(t, mr.agentStats.ExporterStats, 1)
	assert.Equal(t, "test-exporter", mr.agentStats.ExporterStats[0].Name)
	assert.Equal(t, int64(5), mr.agentStats.ExporterStats[0].AcceptedSpans)
}

func TestMetricsReporterRecordDropped(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
	require.NoError(t, err)

	mr.recordDropped(7)
	assert.Equal(t, int64(7), mr.agentStats.ExporterStats[0].RefusedSpans)

	mr.recordDropped(2)
	assert.Equal(t, int64(9), mr.agentStats.ExporterStats[0].RefusedSpans)
}

func TestMetricsReporterRecordDroppedEmptyExporterStats(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
	require.NoError(t, err)

	mr.agentStats.ExporterStats = nil

	mr.recordDropped(3)
	require.Len(t, mr.agentStats.ExporterStats, 1)
	assert.Equal(t, "test-exporter", mr.agentStats.ExporterStats[0].Name)
	assert.Equal(t, int64(3), mr.agentStats.ExporterStats[0].RefusedSpans)
}

func TestMetricsReporterBuildRequest(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
	require.NoError(t, err)

	mr.recordSent(10)
	mr.recordDropped(2)

	req := mr.buildRequest()
	require.NotNil(t, req)
	require.NotNil(t, req.Batch)

	batch := req.Batch
	assert.Len(t, batch.Id, 16)
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

func TestMetricsReporterBuildRequestUniqueBatchIDs(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
	require.NoError(t, err)

	first := mr.buildRequest().Batch.Id
	second := mr.buildRequest().Batch.Id
	assert.NotEqual(t, first, second)
}

func TestMetricsReporterCollectHostMetrics(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
	require.NoError(t, err)

	require.NoError(t, mr.collectHostMetrics())
	firstCPU := mr.agentStats.ProcessCpuSeconds
	firstRSS := mr.agentStats.ProcessMemoryRss

	assert.GreaterOrEqual(t, firstCPU, int64(0))
	assert.Greater(t, firstRSS, int64(0))
	assert.GreaterOrEqual(t, mr.agentStats.ProcessUptime, int64(0))

	require.NoError(t, mr.collectHostMetrics())
	assert.GreaterOrEqual(t, mr.agentStats.ProcessCpuSeconds, firstCPU)
	assert.Greater(t, mr.agentStats.ProcessMemoryRss, int64(0))
}

func TestMetricsReporterStartResetsWindowOnSuccess(t *testing.T) {
	cfg := newTestConfig(func(c *Config) { c.MetricsInterval = 20 * time.Millisecond })

	var (
		mu       sync.Mutex
		captured []*api.BatchCreateEventsRequest
	)
	send := func(_ context.Context, req *api.BatchCreateEventsRequest) error {
		mu.Lock()
		captured = append(captured, req)
		mu.Unlock()
		return nil
	}

	mr, err := newMetricsReporter(cfg, componenttest.NewNopTelemetrySettings(), "test-exporter", send)
	require.NoError(t, err)

	initialWindow := mr.agentStats.WindowStartTime

	mr.start()
	defer mr.shutdown()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(captured) >= 1
	}, 2*time.Second, 10*time.Millisecond, "expected at least one send call")

	require.Eventually(t, func() bool {
		mr.mutex.Lock()
		defer mr.mutex.Unlock()
		return mr.agentStats.WindowStartTime.AsTime().After(initialWindow.AsTime())
	}, 2*time.Second, 10*time.Millisecond, "window should advance after a successful send")
}

func TestMetricsReporterStartKeepsWindowOnSendError(t *testing.T) {
	cfg := newTestConfig(func(c *Config) { c.MetricsInterval = 20 * time.Millisecond })

	var (
		mu        sync.Mutex
		sendCount int
	)
	send := func(_ context.Context, _ *api.BatchCreateEventsRequest) error {
		mu.Lock()
		sendCount++
		mu.Unlock()
		return errors.New("send failed")
	}

	mr, err := newMetricsReporter(cfg, componenttest.NewNopTelemetrySettings(), "test-exporter", send)
	require.NoError(t, err)

	initialWindow := mr.agentStats.WindowStartTime

	mr.recordSent(7)
	mr.recordDropped(3)

	mr.start()
	defer mr.shutdown()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return sendCount >= 2
	}, 2*time.Second, 10*time.Millisecond)

	mr.mutex.Lock()
	defer mr.mutex.Unlock()
	assert.Equal(t, initialWindow, mr.agentStats.WindowStartTime, "window should not advance when send fails")
	assert.Equal(t, int64(7), mr.agentStats.ExporterStats[0].AcceptedSpans, "counters should be preserved across failed sends")
	assert.Equal(t, int64(3), mr.agentStats.ExporterStats[0].RefusedSpans)
}

func TestMetricsReporterShutdownWithoutStart(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
	require.NoError(t, err)

	mr.shutdown()
}

func TestMetricsReporterConcurrency(t *testing.T) {
	mr, err := newMetricsReporter(newTestConfig(), componenttest.NewNopTelemetrySettings(), "test-exporter", noopSend)
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
