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

	require.Equal(t, defaultCapability, mockOpamp.capability)

	logs := generateTestLogs()
	require.NoError(t, e.ConsumeLogs(context.Background(), logs))

	msg := mockOpamp.waitForMessage(t)
	require.Equal(t, defaultMessageType, msg.messageType)

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

	require.Equal(t, defaultCapability, mockOpamp.capability)

	metrics := generateTestMetrics()
	require.NoError(t, e.ConsumeMetrics(context.Background(), metrics))

	msg := mockOpamp.waitForMessage(t)
	require.Equal(t, defaultMessageType, msg.messageType)

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

	require.Equal(t, defaultCapability, mockOpamp.capability)

	traces := generateTestTraces()
	require.NoError(t, e.ConsumeTraces(context.Background(), traces))

	msg := mockOpamp.waitForMessage(t)
	require.Equal(t, defaultMessageType, msg.messageType)

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

func TestExporter_CustomCapabilityAndMessageType(t *testing.T) {
	factory := NewFactory()
	set := exportertest.NewNopSettings(metadata.Type)
	set.ID = component.NewIDWithName(metadata.Type, "custom")

	cfg := factory.CreateDefaultConfig().(*Config)
	cfg.CustomMessage.Capability = "com.example.throughput"
	cfg.CustomMessage.Type = "throughput-proto"

	e, err := factory.CreateLogs(context.Background(), set, cfg)
	require.NoError(t, err)

	mockOpamp := newMockOpAMPExtension()
	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}

	require.NoError(t, e.Start(context.Background(), host))
	t.Cleanup(func() {
		require.NoError(t, e.Shutdown(context.Background()))
	})

	require.Equal(t, "com.example.throughput", mockOpamp.capability)

	require.NoError(t, e.ConsumeLogs(context.Background(), generateTestLogs()))

	msg := mockOpamp.waitForMessage(t)
	require.Equal(t, "throughput-proto", msg.messageType)
}

func TestExporter_MaxQueuedMessagesPassedToRegister(t *testing.T) {
	factory := NewFactory()
	set := exportertest.NewNopSettings(metadata.Type)
	set.ID = component.NewIDWithName(metadata.Type, "max-queued")

	cfg := factory.CreateDefaultConfig().(*Config)
	cfg.MaxQueuedMessages = 42

	e, err := factory.CreateLogs(context.Background(), set, cfg)
	require.NoError(t, err)

	mockOpamp := newMockOpAMPExtension()
	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}

	require.NoError(t, e.Start(context.Background(), host))
	t.Cleanup(func() {
		require.NoError(t, e.Shutdown(context.Background()))
	})

	require.NotNil(t, mockOpamp.registerOptions)
	require.Equal(t, 42, mockOpamp.registerOptions.MaxQueuedMessages)
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

func TestExporter_Shutdown_DrainsInflightSends(t *testing.T) {
	e := newOpAMPExporter(exportertest.NewNopSettings(metadata.Type).Logger,
		createDefaultConfig().(*Config),
		component.NewIDWithName(metadata.Type, "drain"))
	t.Cleanup(func() { unregisterExporter(e.exporterID) })

	mockOpamp := newMockOpAMPExtension()
	// SendMessage will return ErrCustomMessagePending with a channel that
	// the test never closes — the consume goroutine should park in its
	// retry loop waiting on that channel.
	mockOpamp.holdPendingChan = make(chan struct{})

	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}
	require.NoError(t, e.start(context.Background(), host))

	consumeErr := make(chan error, 1)
	go func() {
		consumeErr <- e.consumeLogs(context.Background(), generateTestLogs())
	}()

	// Wait for SendMessage to be called at least once so we know the
	// goroutine is parked in the retry loop.
	require.Eventually(t, func() bool {
		return mockOpamp.sendCalls() >= 1
	}, time.Second, 10*time.Millisecond)

	// Shutdown must wait for the consume to observe `done` and return.
	// `Unregister` must not run until after the consume has drained.
	require.NoError(t, e.shutdown(context.Background()))

	require.ErrorIs(t, <-consumeErr, errShuttingDown)
	require.Equal(t, 1, mockOpamp.unregisterCalls())
}

