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

package throughputmeasurementprocessor

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/jonboulle/clockwork"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/plogtest"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/pmetrictest"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/ptracetest"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/pkg/measurements"
)

func TestProcessor_Logs(t *testing.T) {
	manualReader := metric.NewManualReader()
	defer manualReader.Shutdown(context.Background())

	mp := metric.NewMeterProvider(
		metric.WithReader(manualReader),
	)
	defer mp.Shutdown(context.Background())

	processorID := component.MustNewIDWithName("throughputmeasurement", "1")

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:       true,
		SamplingRatio: 1,
	}, processorID)
	require.NoError(t, err)

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	processedLogs, err := tmp.processLogs(context.Background(), logs)
	require.NoError(t, err)

	// Output logs should be the same as input logs (passthrough check)
	require.NoError(t, plogtest.CompareLogs(logs, processedLogs))

	var rm metricdata.ResourceMetrics
	require.NoError(t, manualReader.Collect(context.Background(), &rm))

	// Extract the metrics we care about from the metrics we collected
	var logSize, logCount int64

	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			switch metric.Name {
			case "otelcol_processor_throughputmeasurement_log_data_size":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 1, len(sum.DataPoints))

				processorAttr, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("processor"))
				require.True(t, ok, "processor attribute was not found")
				require.Equal(t, processorID.String(), processorAttr.AsString())

				logSize = sum.DataPoints[0].Value

			case "otelcol_processor_throughputmeasurement_log_count":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 1, len(sum.DataPoints))

				processorAttr, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("processor"))
				require.True(t, ok, "processor attribute was not found")
				require.Equal(t, processorID.String(), processorAttr.AsString())

				logCount = sum.DataPoints[0].Value
			}

		}
	}

	require.Equal(t, int64(3974), logSize)
	require.Equal(t, int64(16), logCount)
}

func TestProcessor_Metrics(t *testing.T) {
	manualReader := metric.NewManualReader()
	defer manualReader.Shutdown(context.Background())

	mp := metric.NewMeterProvider(
		metric.WithReader(manualReader),
	)
	defer mp.Shutdown(context.Background())

	processorID := component.MustNewIDWithName("throughputmeasurement", "1")

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:       true,
		SamplingRatio: 1,
	}, processorID)
	require.NoError(t, err)

	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	processedMetrics, err := tmp.processMetrics(context.Background(), metrics)
	require.NoError(t, err)

	// Output metrics should be the same as input logs (passthrough check)
	require.NoError(t, pmetrictest.CompareMetrics(metrics, processedMetrics))

	var rm metricdata.ResourceMetrics
	require.NoError(t, manualReader.Collect(context.Background(), &rm))

	// Extract the metrics we care about from the metrics we collected
	var metricSize, datapointCount int64

	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			switch metric.Name {
			case "otelcol_processor_throughputmeasurement_metric_data_size":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 1, len(sum.DataPoints))

				processorAttr, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("processor"))
				require.True(t, ok, "processor attribute was not found")
				require.Equal(t, processorID.String(), processorAttr.AsString())

				metricSize = sum.DataPoints[0].Value

			case "otelcol_processor_throughputmeasurement_metric_count":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 1, len(sum.DataPoints))

				processorAttr, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("processor"))
				require.True(t, ok, "processor attribute was not found")
				require.Equal(t, processorID.String(), processorAttr.AsString())

				datapointCount = sum.DataPoints[0].Value
			}

		}
	}

	require.Equal(t, int64(5675), metricSize)
	require.Equal(t, int64(37), datapointCount)
}

