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

package throughputmeasurementprocessor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
)

var errDownstream = errors.New("downstream refused")

// newTestProcessor builds a processor with the given config and returns it with a
// manual reader for its counters.
func newTestProcessor(t *testing.T, cfg *Config) (*throughputMeasurementProcessor, *metric.ManualReader) {
	t.Helper()

	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, cfg, component.MustNewIDWithName("throughputmeasurement", t.Name()))
	require.NoError(t, err)
	return tmp, reader
}

// collectSums reads every Int64 sum from the reader and returns name to value.
func collectSums(t *testing.T, reader *metric.ManualReader) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	sums := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			require.Len(t, sum.DataPoints, 1, m.Name)
			sums[m.Name] = sum.DataPoints[0].Value
		}
	}
	return sums
}

func TestLogsConsumer_Delivered(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1, MeasureLogRawBytes: true})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	sink := new(consumertest.LogsSink)
	require.NoError(t, newLogsConsumer(tmp, sink).ConsumeLogs(context.Background(), logs))
	require.Equal(t, 16, sink.LogRecordCount())

	sums := collectSums(t, reader)
	require.Equal(t, int64(3974), sums["otelcol_processor_throughputmeasurement_log_data_size"])
	require.Equal(t, int64(16), sums["otelcol_processor_throughputmeasurement_log_count"])
	require.Equal(t, int64(2373), sums["otelcol_processor_throughputmeasurement_log_raw_bytes"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_data_size_rejected")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_count_rejected")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_raw_bytes_rejected")
}

func TestLogsConsumer_Rejected(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1, MeasureLogRawBytes: true})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	err = newLogsConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeLogs(context.Background(), logs)
	require.ErrorIs(t, err, errDownstream)

	sums := collectSums(t, reader)
	require.Equal(t, int64(3974), sums["otelcol_processor_throughputmeasurement_log_data_size_rejected"])
	require.Equal(t, int64(16), sums["otelcol_processor_throughputmeasurement_log_count_rejected"])
	require.Equal(t, int64(2373), sums["otelcol_processor_throughputmeasurement_log_raw_bytes_rejected"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_data_size")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_count")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_raw_bytes")
	require.Equal(t, int64(0), tmp.measurements.SequenceNumber())
}

// A downstream batch processor moves the records out of the payload before it returns.
// The measurement must be taken before the forward, so the full size is recorded.
func TestLogsConsumer_MeasuresBeforeForward(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	draining, err := consumer.NewLogs(func(_ context.Context, ld plog.Logs) error {
		ld.ResourceLogs().MoveAndAppendTo(plog.NewResourceLogsSlice())
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, newLogsConsumer(tmp, draining).ConsumeLogs(context.Background(), logs))
	require.Equal(t, 0, logs.LogRecordCount(), "precondition: downstream emptied the payload")

	sums := collectSums(t, reader)
	require.Equal(t, int64(3974), sums["otelcol_processor_throughputmeasurement_log_data_size"])
	require.Equal(t, int64(16), sums["otelcol_processor_throughputmeasurement_log_count"])
}

func TestLogsConsumer_DisabledRecordsNothing(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: false, SamplingRatio: 1})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	err = newLogsConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeLogs(context.Background(), logs)
	require.ErrorIs(t, err, errDownstream)

	require.Empty(t, collectSums(t, reader))
}

func TestLogsConsumer_ZeroSamplingRecordsNothing(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 0})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	err = newLogsConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeLogs(context.Background(), logs)
	require.ErrorIs(t, err, errDownstream)

	require.Empty(t, collectSums(t, reader))
}

func TestMetricsConsumer_Delivered(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	sink := new(consumertest.MetricsSink)
	require.NoError(t, newMetricsConsumer(tmp, sink).ConsumeMetrics(context.Background(), metrics))
	require.Equal(t, 37, sink.DataPointCount())

	sums := collectSums(t, reader)
	require.Equal(t, int64(5675), sums["otelcol_processor_throughputmeasurement_metric_data_size"])
	require.Equal(t, int64(37), sums["otelcol_processor_throughputmeasurement_metric_count"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_data_size_rejected")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_count_rejected")
}

func TestMetricsConsumer_Rejected(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	err = newMetricsConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeMetrics(context.Background(), metrics)
	require.ErrorIs(t, err, errDownstream)

	sums := collectSums(t, reader)
	require.Equal(t, int64(5675), sums["otelcol_processor_throughputmeasurement_metric_data_size_rejected"])
	require.Equal(t, int64(37), sums["otelcol_processor_throughputmeasurement_metric_count_rejected"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_data_size")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_count")
	require.Equal(t, int64(0), tmp.measurements.SequenceNumber())
}

func TestMetricsConsumer_MeasuresBeforeForward(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	draining, err := consumer.NewMetrics(func(_ context.Context, md pmetric.Metrics) error {
		md.ResourceMetrics().MoveAndAppendTo(pmetric.NewResourceMetricsSlice())
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, newMetricsConsumer(tmp, draining).ConsumeMetrics(context.Background(), metrics))
	require.Equal(t, 0, metrics.DataPointCount(), "precondition: downstream emptied the payload")

	sums := collectSums(t, reader)
	require.Equal(t, int64(5675), sums["otelcol_processor_throughputmeasurement_metric_data_size"])
	require.Equal(t, int64(37), sums["otelcol_processor_throughputmeasurement_metric_count"])
}

func TestTracesConsumer_Delivered(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	sink := new(consumertest.TracesSink)
	require.NoError(t, newTracesConsumer(tmp, sink).ConsumeTraces(context.Background(), traces))
	require.Equal(t, 178, sink.SpanCount())

	sums := collectSums(t, reader)
	require.Equal(t, int64(16767), sums["otelcol_processor_throughputmeasurement_trace_data_size"])
	require.Equal(t, int64(178), sums["otelcol_processor_throughputmeasurement_trace_count"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_data_size_rejected")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_count_rejected")
}

func TestTracesConsumer_Rejected(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	err = newTracesConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeTraces(context.Background(), traces)
	require.ErrorIs(t, err, errDownstream)

	sums := collectSums(t, reader)
	require.Equal(t, int64(16767), sums["otelcol_processor_throughputmeasurement_trace_data_size_rejected"])
	require.Equal(t, int64(178), sums["otelcol_processor_throughputmeasurement_trace_count_rejected"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_data_size")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_count")
	require.Equal(t, int64(0), tmp.measurements.SequenceNumber())
}

func TestTracesConsumer_MeasuresBeforeForward(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	draining, err := consumer.NewTraces(func(_ context.Context, td ptrace.Traces) error {
		td.ResourceSpans().MoveAndAppendTo(ptrace.NewResourceSpansSlice())
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, newTracesConsumer(tmp, draining).ConsumeTraces(context.Background(), traces))
	require.Equal(t, 0, traces.SpanCount(), "precondition: downstream emptied the payload")

	sums := collectSums(t, reader)
	require.Equal(t, int64(16767), sums["otelcol_processor_throughputmeasurement_trace_data_size"])
	require.Equal(t, int64(178), sums["otelcol_processor_throughputmeasurement_trace_count"])
}
