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

package snapshotprocessor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor/processortest"
	"go.uber.org/zap"
)

func TestProcess_Logs(t *testing.T) {
	factory := NewFactory()
	sink := &consumertest.LogsSink{}

	pSet := processortest.NewNopSettings(componentType)
	p, err := factory.CreateLogs(context.Background(), pSet, factory.CreateDefaultConfig(), sink)
	require.NoError(t, err)

	mockOpamp := &mockOpAMPExtension{
		msgChan: make(chan *protobufs.CustomMessage, 1),
	}

	mockHost := &mockHost{
		extensions: map[component.ID]component.Component{
			component.MustNewID("opamp"): mockOpamp,
		},
	}

	require.NoError(t, p.Start(context.Background(), mockHost))
	t.Cleanup(func() {
		require.NoError(t, p.Shutdown(context.Background()))
	})

	require.Equal(t, "com.bindplane.snapshot", mockOpamp.capability)

	l, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	require.NoError(t, p.ConsumeLogs(context.Background(), l))

	require.Equal(t, 1, len(sink.AllLogs()))
	require.Equal(t, l, sink.AllLogs()[0])

	// Request buffer
	reqPayload := fmt.Sprintf(`{"processor":%q,"pipeline_type":"logs","session_id":"my-session-id","maximum_payload_size":100000}`, pSet.ID)

	cm := &protobufs.CustomMessage{
		Capability: "com.bindplane.snapshot",
		Type:       "requestSnapshot",
		Data:       []byte(reqPayload),
	}

	mockOpamp.msgChan <- cm

	// Wait for response
	require.Eventually(t, func() bool {
		return mockOpamp.GotMessage()
	}, 5*time.Second, 100*time.Millisecond)

	by, err := os.ReadFile(filepath.Join("testdata", "snapshot", "logs-report.json"))
	require.NoError(t, err)

	var expectedMessageContents map[string]any
	err = json.Unmarshal(by, &expectedMessageContents)
	require.NoError(t, err)

	var actualMessageContents map[string]any
	err = json.Unmarshal(gunzipBytes(t, mockOpamp.sentMessage), &actualMessageContents)
	require.NoError(t, err)

	require.Equal(t, expectedMessageContents, actualMessageContents)
	require.Equal(t, "reportSnapshot", mockOpamp.sentMessageType)
}

func TestProcess_Metrics(t *testing.T) {
	factory := NewFactory()
	sink := &consumertest.MetricsSink{}

	pSet := processortest.NewNopSettings(componentType)
	p, err := factory.CreateMetrics(context.Background(), pSet, factory.CreateDefaultConfig(), sink)
	require.NoError(t, err)

	mockOpamp := &mockOpAMPExtension{
		msgChan: make(chan *protobufs.CustomMessage, 1),
	}

	mockHost := &mockHost{
		extensions: map[component.ID]component.Component{
			component.MustNewID("opamp"): mockOpamp,
		},
	}

	require.NoError(t, p.Start(context.Background(), mockHost))
	t.Cleanup(func() {
		require.NoError(t, p.Shutdown(context.Background()))
	})

	require.Equal(t, "com.bindplane.snapshot", mockOpamp.capability)

	m, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	require.NoError(t, p.ConsumeMetrics(context.Background(), m))

	require.Equal(t, 1, len(sink.AllMetrics()))
	require.Equal(t, m, sink.AllMetrics()[0])

	// Request buffer
	reqPayload := fmt.Sprintf(`{"processor":%q,"pipeline_type":"metrics","session_id":"my-session-id","maximum_payload_size":100000}`, pSet.ID)

	cm := &protobufs.CustomMessage{
		Capability: "com.bindplane.snapshot",
		Type:       "requestSnapshot",
		Data:       []byte(reqPayload),
	}

	mockOpamp.msgChan <- cm

	// Wait for response
	require.Eventually(t, func() bool {
		return mockOpamp.GotMessage()
	}, 5*time.Second, 100*time.Millisecond)

	by, err := os.ReadFile(filepath.Join("testdata", "snapshot", "metrics-report.json"))
	require.NoError(t, err)

	var expectedMessageContents map[string]any
	err = json.Unmarshal(by, &expectedMessageContents)
	require.NoError(t, err)

	var actualMessageContents map[string]any
	err = json.Unmarshal(gunzipBytes(t, mockOpamp.sentMessage), &actualMessageContents)
	require.NoError(t, err)

	require.Equal(t, expectedMessageContents, actualMessageContents)
	require.Equal(t, "reportSnapshot", mockOpamp.sentMessageType)
}

