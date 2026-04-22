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

package opampexporter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/observiq/bindplane-otel-contrib/exporter/opampexporter/internal/metadata"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter/exportertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestExporter_ConsumeLogs(t *testing.T) {
	factory := NewFactory()
	set := exportertest.NewNopSettings(metadata.Type)
	set.ID = component.NewIDWithName(metadata.Type, "logs")

	e, err := factory.CreateLogs(context.Background(), set, factory.CreateDefaultConfig())
	require.NoError(t, err)

	mockOpamp := newMockOpAMPExtension()
	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}

	require.NoError(t, e.Start(context.Background(), host))
	t.Cleanup(func() {
		require.NoError(t, e.Shutdown(context.Background()))
	})

	require.Equal(t, opampCapability, mockOpamp.capability)

	logs := generateTestLogs()
	require.NoError(t, e.ConsumeLogs(context.Background(), logs))

	msg := mockOpamp.waitForMessage(t)
	require.Equal(t, otlpMessageType, msg.messageType)

	decoded, err := snappy.Decode(nil, msg.payload)
	require.NoError(t, err)
	unmarshaler := plog.ProtoUnmarshaler{}
	got, err := unmarshaler.UnmarshalLogs(decoded)
	require.NoError(t, err)
	require.Equal(t, logs, got)
}

func TestExporter_ConsumeMetrics(t *testing.T) {
	factory := NewFactory()
	set := exportertest.NewNopSettings(metadata.Type)
	set.ID = component.NewIDWithName(metadata.Type, "metrics")

	e, err := factory.CreateMetrics(context.Background(), set, factory.CreateDefaultConfig())
	require.NoError(t, err)

	mockOpamp := newMockOpAMPExtension()
	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}

	require.NoError(t, e.Start(context.Background(), host))
	t.Cleanup(func() {
		require.NoError(t, e.Shutdown(context.Background()))
	})

	require.Equal(t, opampCapability, mockOpamp.capability)

	metrics := generateTestMetrics()
	require.NoError(t, e.ConsumeMetrics(context.Background(), metrics))

	msg := mockOpamp.waitForMessage(t)
	require.Equal(t, otlpMessageType, msg.messageType)

	decoded, err := snappy.Decode(nil, msg.payload)
	require.NoError(t, err)
	unmarshaler := pmetric.ProtoUnmarshaler{}
	got, err := unmarshaler.UnmarshalMetrics(decoded)
	require.NoError(t, err)
	require.Equal(t, metrics, got)
}

func TestExporter_ConsumeTraces(t *testing.T) {
	factory := NewFactory()
	set := exportertest.NewNopSettings(metadata.Type)
	set.ID = component.NewIDWithName(metadata.Type, "traces")

	e, err := factory.CreateTraces(context.Background(), set, factory.CreateDefaultConfig())
	require.NoError(t, err)

	mockOpamp := newMockOpAMPExtension()
	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}

	require.NoError(t, e.Start(context.Background(), host))
	t.Cleanup(func() {
		require.NoError(t, e.Shutdown(context.Background()))
	})

	require.Equal(t, opampCapability, mockOpamp.capability)

	traces := generateTestTraces()
	require.NoError(t, e.ConsumeTraces(context.Background(), traces))

	msg := mockOpamp.waitForMessage(t)
	require.Equal(t, otlpMessageType, msg.messageType)

	decoded, err := snappy.Decode(nil, msg.payload)
	require.NoError(t, err)
	unmarshaler := ptrace.ProtoUnmarshaler{}
	got, err := unmarshaler.UnmarshalTraces(decoded)
	require.NoError(t, err)
	require.Equal(t, traces, got)
}

func TestExporter_SharedInstanceAcrossSignals(t *testing.T) {
	factory := NewFactory()
	set := exportertest.NewNopSettings(metadata.Type)
	set.ID = component.NewIDWithName(metadata.Type, "shared")

	logsExp, err := factory.CreateLogs(context.Background(), set, factory.CreateDefaultConfig())
	require.NoError(t, err)
	metricsExp, err := factory.CreateMetrics(context.Background(), set, factory.CreateDefaultConfig())
	require.NoError(t, err)
	tracesExp, err := factory.CreateTraces(context.Background(), set, factory.CreateDefaultConfig())
	require.NoError(t, err)

	mockOpamp := newMockOpAMPExtension()
	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}

	// Starting each signal exporter should only register the capability once.
	require.NoError(t, logsExp.Start(context.Background(), host))
	require.NoError(t, metricsExp.Start(context.Background(), host))
	require.NoError(t, tracesExp.Start(context.Background(), host))
	t.Cleanup(func() {
		require.NoError(t, logsExp.Shutdown(context.Background()))
		require.NoError(t, metricsExp.Shutdown(context.Background()))
		require.NoError(t, tracesExp.Shutdown(context.Background()))
	})

	require.Equal(t, 1, mockOpamp.registerCalls())

	require.NoError(t, logsExp.ConsumeLogs(context.Background(), generateTestLogs()))
	require.NoError(t, metricsExp.ConsumeMetrics(context.Background(), generateTestMetrics()))
	require.NoError(t, tracesExp.ConsumeTraces(context.Background(), generateTestTraces()))

	require.Eventually(t, func() bool {
		return mockOpamp.messageCount() == 3
	}, time.Second, 10*time.Millisecond)
}

func TestExporter_Start_MissingExtension(t *testing.T) {
	e := newOpAMPExporter(exportertest.NewNopSettings(metadata.Type).Logger,
		createDefaultConfig().(*Config),
		component.NewIDWithName(metadata.Type, "missing"))
	t.Cleanup(func() { unregisterExporter(e.exporterID) })

	host := &mockHost{extensions: map[component.ID]component.Component{}}
	err := e.start(context.Background(), host)
	require.ErrorContains(t, err, "does not exist")
}

