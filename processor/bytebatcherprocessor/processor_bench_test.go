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

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// generateBenchMetrics creates a realistic metric payload for benchmarking.
func generateBenchMetrics() pmetric.Metrics {
	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("host.name", "bench-host")
	m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName("bench.metric")
	dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
	dp.Attributes().PutStr("instance", "test")
	dp.SetIntValue(42)
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	return metrics
}

// generateBenchLogs creates a realistic log payload for benchmarking.
func generateBenchLogs() plog.Logs {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("host.name", "bench-host")
	l := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	l.Body().SetStr("bench log message with some content")
	l.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	return logs
}

// generateBenchTraces creates a realistic trace payload for benchmarking.
func generateBenchTraces() ptrace.Traces {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("host.name", "bench-host")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.Attributes().PutStr("service.name", "bench-service")
	span.SetName("bench.operation")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now().Add(-1 * time.Second)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	return traces
}

// BenchmarkMetricsProcessorHotPath benchmarks the non-blocking hot path for metrics.
func BenchmarkMetricsProcessorHotPath(b *testing.B) {
	cfg := &Config{
		FlushInterval: 10 * time.Second,
		Bytes:         10 * 1024 * 1024, // 10MB threshold
	}
	logger := zap.NewNop()
	sink := &consumertest.MetricsSink{}

	proc := newMetricsProcessor(cfg, logger, nil, func() batch[pmetric.Metrics] {
		return newBatchMetrics(sink, nil)
	})

	ctx := context.Background()
	_ = proc.Start(ctx, nil)
	defer proc.Shutdown(ctx)

	md := generateBenchMetrics()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = proc.processMetrics(ctx, md)
	}
}

// BenchmarkLogsProcessorHotPath benchmarks the non-blocking hot path for logs.
func BenchmarkLogsProcessorHotPath(b *testing.B) {
	cfg := &Config{
		FlushInterval: 10 * time.Second,
		Bytes:         10 * 1024 * 1024, // 10MB threshold
	}
	logger := zap.NewNop()
	sink := &consumertest.LogsSink{}

	proc := newLogsProcessor(cfg, logger, nil, func() batch[plog.Logs] {
		return newBatchLogs(sink, nil)
	})

	ctx := context.Background()
	_ = proc.Start(ctx, nil)
	defer proc.Shutdown(ctx)

	ld := generateBenchLogs()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = proc.processLogs(ctx, ld)
	}
}

// BenchmarkTracesProcessorHotPath benchmarks the non-blocking hot path for traces.
func BenchmarkTracesProcessorHotPath(b *testing.B) {
	cfg := &Config{
		FlushInterval: 10 * time.Second,
		Bytes:         10 * 1024 * 1024, // 10MB threshold
	}
	logger := zap.NewNop()
	sink := &consumertest.TracesSink{}

	proc := newTracesProcessor(cfg, logger, nil, func() batch[ptrace.Traces] {
		return newBatchTraces(sink, nil)
	})

	ctx := context.Background()
	_ = proc.Start(ctx, nil)
	defer proc.Shutdown(ctx)

	td := generateBenchTraces()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = proc.processTraces(ctx, td)
	}
}

// BenchmarkSizeTriggeredFlush benchmarks the size-triggered flush path.
func BenchmarkSizeTriggeredFlush(b *testing.B) {
	cfg := &Config{
		FlushInterval: 1 * time.Hour, // High interval so only size triggers
		Bytes:         1024,          // Small threshold for quick flush
	}
	logger := zap.NewNop()
	sink := &consumertest.MetricsSink{}

	proc := newMetricsProcessor(cfg, logger, nil, func() batch[pmetric.Metrics] {
		return newBatchMetrics(sink, nil)
	})

	ctx := context.Background()
	_ = proc.Start(ctx, nil)
	defer proc.Shutdown(ctx)

	md := generateBenchMetrics()

	b.ReportAllocs()
	b.ResetTimer()

	// Alternate calls to trigger size-based flush
	for i := 0; i < b.N; i++ {
		_, _ = proc.processMetrics(ctx, md)
	}
}

// BenchmarkQueueAddAllocation benchmarks the allocation cost of adding to queue.
func BenchmarkQueueAddAllocation(b *testing.B) {
	cfg := &Config{
		FlushInterval: 10 * time.Second,
		Bytes:         10 * 1024 * 1024,
	}
	logger := zap.NewNop()
	sink := &consumertest.MetricsSink{}

	q := newQueue(cfg, logger, nil, func() batch[pmetric.Metrics] {
		return newBatchMetrics(sink, nil)
	})

	md := generateBenchMetrics()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b := q.newBatch()
		q.add(md, b)
	}
}

// BenchmarkBatchFlushMerge benchmarks the merge cost during flush.
func BenchmarkBatchFlushMerge(b *testing.B) {
	sink := &consumertest.MetricsSink{}
	batch := newBatchMetrics(sink, nil)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Reset for each iteration
		batch.reset()

		// Add 10 metric batches
		for j := 0; j < 10; j++ {
			batch.add(generateBenchMetrics())
		}

		ctx := context.Background()
		_ = batch.flush(ctx)
	}
}

// BenchmarkProtoMarshalerSizing benchmarks the ProtoMarshaler sizing cost.
func BenchmarkProtoMarshalerSizing(b *testing.B) {
	md := generateBenchMetrics()
	sizer := pmetric.ProtoMarshaler{}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = sizer.MetricsSize(md)
	}
}

// BenchmarkContention benchmarks contention on the queue mutex with concurrent adds.
func BenchmarkContention(b *testing.B) {
	cfg := &Config{
		FlushInterval: 10 * time.Second,
		Bytes:         100 * 1024 * 1024, // High threshold to avoid frequent flushes
	}
	logger := zap.NewNop()
	sink := &consumertest.MetricsSink{}

	proc := newMetricsProcessor(cfg, logger, nil, func() batch[pmetric.Metrics] {
		return newBatchMetrics(sink, nil)
	})

	ctx := context.Background()
	_ = proc.Start(ctx, nil)
	defer proc.Shutdown(ctx)

	md := generateBenchMetrics()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = proc.processMetrics(ctx, md)
		}
	})
}