func TestProcess_Traces(t *testing.T) {
	factory := NewFactory()
	sink := &consumertest.TracesSink{}

	pSet := processortest.NewNopSettings(componentType)
	p, err := factory.CreateTraces(context.Background(), pSet, factory.CreateDefaultConfig(), sink)
	require.NoError(t, err)

	mockOpamp := &mockOpAMPExtension{
		msgChan: make(chan *protobufs.CustomMessage, 1),
	}

	mockHost := &mockHost{
		extensions: map[component.ID]component.Component{
			component.MustNewID("opamp"): mockOpamp,
		},
	}

	require.NoError(t, p.Start(context.Background(), mockHost))
	t.Cleanup(func() {
		require.NoError(t, p.Shutdown(context.Background()))
	})

	require.Equal(t, "com.bindplane.snapshot", mockOpamp.capability)

	tr, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	require.NoError(t, p.ConsumeTraces(context.Background(), tr))

	require.Equal(t, 1, len(sink.AllTraces()))
	require.Equal(t, tr, sink.AllTraces()[0])

	// Request buffer
	reqPayload := fmt.Sprintf(`{"processor":%q,"pipeline_type":"traces","session_id":"my-session-id","maximum_payload_size":100000}`, pSet.ID)

	cm := &protobufs.CustomMessage{
		Capability: "com.bindplane.snapshot",
		Type:       "requestSnapshot",
		Data:       []byte(reqPayload),
	}

	mockOpamp.msgChan <- cm

	// Wait for response
	require.Eventually(t, func() bool {
		return mockOpamp.GotMessage()
	}, 5*time.Second, 100*time.Millisecond)

	by, err := os.ReadFile(filepath.Join("testdata", "snapshot", "traces-report.json"))
	require.NoError(t, err)

	var expectedMessageContents map[string]any
	err = json.Unmarshal(by, &expectedMessageContents)
	require.NoError(t, err)

	var actualMessageContents map[string]any
	err = json.Unmarshal(gunzipBytes(t, mockOpamp.sentMessage), &actualMessageContents)
	require.NoError(t, err)

	require.Equal(t, expectedMessageContents, actualMessageContents)
	require.Equal(t, "reportSnapshot", mockOpamp.sentMessageType)
}

