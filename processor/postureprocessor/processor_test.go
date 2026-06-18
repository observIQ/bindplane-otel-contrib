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

package postureprocessor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor/processortest"

	"github.com/observiq/bindplane-otel-contrib/internal/posture"
)

// --- in-memory storage extension test double ---

type memClient struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemClient() *memClient { return &memClient{data: make(map[string][]byte)} }

func (m *memClient) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), v...), nil
}

func (m *memClient) Set(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte(nil), value...)
	return nil
}

func (m *memClient) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memClient) Batch(_ context.Context, ops ...*storage.Operation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range ops {
		switch op.Type {
		case storage.Get:
			if v, ok := m.data[op.Key]; ok {
				op.Value = append([]byte(nil), v...)
			} else {
				op.Value = nil
			}
		case storage.Set:
			m.data[op.Key] = append([]byte(nil), op.Value...)
		case storage.Delete:
			delete(m.data, op.Key)
		}
	}
	return nil
}

func (m *memClient) Close(context.Context) error { return nil }

type memStorage struct {
	mu      sync.Mutex
	clients map[string]*memClient
}

func newMemStorage() *memStorage { return &memStorage{clients: make(map[string]*memClient)} }

func (s *memStorage) Start(context.Context, component.Host) error { return nil }
func (s *memStorage) Shutdown(context.Context) error              { return nil }

func (s *memStorage) GetClient(_ context.Context, _ component.Kind, _ component.ID, name string) (storage.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[name]
	if !ok {
		c = newMemClient()
		s.clients[name] = c
	}
	return c, nil
}

// --- stub posture provider ---

type stubProvider struct {
	mu    sync.Mutex
	level posture.Level
	subs  []chan posture.Level
}

func (p *stubProvider) Start(context.Context, component.Host) error { return nil }
func (p *stubProvider) Shutdown(context.Context) error              { return nil }

func (p *stubProvider) Current() posture.Level {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.level
}

func (p *stubProvider) Subscribe() (<-chan posture.Level, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := make(chan posture.Level, 1)
	p.subs = append(p.subs, ch)
	return ch, func() {}
}

func (p *stubProvider) RecordExportResult(bool) {}

func (p *stubProvider) set(l posture.Level) {
	p.mu.Lock()
	p.level = l
	subs := append([]chan posture.Level(nil), p.subs...)
	p.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- l:
		default:
		}
	}
}

// --- helpers ---

const (
	storageID = "mem"
	postureID = "posture"
)

func testHost(t *testing.T, prov posture.Provider, store storage.Extension) component.Host {
	t.Helper()
	return hostWithExtensions{exts: map[component.ID]component.Component{
		component.MustNewID(storageID): store,
		component.MustNewID(postureID): prov.(component.Component),
	}}
}

type hostWithExtensions struct {
	exts map[component.ID]component.Component
}

func (h hostWithExtensions) GetExtensions() map[component.ID]component.Component { return h.exts }

func testConfig() *Config {
	sid := component.MustNewID(storageID)
	pid := component.MustNewID(postureID)
	return &Config{
		Levels:           posture.DefaultLevels,
		StorageID:        &sid,
		PostureExtension: &pid,
		DefaultMinLevel:  "full",
		Tiers: []TierConfig{
			{Name: "battle", Condition: `attributes["priority"] == "realtime"`, MinLevel: "silent"},
			{Name: "ops", Condition: `attributes["tier"] == "ops"`, MinLevel: "medium"},
		},
		Drain:  DrainConfig{Interval: time.Millisecond},
		Buffer: BufferConfig{OverflowPolicy: overflowDropOldest},
	}
}

func makeLogs(records ...map[string]string) plog.Logs {
	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	for _, attrs := range records {
		lr := sl.LogRecords().AppendEmpty()
		for k, v := range attrs {
			lr.Attributes().PutStr(k, v)
		}
	}
	return ld
}