func TestExporter_Shutdown_BoundedByContext(t *testing.T) {
	e := newOpAMPExporter(exportertest.NewNopSettings(metadata.Type).Logger,
		createDefaultConfig().(*Config),
		component.NewIDWithName(metadata.Type, "bounded"))
	t.Cleanup(func() { unregisterExporter(e.exporterID) })

	mockOpamp := newMockOpAMPExtension()
	// Block SendMessage itself so the consume goroutine cannot observe
	// `done` — it's stuck inside the handler call. Shutdown should still
	// unblock when its context is cancelled.
	mockOpamp.blockSend = make(chan struct{})
	t.Cleanup(func() { close(mockOpamp.blockSend) })

	host := &mockHost{extensions: map[component.ID]component.Component{
		component.MustNewID("opamp"): mockOpamp,
	}}
	require.NoError(t, e.start(context.Background(), host))

	go func() {
		_ = e.consumeLogs(context.Background(), generateTestLogs())
	}()

	require.Eventually(t, func() bool {
		return mockOpamp.sendCalls() >= 1
	}, time.Second, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := e.shutdown(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
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

	capability      string
	registerOptions *opampcustommessages.CustomCapabilityRegisterOptions
	registerCount   int
	sendCallCount   int
	sentMessages    []sentMessage
	sendErr         error
	unregisterCall  int

	// pendingBeforeSuccess controls how many SendMessage calls return
	// ErrCustomMessagePending before the first successful send.
	pendingBeforeSuccess int

	// holdPendingChan, when non-nil, is returned by SendMessage along with
	// ErrCustomMessagePending so callers block indefinitely waiting for a
	// pending-send slot (until the test closes the channel).
	holdPendingChan chan struct{}

	// blockSend, when non-nil, blocks SendMessage itself until the channel
	// is closed. Used to simulate a consume that can't observe shutdown
	// because it is stuck inside the handler call.
	blockSend chan struct{}
}

func newMockOpAMPExtension() *mockOpAMPExtension {
	return &mockOpAMPExtension{}
}

func (m *mockOpAMPExtension) Start(_ context.Context, _ component.Host) error { return nil }
func (m *mockOpAMPExtension) Shutdown(_ context.Context) error                { return nil }

func (m *mockOpAMPExtension) Register(capability string, opts ...opampcustommessages.CustomCapabilityRegisterOption) (opampcustommessages.CustomCapabilityHandler, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capability = capability
	m.registerCount++
	resolved := opampcustommessages.DefaultCustomCapabilityRegisterOptions()
	for _, opt := range opts {
		opt(resolved)
	}
	m.registerOptions = resolved
	return m, nil
}

func (m *mockOpAMPExtension) Message() <-chan *protobufs.CustomMessage {
	return nil
}

func (m *mockOpAMPExtension) SendMessage(messageType string, message []byte) (chan struct{}, error) {
	m.mu.Lock()
	m.sendCallCount++
	sendErr := m.sendErr
	holdChan := m.holdPendingChan
	blockSend := m.blockSend
	pendingBeforeSuccess := m.pendingBeforeSuccess
	if pendingBeforeSuccess > 0 {
		m.pendingBeforeSuccess--
	}
	m.mu.Unlock()

	if blockSend != nil {
		<-blockSend
	}

	if sendErr != nil {
		return nil, sendErr
	}

	if holdChan != nil {
		return holdChan, types.ErrCustomMessagePending
	}

	if pendingBeforeSuccess > 0 {
		ch := make(chan struct{})
		close(ch)
		return ch, types.ErrCustomMessagePending
	}

	m.mu.Lock()
	m.sentMessages = append(m.sentMessages, sentMessage{
		messageType: messageType,
		payload:     append([]byte(nil), message...),
	})
	m.mu.Unlock()
	return nil, nil
}

func (m *mockOpAMPExtension) Unregister() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unregisterCall++
}

func (m *mockOpAMPExtension) unregisterCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unregisterCall
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