func TestExporter_Start_ExtensionIsNotRegistry(t *testing.T) {
	e := newOpAMPExporter(exportertest.NewNopSettings(metadata.Type).Logger,
		createDefaultConfig().(*Config),
		component.NewIDWithName(metadata.Type, "not-registry"))
	t.Cleanup(func() { unregisterExporter(e.exporterID) })

	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): &nonRegistryExtension{},
	}}
	err := e.start(context.Background(), host)
	require.ErrorContains(t, err, "is not a custom message registry")
}

func TestExporter_Send_RetriesOnPending(t *testing.T) {
	e := newOpAMPExporter(exportertest.NewNopSettings(metadata.Type).Logger,
		createDefaultConfig().(*Config),
		component.NewIDWithName(metadata.Type, "pending"))
	t.Cleanup(func() { unregisterExporter(e.exporterID) })

	mockOpamp := newMockOpAMPExtension()
	// First call returns pending; second call succeeds.
	mockOpamp.pendingBeforeSuccess = 1

	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}

	require.NoError(t, e.start(context.Background(), host))
	t.Cleanup(func() { require.NoError(t, e.shutdown(context.Background())) })

	require.NoError(t, e.consumeLogs(context.Background(), generateTestLogs()))
	require.Equal(t, 1, mockOpamp.messageCount())
	require.Equal(t, 2, mockOpamp.sendCalls())
}

func TestExporter_Send_ReturnsError(t *testing.T) {
	e := newOpAMPExporter(exportertest.NewNopSettings(metadata.Type).Logger,
		createDefaultConfig().(*Config),
		component.NewIDWithName(metadata.Type, "error"))
	t.Cleanup(func() { unregisterExporter(e.exporterID) })

	mockOpamp := newMockOpAMPExtension()
	mockOpamp.sendErr = errors.New("boom")

	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}

	require.NoError(t, e.start(context.Background(), host))
	t.Cleanup(func() { require.NoError(t, e.shutdown(context.Background())) })

	err := e.consumeLogs(context.Background(), generateTestLogs())
	require.ErrorContains(t, err, "boom")
}

// --- test doubles ---

type mockHost struct {
	extensions map[component.ID]component.Component
}

func (h *mockHost) GetExtensions() map[component.ID]component.Component {
	return h.extensions
}

type sentMessage struct {
	messageType string
	payload     []byte
}

type mockOpAMPExtension struct {
	mu sync.Mutex

	capability     string
	registerCount  int
	sendCallCount  int
	sentMessages   []sentMessage
	sendErr        error
	unregisterCall int

	// pendingBeforeSuccess controls how many SendMessage calls return
	// ErrCustomMessagePending before the first successful send.
	pendingBeforeSuccess int
}

func newMockOpAMPExtension() *mockOpAMPExtension {
	return &mockOpAMPExtension{}
}

func (m *mockOpAMPExtension) Start(_ context.Context, _ component.Host) error { return nil }
func (m *mockOpAMPExtension) Shutdown(_ context.Context) error                { return nil }

func (m *mockOpAMPExtension) Register(capability string, _ ...opampcustommessages.CustomCapabilityRegisterOption) (opampcustommessages.CustomCapabilityHandler, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capability = capability
	m.registerCount++
	return m, nil
}

func (m *mockOpAMPExtension) Message() <-chan *protobufs.CustomMessage {
	return nil
}

func (m *mockOpAMPExtension) SendMessage(messageType string, message []byte) (chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sendCallCount++

	if m.sendErr != nil {
		return nil, m.sendErr
	}

	if m.pendingBeforeSuccess > 0 {
		m.pendingBeforeSuccess--
		ch := make(chan struct{})
		close(ch)
		return ch, types.ErrCustomMessagePending
	}

	m.sentMessages = append(m.sentMessages, sentMessage{
		messageType: messageType,
		payload:     append([]byte(nil), message...),
	})
	return nil, nil
}

func (m *mockOpAMPExtension) Unregister() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unregisterCall++
}

func (m *mockOpAMPExtension) registerCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registerCount
}

func (m *mockOpAMPExtension) sendCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sendCallCount
}

func (m *mockOpAMPExtension) messageCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sentMessages)
}

func (m *mockOpAMPExtension) waitForMessage(t *testing.T) sentMessage {
	t.Helper()
	var msg sentMessage
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.sentMessages) == 0 {
			return false
		}
		msg = m.sentMessages[0]
		return true
	}, time.Second, 10*time.Millisecond)
	return msg
}

// nonRegistryExtension is a component that is not an OpAMP custom capability registry.
type nonRegistryExtension struct{}

func (e *nonRegistryExtension) Start(_ context.Context, _ component.Host) error { return nil }
func (e *nonRegistryExtension) Shutdown(_ context.Context) error                { return nil }

// --- test data helpers ---

func generateTestLogs() plog.Logs {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("resource", "R1")
	l := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	l.Body().SetStr("test log message")
	l.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000000, 0)))
	return logs
}

func generateTestMetrics() pmetric.Metrics {
	metrics := pmetric.NewMetrics()
	rm := metrics.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("resource", "R1")
	m := rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty()
	m.SetName("test_metric")
	dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
	dp.Attributes().PutStr("test_attr", "value_1")
	dp.SetIntValue(123)
	dp.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000000, 0)))
	return metrics
}

func generateTestTraces() ptrace.Traces {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("resource", "R1")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.Attributes().PutStr("test_attr", "value_1")
	span.SetName("test_span")
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000000, 0)))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000001, 0)))
	return traces
}