func countRecords(all []plog.Logs) int {
	total := 0
	for _, ld := range all {
		for i := 0; i < ld.ResourceLogs().Len(); i++ {
			rl := ld.ResourceLogs().At(i)
			for j := 0; j < rl.ScopeLogs().Len(); j++ {
				total += rl.ScopeLogs().At(j).LogRecords().Len()
			}
		}
	}
	return total
}

func TestLogsGateAndDrain(t *testing.T) {
	cfg := testConfig()
	require.NoError(t, cfg.Validate())

	prov := &stubProvider{level: 0} // silent
	store := newMemStorage()
	host := testHost(t, prov, store)
	sink := &consumertest.LogsSink{}

	p, err := createLogsProcessor(context.Background(), processortest.NewNopSettings(componentType), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, p.Start(context.Background(), host))
	t.Cleanup(func() { require.NoError(t, p.Shutdown(context.Background())) })

	// At silent: only the battle record egresses; ops and default are buffered.
	ld := makeLogs(
		map[string]string{"priority": "realtime"},
		map[string]string{"tier": "ops"},
		map[string]string{"other": "x"},
	)
	require.NoError(t, p.ConsumeLogs(context.Background(), ld))
	assert.Equal(t, 1, countRecords(sink.AllLogs()), "only battle should egress at silent")

	// Raise to full: the buffered ops + default records drain to the same sink.
	prov.set(3) // full
	require.Eventually(t, func() bool {
		return countRecords(sink.AllLogs()) == 3
	}, 3*time.Second, 10*time.Millisecond, "buffered records should drain after posture rises")
}

func TestLogsForwardAllWhenPermissive(t *testing.T) {
	cfg := testConfig()
	prov := &stubProvider{level: 3} // full
	store := newMemStorage()
	host := testHost(t, prov, store)
	sink := &consumertest.LogsSink{}

	p, err := createLogsProcessor(context.Background(), processortest.NewNopSettings(componentType), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, p.Start(context.Background(), host))
	t.Cleanup(func() { require.NoError(t, p.Shutdown(context.Background())) })

	ld := makeLogs(
		map[string]string{"priority": "realtime"},
		map[string]string{"tier": "ops"},
		map[string]string{"other": "x"},
	)
	require.NoError(t, p.ConsumeLogs(context.Background(), ld))
	assert.Equal(t, 3, countRecords(sink.AllLogs()), "everything egresses at full")
}

func TestRestartRecovery(t *testing.T) {
	cfg := testConfig()
	prov := &stubProvider{level: 0} // silent
	store := newMemStorage()        // persists across processor instances
	host := testHost(t, prov, store)

	// First instance: buffer deferrable data while restricted, then shut down.
	sink1 := &consumertest.LogsSink{}
	p1, err := createLogsProcessor(context.Background(), processortest.NewNopSettings(componentType), cfg, sink1)
	require.NoError(t, err)
	require.NoError(t, p1.Start(context.Background(), host))
	require.NoError(t, p1.ConsumeLogs(context.Background(), makeLogs(
		map[string]string{"tier": "ops"},
		map[string]string{"other": "x"},
	)))
	assert.Equal(t, 0, countRecords(sink1.AllLogs()))
	require.NoError(t, p1.Shutdown(context.Background()))

	// Second instance against the same storage: raise posture, backlog drains.
	prov2 := &stubProvider{level: 3} // full
	host2 := testHost(t, prov2, store)
	sink2 := &consumertest.LogsSink{}
	p2, err := createLogsProcessor(context.Background(), processortest.NewNopSettings(componentType), cfg, sink2)
	require.NoError(t, err)
	require.NoError(t, p2.Start(context.Background(), host2))
	t.Cleanup(func() { require.NoError(t, p2.Shutdown(context.Background())) })

	require.Eventually(t, func() bool {
		return countRecords(sink2.AllLogs()) == 2
	}, 3*time.Second, 10*time.Millisecond, "backlog should survive restart and drain")
}