func TestProcessor_Traces(t *testing.T) {
	manualReader := metric.NewManualReader()
	defer manualReader.Shutdown(context.Background())

	mp := metric.NewMeterProvider(
		metric.WithReader(manualReader),
	)
	defer mp.Shutdown(context.Background())

	processorID := component.MustNewIDWithName("throughputmeasurement", "1")

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:       true,
		SamplingRatio: 1,
	}, processorID)
	require.NoError(t, err)

	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	processedTraces, err := tmp.processTraces(context.Background(), traces)
	require.NoError(t, err)

	// Output traces should be the same as input logs (passthrough check)
	require.NoError(t, ptracetest.CompareTraces(traces, processedTraces))

	var rm metricdata.ResourceMetrics
	require.NoError(t, manualReader.Collect(context.Background(), &rm))

	// Extract the metrics we care about from the metrics we collected
	var traceSize, spanCount int64

	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			switch metric.Name {
			case "otelcol_processor_throughputmeasurement_trace_data_size":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 1, len(sum.DataPoints))

				processorAttr, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("processor"))
				require.True(t, ok, "processor attribute was not found")
				require.Equal(t, processorID.String(), processorAttr.AsString())

				traceSize = sum.DataPoints[0].Value

			case "otelcol_processor_throughputmeasurement_trace_count":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 1, len(sum.DataPoints))

				processorAttr, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("processor"))
				require.True(t, ok, "processor attribute was not found")
				require.Equal(t, processorID.String(), processorAttr.AsString())

				spanCount = sum.DataPoints[0].Value
			}

		}
	}

	require.Equal(t, int64(16767), traceSize)
	require.Equal(t, int64(178), spanCount)
}

// Test that 2 instances with the same processor ID add their metrics together
func TestProcessor_Logs_TwoInstancesSameID(t *testing.T) {
	manualReader := metric.NewManualReader()
	defer manualReader.Shutdown(context.Background())

	mp := metric.NewMeterProvider(
		metric.WithReader(manualReader),
	)
	defer mp.Shutdown(context.Background())

	processorID := component.MustNewIDWithName("throughputmeasurement", "1")

	tmp1, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:            true,
		SamplingRatio:      1,
		MeasureLogRawBytes: true,
	}, processorID)
	require.NoError(t, err)

	tmp2, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:            true,
		SamplingRatio:      1,
		MeasureLogRawBytes: true,
	}, processorID)
	require.NoError(t, err)

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	_, err = tmp1.processLogs(context.Background(), logs)
	require.NoError(t, err)

	_, err = tmp2.processLogs(context.Background(), logs)
	require.NoError(t, err)

	var rm metricdata.ResourceMetrics
	require.NoError(t, manualReader.Collect(context.Background(), &rm))

	// Extract the metrics we care about from the metrics we collected
	var logSize, logCount, logRawBytesSize int64

	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			switch metric.Name {
			case "otelcol_processor_throughputmeasurement_log_data_size":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 1, len(sum.DataPoints))

				processorAttr, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("processor"))
				require.True(t, ok, "processor attribute was not found")
				require.Equal(t, processorID.String(), processorAttr.AsString())

				logSize = sum.DataPoints[0].Value

			case "otelcol_processor_throughputmeasurement_log_count":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 1, len(sum.DataPoints))

				processorAttr, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("processor"))
				require.True(t, ok, "processor attribute was not found")
				require.Equal(t, processorID.String(), processorAttr.AsString())

				logCount = sum.DataPoints[0].Value

			case "otelcol_processor_throughputmeasurement_log_raw_bytes":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 1, len(sum.DataPoints))

				processorAttr, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("processor"))
				require.True(t, ok, "processor attribute was not found")
				require.Equal(t, processorID.String(), processorAttr.AsString())

				logRawBytesSize = sum.DataPoints[0].Value
			}

		}
	}

	require.Equal(t, int64(2*3974), logSize)
	require.Equal(t, int64(2*16), logCount)
	require.Equal(t, int64(4746), logRawBytesSize)
}

