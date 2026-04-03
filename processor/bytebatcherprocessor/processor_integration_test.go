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

package bytebatcherprocessor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// TestSizeTriggeredFlush verifies that flushing occurs when byte threshold is exceeded.
func TestSizeTriggeredFlush(t *testing.T) {
	cfg := &Config{
		FlushInterval: 10 * time.Second, // Long interval
		Bytes:         500,                // Small threshold (~72 bytes per metric, need 7-8 to trigger)
	}
	logger := zap.NewNop()
	sink := &consumertest.MetricsSink{}

	proc := newMetricsProcessor(cfg, logger, nil, func() batch[pmetric.Metrics] {
		return newBatchMetrics(sink, nil)
	})

	ctx := context.Background()
	require.NoError(t, proc.Start(ctx, nil))
	defer proc.Shutdown(ctx)

	// Create a small metric
	createMetric := func() pmetric.Metrics {
		md := pmetric.NewMetrics()
		rm := md.ResourceMetrics().AppendEmpty()
		rm.Resource().Attributes().PutStr("host", "test")
		m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
		m.SetName("test.metric")
		dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
		dp.Attributes().PutStr("instance", "test")
		dp.SetIntValue(42)
		return md
	}

	// Queue items until size threshold is exceeded
	for i := 0; i < 8; i++ {
		_, _ = proc.processMetrics(ctx, createMetric())
	}

	// Should have triggered size-based flush by now
	require.Eventually(t, func() bool {
		return len(sink.AllMetrics()) > 0
	}, 2*time.Second, 100*time.Millisecond, "size-triggered flush should occur")
}

// TestIntervalTriggeredFlush verifies that flushing occurs on the interval timer.
func TestIntervalTriggeredFlush(t *testing.T) {
	cfg := &Config{
		FlushInterval: 500 * time.Millisecond,
		Bytes:         10 * 1024 * 1024, // Large threshold
	}
	logger := zap.NewNop()
	sink := &consumertest.LogsSink{}

	proc := newLogsProcessor(cfg, logger, nil, func() batch[plog.Logs] {
		return newBatchLogs(sink, nil)
	})

	ctx := context.Background()
	require.NoError(t, proc.Start(ctx, nil))
	defer proc.Shutdown(ctx)

	// Create a small log
	createLog := func() plog.Logs {
		ld := plog.NewLogs()
		rl := ld.ResourceLogs().AppendEmpty()
		rl.Resource().Attributes().PutStr("host", "test")
		l := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		l.Body().SetStr("test log")
		l.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		return ld
	}

	// Send a single log (won't trigger size flush)
	_, _ = proc.processLogs(ctx, createLog())
	require.Equal(t, 0, len(sink.AllLogs()), "should not flush immediately")

	// Wait for interval to trigger
	require.Eventually(t, func() bool {
		return len(sink.AllLogs()) > 0
	}, 2*time.Second, 100*time.Millisecond, "interval-triggered flush should occur")
}

// TestShutdownFlushesRemaining verifies that shutdown flushes remaining items.
func TestShutdownFlushesRemaining(t *testing.T) {
	cfg := &Config{
		FlushInterval: 10 * time.Second, // Long interval
		Bytes:         10 * 1024 * 1024,  // Large threshold
	}
	logger := zap.NewNop()
	sink := &consumertest.TracesSink{}

	proc := newTracesProcessor(cfg, logger, nil, func() batch[ptrace.Traces] {
		return newBatchTraces(sink, nil)
	})

	ctx := context.Background()
	require.NoError(t, proc.Start(ctx, nil))

	// Send a trace (won't trigger size or interval flush)
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("host", "test")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("test.span")
	_, _ = proc.processTraces(ctx, td)

	// Should not have flushed yet
	require.Equal(t, 0, len(sink.AllTraces()), "should not flush yet")

	// Shutdown should flush remaining items
	require.NoError(t, proc.Shutdown(ctx))
	require.Equal(t, 1, len(sink.AllTraces()), "shutdown should flush remaining items")
}

// TestMultipleBatchesAreAccumulated verifies that multiple items are merged in a single flush.
func TestMultipleBatchesAreAccumulated(t *testing.T) {
	cfg := &Config{
		FlushInterval: 500 * time.Millisecond,
		Bytes:         10 * 1024 * 1024, // Large threshold
	}
	logger := zap.NewNop()
	sink := &consumertest.MetricsSink{}

	proc := newMetricsProcessor(cfg, logger, nil, func() batch[pmetric.Metrics] {
		return newBatchMetrics(sink, nil)
	})

	ctx := context.Background()
	require.NoError(t, proc.Start(ctx, nil))
	defer proc.Shutdown(ctx)

	// Create a metric
	createMetric := func() pmetric.Metrics {
		md := pmetric.NewMetrics()
		rm := md.ResourceMetrics().AppendEmpty()
		m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
		m.SetName("test.metric")
		dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
		dp.SetIntValue(42)
		return md
	}

	// Send 5 metrics over time
	for i := 0; i < 5; i++ {
		_, _ = proc.processMetrics(ctx, createMetric())
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for flush
	require.Eventually(t, func() bool {
		return len(sink.AllMetrics()) > 0
	}, 2*time.Second, 100*time.Millisecond)

	// All 5 should be merged into at least 1 batch
	assert.GreaterOrEqual(t, len(sink.AllMetrics()), 1, "should have at least 1 batch")

	// Total data points across all batches should be 5
	totalDataPoints := 0
	for _, batch := range sink.AllMetrics() {
		totalDataPoints += batch.DataPointCount()
	}
	assert.Equal(t, 5, totalDataPoints, "all 5 metrics should be present")
}

// TestConcurrentProcessing verifies thread-safety with concurrent calls.
func TestConcurrentProcessing(t *testing.T) {
	cfg := &Config{
		FlushInterval: 1 * time.Second,
		Bytes:         100 * 1024 * 1024, // High threshold
	}
	logger := zap.NewNop()
	sink := &consumertest.MetricsSink{}

	proc := newMetricsProcessor(cfg, logger, nil, func() batch[pmetric.Metrics] {
		return newBatchMetrics(sink, nil)
	})

	ctx := context.Background()
	require.NoError(t, proc.Start(ctx, nil))
	defer proc.Shutdown(ctx)

	// Create a metric
	createMetric := func() pmetric.Metrics {
		md := pmetric.NewMetrics()
		rm := md.ResourceMetrics().AppendEmpty()
		m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
		m.SetName("test.metric")
		dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
		dp.SetIntValue(1)
		return md
	}

	// Send metrics concurrently
	done := make(chan struct{})
	numGoroutines := 10
	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < 10; j++ {
				_, _ = proc.processMetrics(ctx, createMetric())
			}
			done <- struct{}{}
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Wait for flush
	require.Eventually(t, func() bool {
		return len(sink.AllMetrics()) > 0
	}, 2*time.Second, 100*time.Millisecond)

	// Verify all 100 metrics made it through
	totalDataPoints := 0
	for _, batch := range sink.AllMetrics() {
		totalDataPoints += batch.DataPointCount()
	}
	assert.Equal(t, numGoroutines*10, totalDataPoints, "all metrics should be processed")
}