func TestProcess_Metrics_PreservesTemporalityWithFiltering(t *testing.T) {
	factory := NewFactory()
	sink := &consumertest.MetricsSink{}

	pSet := processortest.NewNopSettings(componentType)
	p, err := factory.CreateMetrics(context.Background(), pSet, factory.CreateDefaultConfig(), sink)
	require.NoError(t, err)

	mockOpamp := &mockOpAMPExtension{
		msgChan: make(chan *protobufs.CustomMessage, 1),
	}

	mockHost := &mockHost{
		extensions: map[component.ID]component.Component{
			component.MustNewID("opamp"): mockOpamp,
		},
	}

	require.NoError(t, p.Start(context.Background(), mockHost))
	t.Cleanup(func() {
		require.NoError(t, p.Shutdown(context.Background()))
	})

	// Load test metrics with different aggregation temporalities
	m, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "temporality-metrics.yaml"))
	require.NoError(t, err)

	require.NoError(t, p.ConsumeMetrics(context.Background(), m))

	// Request buffer with search query (this triggers the filtering code path where the bug occurred)
	reqPayload := fmt.Sprintf(`{"processor":%q,"pipeline_type":"metrics","session_id":"filtering-test","search_query":"transmit","maximum_payload_size":100000}`, pSet.ID)

	cm := &protobufs.CustomMessage{
		Capability: "com.bindplane.snapshot",
		Type:       "requestSnapshot",
		Data:       []byte(reqPayload),
	}

	mockOpamp.msgChan <- cm

	// Wait for response
	require.Eventually(t, func() bool {
		return mockOpamp.GotMessage()
	}, 5*time.Second, 100*time.Millisecond)

	// Parse the actual response
	var actualMessageContents map[string]any
	err = json.Unmarshal(gunzipBytes(t, mockOpamp.sentMessage), &actualMessageContents)
	require.NoError(t, err)

	// Verify filtering worked and only "transmit" metric is present
	telemetryPayload := actualMessageContents["telemetry_payload"].(map[string]any)
	resourceMetrics := telemetryPayload["resourceMetrics"].([]any)
	require.Len(t, resourceMetrics, 1, "Should have one resource metric after filtering")

	firstResource := resourceMetrics[0].(map[string]any)
	scopeMetrics := firstResource["scopeMetrics"].([]any)
	require.Len(t, scopeMetrics, 1, "Should have one scope metric")

	firstScope := scopeMetrics[0].(map[string]any)
	metrics := firstScope["metrics"].([]any)
	require.Len(t, metrics, 1, "Should have one metric matching 'transmit' filter")

	// Verify the filtered metric is the correct one and has preserved aggregation temporality
	filteredMetric := metrics[0].(map[string]any)
	require.Equal(t, "system.network.io", filteredMetric["name"])

	sum := filteredMetric["sum"].(map[string]any)
	require.Equal(t, float64(2), sum["aggregationTemporality"], "Aggregation temporality should be preserved as Cumulative (2) even after filtering")
	require.Equal(t, true, sum["isMonotonic"], "IsMonotonic should be preserved after filtering")

	// Verify the data point attributes contain "transmit"
	dataPoints := sum["dataPoints"].([]any)
	require.Len(t, dataPoints, 1, "Should have one data point")

	dataPoint := dataPoints[0].(map[string]any)
	attributes := dataPoint["attributes"].([]any)
	foundTransmit := false
	for _, attrAny := range attributes {
		attr := attrAny.(map[string]any)
		if attr["key"] == "direction" {
			value := attr["value"].(map[string]any)
			if value["stringValue"] == "transmit" {
				foundTransmit = true
				break
			}
		}
	}
	require.True(t, foundTransmit, "Filtered metric should contain 'transmit' attribute")
}

// admissionTestBatch builds a logs batch of the given size whose record
// bodies carry the given tag, so tests can tell which batch the buffer holds.
func admissionTestBatch(tag string, records int) plog.Logs {
	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	for i := 0; i < records; i++ {
		sl.LogRecords().AppendEmpty().Body().SetStr(tag)
	}
	return ld
}

// bufferedBody returns the body of the first buffered log record.
func bufferedBody(t *testing.T, sp *snapshotProcessor) string {
	t.Helper()

	payload, err := sp.logBuffer.ConstructPayload(&plog.ProtoMarshaler{}, nil, nil, 10*1024*1024)
	require.NoError(t, err)

	ld, err := (&plog.ProtoUnmarshaler{}).UnmarshalLogs(payload)
	require.NoError(t, err)
	require.Greater(t, ld.LogRecordCount(), 0)

	return ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Body().Str()
}

func TestRateLimitedAdmission(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.RefreshInterval = time.Hour // never re-arms during the test
	sp := newSnapshotProcessor(zap.NewNop(), cfg, component.MustNewID("snapshotprocessor"))
	ctx := context.Background()

	// An empty buffer admits unconditionally so it fills promptly.
	_, err := sp.processLogs(ctx, admissionTestBatch("first", 100))
	require.NoError(t, err)
	require.Equal(t, 100, sp.logBuffer.Len())
	require.Equal(t, "first", bufferedBody(t, sp))

	// The buffer is full and the admit flag is down: rejected.
	_, err = sp.processLogs(ctx, admissionTestBatch("second", 100))
	require.NoError(t, err)
	require.Equal(t, "first", bufferedBody(t, sp))

	// Re-arm (normally done by the refresh ticker): exactly one batch is
	// admitted, then admission closes again.
	sp.admitLogs.Store(true)
	_, err = sp.processLogs(ctx, admissionTestBatch("third", 100))
	require.NoError(t, err)
	require.Equal(t, "third", bufferedBody(t, sp))

	_, err = sp.processLogs(ctx, admissionTestBatch("fourth", 100))
	require.NoError(t, err)
	require.Equal(t, "third", bufferedBody(t, sp))
}