func TestProcessor_Logs_TwoInstancesDifferentID(t *testing.T) {
	// Test that different IDs shouldn't overlap, but instead create distinct datapoints.
	manualReader := metric.NewManualReader()
	defer manualReader.Shutdown(context.Background())

	mp := metric.NewMeterProvider(
		metric.WithReader(manualReader),
	)
	defer mp.Shutdown(context.Background())

	processorID1 := component.MustNewIDWithName("throughputmeasurement", "1")
	processorID2 := component.MustNewIDWithName("throughputmeasurement", "2")

	tmp1, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:            true,
		SamplingRatio:      1,
		MeasureLogRawBytes: true,
	}, processorID1)
	require.NoError(t, err)

	tmp2, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:            true,
		SamplingRatio:      1,
		MeasureLogRawBytes: true,
	}, processorID2)
	require.NoError(t, err)

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	_, err = tmp1.processLogs(context.Background(), logs)
	require.NoError(t, err)

	// Ingest twice on the second processor so we get a different count for proc2
	_, err = tmp2.processLogs(context.Background(), logs)
	require.NoError(t, err)
	_, err = tmp2.processLogs(context.Background(), logs)
	require.NoError(t, err)

	var rm metricdata.ResourceMetrics
	require.NoError(t, manualReader.Collect(context.Background(), &rm))

	// Extract the metrics we care about from the metrics we collected
	var logSize1, logCount1, logSize2, logCount2 int64

	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			switch metric.Name {
			case "otelcol_processor_throughputmeasurement_log_data_size":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 2, len(sum.DataPoints))

				for _, dp := range sum.DataPoints {
					processorAttr, ok := dp.Attributes.Value(attribute.Key("processor"))
					require.True(t, ok, "processor attribute was not found")

					switch processorAttr.AsString() {
					case processorID1.String():
						logSize1 = dp.Value
					case processorID2.String():
						logSize2 = dp.Value
					default:
						require.Fail(t, "ID %s should not be present in log data size metrics", processorAttr.AsString())
					}
				}

			case "otelcol_processor_throughputmeasurement_log_count":
				sum := metric.Data.(metricdata.Sum[int64])
				require.Equal(t, 2, len(sum.DataPoints))

				for _, dp := range sum.DataPoints {
					processorAttr, ok := dp.Attributes.Value(attribute.Key("processor"))
					require.True(t, ok, "processor attribute was not found")

					switch processorAttr.AsString() {
					case processorID1.String():
						logCount1 = dp.Value
					case processorID2.String():
						logCount2 = dp.Value
					default:
						require.Fail(t, "ID %s should not be present in log count metrics", processorAttr.AsString())
					}
				}
			}

		}
	}

	require.Equal(t, int64(3974), logSize1)
	require.Equal(t, int64(16), logCount1)

	require.Equal(t, int64(2*3974), logSize2)
	require.Equal(t, int64(2*16), logCount2)
}

func TestProcessor_ReportsMeasurementsOverOpAMP(t *testing.T) {
	mp := metric.NewMeterProvider()
	defer mp.Shutdown(context.Background())

	processorID := component.MustNewIDWithName("throughputmeasurement", "1")
	opampID := component.MustNewID("opamp")

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:       true,
		SamplingRatio: 1,
		OpAMP:         opampID,
		Interval:      time.Minute,
	}, processorID)
	require.NoError(t, err)

	clk := installFakeReporterClock(t)

	mockOpamp := &mockOpAMPExtension{msgChan: make(chan *protobufs.CustomMessage, 1)}
	mh := mockHost{
		extMap: map[component.ID]component.Component{
			opampID: mockOpamp,
		},
	}

	require.NoError(t, tmp.start(context.Background(), mh))
	require.Equal(t, measurements.ReportMeasurementsV1Capability, mockOpamp.capability)

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	_, err = tmp.processLogs(context.Background(), logs)
	require.NoError(t, err)

	// Wait for the report loop's ticker to exist, then fire it.
	clk.BlockUntil(1)
	clk.Advance(time.Minute)

	require.Eventually(t, func() bool {
		return mockOpamp.GotMessage()
	}, 5*time.Second, 10*time.Millisecond)

	require.Equal(t, measurements.ReportMeasurementsType, mockOpamp.sentMessageType)

	decoded, err := snappy.Decode(nil, mockOpamp.sentMessage)
	require.NoError(t, err)

	unmarshaler := pmetric.ProtoUnmarshaler{}
	m, err := unmarshaler.UnmarshalMetrics(decoded)
	require.NoError(t, err)

	metricNames := map[string]struct{}{}
	sm := m.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
	for i := 0; i < sm.Len(); i++ {
		metricNames[sm.At(i).Name()] = struct{}{}
		dp := sm.At(i).Sum().DataPoints().At(0)
		processorAttr, ok := dp.Attributes().Get("processor")
		require.True(t, ok, "processor attribute was not found")
		require.Equal(t, processorID.String(), processorAttr.Str())
	}
	require.Contains(t, metricNames, "otelcol_processor_throughputmeasurement_log_data_size")

	require.NoError(t, tmp.shutdown(context.Background()))

	// The last processor to shut down tears the shared reporter down.
	reporterMux.Lock()
	require.Nil(t, reporter)
	reporterMux.Unlock()
}

// Test that multiple processors report through a single shared reporter as one
// aggregated message, like the bindplane extension does.
func TestProcessor_AggregatesMeasurementsOverOpAMP(t *testing.T) {
	mp := metric.NewMeterProvider()
	defer mp.Shutdown(context.Background())

	opampID := component.MustNewID("opamp")
	cfg := &Config{
		Enabled:       true,
		SamplingRatio: 1,
		OpAMP:         opampID,
		Interval:      time.Minute,
	}

	processorID1 := component.MustNewIDWithName("throughputmeasurement", "agg1")
	processorID2 := component.MustNewIDWithName("throughputmeasurement", "agg2")

	tmp1, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, cfg, processorID1)
	require.NoError(t, err)
	tmp2, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, cfg, processorID2)
	require.NoError(t, err)

	clk := installFakeReporterClock(t)

	mockOpamp := &mockOpAMPExtension{msgChan: make(chan *protobufs.CustomMessage, 1)}
	mh := mockHost{
		extMap: map[component.ID]component.Component{
			opampID: mockOpamp,
		},
	}

	require.NoError(t, tmp1.start(context.Background(), mh))
	require.NoError(t, tmp2.start(context.Background(), mh))

	// Both processors share one reporter: the capability is registered once.
	require.Equal(t, 1, mockOpamp.RegisterCount())

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	_, err = tmp1.processLogs(context.Background(), logs)
	require.NoError(t, err)
	_, err = tmp2.processLogs(context.Background(), logs)
	require.NoError(t, err)

	clk.BlockUntil(1)
	clk.Advance(time.Minute)

	require.Eventually(t, func() bool {
		return mockOpamp.GotMessage()
	}, 5*time.Second, 10*time.Millisecond)

	decoded, err := snappy.Decode(nil, mockOpamp.sentMessage)
	require.NoError(t, err)

	unmarshaler := pmetric.ProtoUnmarshaler{}
	m, err := unmarshaler.UnmarshalMetrics(decoded)
	require.NoError(t, err)

	// One message contains datapoints from both processors.
	seenProcessors := map[string]struct{}{}
	sm := m.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
	for i := 0; i < sm.Len(); i++ {
		dps := sm.At(i).Sum().DataPoints()
		for j := 0; j < dps.Len(); j++ {
			processorAttr, ok := dps.At(j).Attributes().Get("processor")
			require.True(t, ok, "processor attribute was not found")
			seenProcessors[processorAttr.Str()] = struct{}{}
		}
	}
	require.Contains(t, seenProcessors, processorID1.String())
	require.Contains(t, seenProcessors, processorID2.String())

	// The reporter survives until the last processor shuts down.
	require.NoError(t, tmp1.shutdown(context.Background()))
	reporterMux.Lock()
	require.NotNil(t, reporter)
	reporterMux.Unlock()

	require.NoError(t, tmp2.shutdown(context.Background()))
	reporterMux.Lock()
	require.Nil(t, reporter)
	reporterMux.Unlock()
}

// installFakeReporterClock swaps the shared reporter's clock for a fake one
// for the duration of the test.
func installFakeReporterClock(t *testing.T) *clockwork.FakeClock {
	t.Helper()
	clk := clockwork.NewFakeClock()
	old := reporterClock
	reporterClock = clk
	t.Cleanup(func() { reporterClock = old })
	return clk
}

type mockOpAMPExtension struct {
	msgChan chan *protobufs.CustomMessage

	capability    string
	registerCount int

	gotMessageMux   sync.Mutex
	gotMessage      bool
	sentMessageType string
	sentMessage     []byte
}

func (m *mockOpAMPExtension) Start(_ context.Context, _ component.Host) error { return nil }

func (m *mockOpAMPExtension) Shutdown(_ context.Context) error { return nil }

func (m *mockOpAMPExtension) Register(capability string, _ ...opampcustommessages.CustomCapabilityRegisterOption) (handler opampcustommessages.CustomCapabilityHandler, err error) {
	m.gotMessageMux.Lock()
	defer m.gotMessageMux.Unlock()

	m.capability = capability
	m.registerCount++
	return m, nil
}