func TestAdmissionDisabledRefreshAdmitsEveryBatch(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.RefreshInterval = 0
	sp := newSnapshotProcessor(zap.NewNop(), cfg, component.MustNewID("snapshotprocessor"))
	ctx := context.Background()

	_, err := sp.processLogs(ctx, admissionTestBatch("first", 100))
	require.NoError(t, err)
	_, err = sp.processLogs(ctx, admissionTestBatch("second", 100))
	require.NoError(t, err)

	require.Equal(t, "second", bufferedBody(t, sp))
}

// TestProcessLogs_RejectedPathDoesNotAllocate pins the steady-state hot path:
// once the buffer is full and the refresh interval has not elapsed, a batch
// must pass through without allocating.
func TestProcessLogs_RejectedPathDoesNotAllocate(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	sp := newSnapshotProcessor(zap.NewNop(), cfg, component.MustNewID("snapshotprocessor"))
	ctx := context.Background()

	// Fill the buffer so subsequent batches take the rejected path.
	_, err := sp.processLogs(ctx, admissionTestBatch("fill", 100))
	require.NoError(t, err)

	ld := admissionTestBatch("steady", 1000)
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = sp.processLogs(ctx, ld)
	})
	require.Zero(t, allocs, "rejected-path processLogs must not allocate")
}

func TestSignalsConfigLimitsBuffering(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.Signals = []string{"logs"}
	sp := newSnapshotProcessor(zap.NewNop(), cfg, component.MustNewID("snapshotprocessor"))
	ctx := context.Background()

	require.NotNil(t, sp.logBuffer)
	require.Nil(t, sp.metricBuffer)
	require.Nil(t, sp.traceBuffer)

	// Logs are buffered.
	_, err := sp.processLogs(ctx, admissionTestBatch("logs", 10))
	require.NoError(t, err)
	require.Equal(t, 10, sp.logBuffer.Len())

	// Metrics and traces pass through without buffering (and without panic).
	md, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)
	_, err = sp.processMetrics(ctx, md)
	require.NoError(t, err)

	td, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)
	_, err = sp.processTraces(ctx, td)
	require.NoError(t, err)

	// A snapshot request for an unbuffered signal type is rejected without
	// panicking.
	sp.processSnapshotRequest(&protobufs.CustomMessage{
		Capability: snapshotCapability,
		Type:       snapshotRequestType,
		Data:       []byte(fmt.Sprintf(`{"processor":%q,"pipeline_type":"metrics","session_id":"s"}`, sp.processorID)),
	})
}

// mockHost for component.Host
type mockHost struct {
	extensions map[component.ID]component.Component
}

func (nh *mockHost) GetFactory(component.Kind, component.Type) component.Factory {
	return nil
}

func (nh *mockHost) GetExtensions() map[component.ID]component.Component {
	return nh.extensions
}

type mockOpAMPExtension struct {
	msgChan chan *protobufs.CustomMessage

	capability string

	gotMessageMux   sync.Mutex
	gotMessage      bool
	sentMessageType string
	sentMessage     []byte
}

// Start implements component.Component::Start
func (m *mockOpAMPExtension) Start(_ context.Context, _ component.Host) error {
	return nil
}

// Shutdown implements component.Component::Shutdown
func (m *mockOpAMPExtension) Shutdown(_ context.Context) error { return nil }

func (m *mockOpAMPExtension) Register(capability string, _ ...opampcustommessages.CustomCapabilityRegisterOption) (handler opampcustommessages.CustomCapabilityHandler, err error) {
	m.capability = capability
	return m, nil
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

func gunzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()

	r, err := gzip.NewReader(bytes.NewBuffer(b))
	require.NoError(t, err)
	bOut, err := io.ReadAll(r)
	require.NoError(t, err)

	return bOut
}