func (m *mockOpAMPExtension) RegisterCount() int {
	m.gotMessageMux.Lock()
	defer m.gotMessageMux.Unlock()

	return m.registerCount
}

func (m *mockOpAMPExtension) Message() <-chan *protobufs.CustomMessage {
	return m.msgChan
}

func (m *mockOpAMPExtension) SendMessage(messageType string, message []byte) (messageSendingChannel chan struct{}, err error) {
	m.gotMessageMux.Lock()
	defer m.gotMessageMux.Unlock()

	if m.gotMessage {
		return
	}
	m.gotMessage = true

	m.sentMessageType = messageType
	m.sentMessage = message
	return
}

func (m *mockOpAMPExtension) GotMessage() bool {
	m.gotMessageMux.Lock()
	defer m.gotMessageMux.Unlock()

	return m.gotMessage
}

func (m *mockOpAMPExtension) Unregister() {}

func TestProcessor_RegistersWithBindplaneExtension(t *testing.T) {
	mp := metric.NewMeterProvider()
	defer mp.Shutdown(context.Background())

	processorID := component.MustNewIDWithName("throughputmeasurement", "bindplane_ext_fallback")
	bindplaneID := component.MustNewID("bindplane")

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:            true,
		SamplingRatio:      1,
		BindplaneExtension: bindplaneID,
	}, processorID)
	require.NoError(t, err)

	reg := measurements.NewResettableThroughputMeasurementsRegistry(false)
	mh := mockHost{
		extMap: map[component.ID]component.Component{
			bindplaneID: mockThroughputRegistry{reg},
		},
	}

	require.NoError(t, tmp.start(context.Background(), mh))

	// Registering the same processor ID again through the extension errors,
	// proving the first registration landed in the extension's registry.
	require.Error(t, reg.RegisterThroughputMeasurements(processorID.String(), tmp.measurements))

	require.NoError(t, tmp.shutdown(context.Background()))
}

func TestProcessor_RegistersWithAgentRegistry(t *testing.T) {
	mp := metric.NewMeterProvider()
	defer mp.Shutdown(context.Background())

	processorID := component.MustNewIDWithName("throughputmeasurement", "v1_fallback")

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:       true,
		SamplingRatio: 1,
	}, processorID)
	require.NoError(t, err)

	// Neither opamp nor bindplane_extension set: registers with the package-level
	// registry read by the v1 bindplane agent.
	require.NoError(t, tmp.start(context.Background(), mockHost{}))
	require.Error(t, measurements.BindplaneAgentThroughputMeasurementsRegistry.RegisterThroughputMeasurements(processorID.String(), tmp.measurements))

	// A second processor with the same ID (e.g. after a config reload without a
	// registry reset) must not fail startup.
	tmp2, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:       true,
		SamplingRatio: 1,
	}, processorID)
	require.NoError(t, err)
	require.NoError(t, tmp2.start(context.Background(), mockHost{}))

	require.NoError(t, tmp.shutdown(context.Background()))
	require.NoError(t, tmp2.shutdown(context.Background()))
}

type mockThroughputRegistry struct {
	*measurements.ResettableThroughputMeasurementsRegistry
}

func (mockThroughputRegistry) Start(_ context.Context, _ component.Host) error { return nil }
func (mockThroughputRegistry) Shutdown(_ context.Context) error                { return nil }

func TestProcessor_BindplaneExtensionMissing_FallsBackToAgentRegistry(t *testing.T) {
	mp := metric.NewMeterProvider()
	defer mp.Shutdown(context.Background())

	processorID := component.MustNewIDWithName("throughputmeasurement", "missing_ext_fallback")

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:            true,
		SamplingRatio:      1,
		BindplaneExtension: component.MustNewID("bindplane"),
	}, processorID)
	require.NoError(t, err)

	// Old Bindplane servers render bindplane_extension without instantiating the
	// extension; startup must succeed and fall back to the v1 agent registry.
	require.NoError(t, tmp.start(context.Background(), mockHost{}))
	require.Error(t, measurements.BindplaneAgentThroughputMeasurementsRegistry.RegisterThroughputMeasurements(processorID.String(), tmp.measurements))

	require.NoError(t, tmp.shutdown(context.Background()))
}
