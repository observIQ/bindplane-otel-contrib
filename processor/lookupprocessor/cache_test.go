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

package lookupprocessor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.uber.org/zap"
)

// fakeSource is a controllable LookupSource for tests.
type fakeSource struct {
	calls   int
	data    map[string]map[string]string
	loadErr error
}

func (f *fakeSource) Lookup(_ context.Context, key string) (map[string]string, error) {
	f.calls++
	v, ok := f.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}

func (f *fakeSource) Load() error  { return f.loadErr }
func (f *fakeSource) Close() error { return nil }

func newComponentID(t *testing.T, name string) component.ID {
	t.Helper()
	typ, err := component.NewType("lookup")
	require.NoError(t, err)
	return component.NewIDWithName(typ, name)
}

func TestLookupCache_Disabled_Passthrough(t *testing.T) {
	fs := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	c, err := NewLookupCache(context.Background(), fs, time.Minute, 0, false, nil, nil, newComponentID(t, "disabled"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.Lookup(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"a": "1"}, got)
	require.Equal(t, 1, fs.calls)

	// Second call also hits source because cache is disabled.
	_, _ = c.Lookup(context.Background(), "k")
	require.Equal(t, 2, fs.calls)
}

func TestLookupCache_InMemory_HitMissExpiry(t *testing.T) {
	fs := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	c, err := NewLookupCache(context.Background(), fs, 500*time.Millisecond, 0, true, nil, nil, newComponentID(t, "mem"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	fclock := clockwork.NewFakeClock()
	c.clock = fclock

	// First call populates cache.
	_, err = c.Lookup(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, 1, fs.calls)

	// Second call within TTL is a hit.
	_, err = c.Lookup(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, 1, fs.calls)

	// Advance past the TTL, expect another source call.
	fclock.Advance(600 * time.Millisecond)
	_, err = c.Lookup(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, 2, fs.calls)
}

func TestLookupCache_InMemory_ExpiredEntryMissesWithoutMutation(t *testing.T) {
	fs := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	c, err := NewLookupCache(context.Background(), fs, 50*time.Millisecond, 0, true, nil, nil, newComponentID(t, "evict"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	fclock := clockwork.NewFakeClock()
	c.clock = fclock

	_, err = c.Lookup(context.Background(), "k")
	require.NoError(t, err)
	require.Len(t, c.mem, 1)

	fclock.Advance(60 * time.Millisecond)

	// An expired entry is a miss, but the read path must not mutate the map;
	// reclamation happens on insert overflow instead.
	_, found, err := c.get(context.Background(), "k")
	require.NoError(t, err)
	require.False(t, found)
	require.Len(t, c.mem, 1, "reads must not delete expired entries")

	// A full Lookup re-fetches from the source and refreshes the entry in place.
	_, err = c.Lookup(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, 2, fs.calls)
	require.Len(t, c.mem, 1)
	_, found, err = c.get(context.Background(), "k")
	require.NoError(t, err)
	require.True(t, found, "refreshed entry must be a hit again")
}

// keyedSource returns synthetic data for any key, so unique keys always
// populate the cache.
type keyedSource struct{}

func (keyedSource) Lookup(_ context.Context, key string) (map[string]string, error) {
	return map[string]string{"v": key}, nil
}
func (keyedSource) Load() error  { return nil }
func (keyedSource) Close() error { return nil }

func TestLookupCache_InMemory_BoundedAtMaxEntries(t *testing.T) {
	c, err := NewLookupCache(context.Background(), keyedSource{}, time.Minute, 100, true, nil, nil, newComponentID(t, "bounded"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	for i := 0; i < 500; i++ {
		_, err := c.Lookup(context.Background(), fmt.Sprintf("key-%d", i))
		require.NoError(t, err)
	}

	require.LessOrEqual(t, len(c.mem), 100, "in-memory cache must never exceed maxEntries")

	// The most recent insert must survive its own overflow eviction.
	_, found, err := c.get(context.Background(), "key-499")
	require.NoError(t, err)
	require.True(t, found)
}

func TestLookupCache_InMemory_EvictsExpiredBeforeLive(t *testing.T) {
	c, err := NewLookupCache(context.Background(), keyedSource{}, time.Minute, 10, true, nil, nil, newComponentID(t, "expired-first"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	fclock := clockwork.NewFakeClock()
	c.clock = fclock

	for i := 0; i < 5; i++ {
		_, err := c.Lookup(context.Background(), fmt.Sprintf("old-%d", i))
		require.NoError(t, err)
	}

	fclock.Advance(2 * time.Minute)

	// Six live inserts push the map to 11 entries; the overflow sweep must
	// reclaim the five expired entries and keep every live one.
	for i := 0; i < 6; i++ {
		_, err := c.Lookup(context.Background(), fmt.Sprintf("new-%d", i))
		require.NoError(t, err)
	}

	require.Len(t, c.mem, 6, "expired entries must be reclaimed before live ones")
	for i := 0; i < 6; i++ {
		_, found, err := c.get(context.Background(), fmt.Sprintf("new-%d", i))
		require.NoError(t, err)
		require.True(t, found, "live entries must survive when expired ones can be evicted instead")
	}
}

func TestLookupCache_InMemory_EvictsArbitraryWhenNoneExpired(t *testing.T) {
	c, err := NewLookupCache(context.Background(), keyedSource{}, time.Hour, 10, true, nil, nil, newComponentID(t, "arbitrary"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	for i := 0; i < 11; i++ {
		_, err := c.Lookup(context.Background(), fmt.Sprintf("key-%d", i))
		require.NoError(t, err)
	}

	// Overflow with nothing expired evicts arbitrary entries down to 90% of the
	// cap, never the key that was just inserted.
	require.Len(t, c.mem, 9)
	_, found, err := c.get(context.Background(), "key-10")
	require.NoError(t, err)
	require.True(t, found)
}

func TestLookupCache_InMemory_MaxEntriesOne(t *testing.T) {
	c, err := NewLookupCache(context.Background(), keyedSource{}, time.Hour, 1, true, nil, nil, newComponentID(t, "one"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	for i := 0; i < 3; i++ {
		_, err := c.Lookup(context.Background(), fmt.Sprintf("key-%d", i))
		require.NoError(t, err)
	}

	// The smallest cap still holds exactly the most recent entry.
	require.Len(t, c.mem, 1)
	_, found, err := c.get(context.Background(), "key-2")
	require.NoError(t, err)
	require.True(t, found)
}

func TestNewLookupCache_DefaultsForNonPositiveArgs(t *testing.T) {
	c, err := NewLookupCache(context.Background(), keyedSource{}, 0, 0, true, nil, nil, newComponentID(t, "defaults"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.Equal(t, defaultCacheTTL, c.ttl)
	require.Equal(t, defaultCacheMaxEntries, c.maxEntries)
}

func TestLookupCache_InMemory_ConcurrentLookups(t *testing.T) {
	c, err := NewLookupCache(context.Background(), keyedSource{}, time.Minute, 64, true, nil, nil, newComponentID(t, "concurrent"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// One error slot per goroutine, checked after Wait: require must not run
	// off the test goroutine.
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				// Overlapping key space mixes hits, misses, and evictions.
				if _, err := c.Lookup(context.Background(), fmt.Sprintf("key-%d", (g*500+i)%256)); err != nil && errs[g] == nil {
					errs[g] = err
				}
			}
		}(g)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	require.LessOrEqual(t, len(c.mem), 64)
}

// BenchmarkLookupCache_ParallelHits exercises concurrent reads of cached
// entries; run with -cpu 1,4,8 to observe read-path scaling.
func BenchmarkLookupCache_ParallelHits(b *testing.B) {
	typ, err := component.NewType("lookup")
	require.NoError(b, err)
	c, err := NewLookupCache(context.Background(), keyedSource{}, time.Hour, 0, true, nil, nil, component.NewIDWithName(typ, "bench"), "logs", zap.NewNop())
	require.NoError(b, err)
	defer func() { _ = c.Close() }()

	const nkeys = 1024
	keys := make([]string, nkeys)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
		_, _ = c.Lookup(context.Background(), keys[i])
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = c.Lookup(context.Background(), keys[i%nkeys])
			i++
		}
	})
}

func TestLookupCache_StorageExtension_ExpiredEntryDeleted(t *testing.T) {
	storageType, err := component.NewType("file_storage")
	require.NoError(t, err)
	storageID := component.NewID(storageType)

	ext := newFakeStorageExtension()
	host := newFakeHost(storageID, ext)

	fs := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	c, err := NewLookupCache(context.Background(), fs, 50*time.Millisecond, 0, true, &storageID, host, newComponentID(t, "evict-stor"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	fclock := clockwork.NewFakeClock()
	c.clock = fclock

	_, err = c.Lookup(context.Background(), "k")
	require.NoError(t, err)

	client := ext.clients["logs"]
	require.NotNil(t, client)
	require.Len(t, client.data, 1)

	fclock.Advance(60 * time.Millisecond)

	_, found, err := c.get(context.Background(), "k")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, client.data, "expired entries must be deleted from the storage client")
}

func TestLookupCache_SourceErrorNotCached(t *testing.T) {
	fs := &fakeSource{data: map[string]map[string]string{}}
	c, err := NewLookupCache(context.Background(), fs, time.Minute, 0, true, nil, nil, newComponentID(t, "err"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Lookup(context.Background(), "missing")
	require.Error(t, err)

	_, err = c.Lookup(context.Background(), "missing")
	require.Error(t, err)
	require.Equal(t, 2, fs.calls, "errors must not be cached")
}

// fakeStorageClient is a minimal in-memory storage.Client.
type fakeStorageClient struct {
	mu     sync.Mutex
	data   map[string][]byte
	closed bool
}

func newFakeStorageClient() *fakeStorageClient {
	return &fakeStorageClient{data: map[string][]byte{}}
}

func (f *fakeStorageClient) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (f *fakeStorageClient) Set(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = value
	return nil
}

func (f *fakeStorageClient) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *fakeStorageClient) Batch(_ context.Context, ops ...*storage.Operation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, op := range ops {
		switch op.Type {
		case storage.Get:
			op.Value = f.data[op.Key]
		case storage.Set:
			f.data[op.Key] = op.Value
		case storage.Delete:
			delete(f.data, op.Key)
		}
	}
	return nil
}

func (f *fakeStorageClient) Close(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// fakeStorageExtension records every GetClient call with its name argument.
type fakeStorageExtension struct {
	extension.Extension
	mu      sync.Mutex
	clients map[string]*fakeStorageClient
	names   []string
}

func newFakeStorageExtension() *fakeStorageExtension {
	return &fakeStorageExtension{clients: map[string]*fakeStorageClient{}}
}

func (f *fakeStorageExtension) GetClient(_ context.Context, _ component.Kind, _ component.ID, name string) (storage.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names = append(f.names, name)
	c := newFakeStorageClient()
	f.clients[name] = c
	return c, nil
}

func (f *fakeStorageExtension) Start(context.Context, component.Host) error { return nil }
func (f *fakeStorageExtension) Shutdown(context.Context) error              { return nil }

// fakeHost exposes a single named extension.
type fakeHost struct {
	exts map[component.ID]component.Component
}

func newFakeHost(id component.ID, ext component.Component) *fakeHost {
	return &fakeHost{exts: map[component.ID]component.Component{id: ext}}
}

func (h *fakeHost) GetExtensions() map[component.ID]component.Component {
	return h.exts
}

func TestLookupCache_StorageExtension_HitMissExpiryAndNaming(t *testing.T) {
	storageType, err := component.NewType("file_storage")
	require.NoError(t, err)
	storageID := component.NewID(storageType)

	ext := newFakeStorageExtension()
	host := newFakeHost(storageID, ext)

	cid := newComponentID(t, "shared")

	// Two cache instances using the same component ID but different signal
	// names; each must get its own storage client.
	fs := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	logsCache, err := NewLookupCache(context.Background(), fs, 200*time.Millisecond, 0, true, &storageID, host, cid, "logs", zap.NewNop())
	require.NoError(t, err)
	fclock := clockwork.NewFakeClock()
	logsCache.clock = fclock

	fs2 := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	tracesCache, err := NewLookupCache(context.Background(), fs2, 200*time.Millisecond, 0, true, &storageID, host, cid, "traces", zap.NewNop())
	require.NoError(t, err)

	require.Equal(t, []string{"logs", "traces"}, ext.names, "GetClient must be called with the signal-specific name for each instance")

	// Populate via lookup, then second lookup must hit cache (no extra source call).
	_, err = logsCache.Lookup(context.Background(), "k")
	require.NoError(t, err)
	_, err = logsCache.Lookup(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, 1, fs.calls)

	// Advance past the TTL and re-fetch.
	fclock.Advance(210 * time.Millisecond)
	_, err = logsCache.Lookup(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, 2, fs.calls)

	// Closing the logs instance must not close the traces client.
	require.NoError(t, logsCache.Close())
	require.True(t, ext.clients["logs"].closed)
	require.False(t, ext.clients["traces"].closed)

	require.NoError(t, tracesCache.Close())
	require.True(t, ext.clients["traces"].closed)
}

func TestLookupCache_StorageExtension_NotFound(t *testing.T) {
	storageType, err := component.NewType("file_storage")
	require.NoError(t, err)
	missing := component.NewIDWithName(storageType, "missing")
	host := newFakeHost(component.NewID(storageType), newFakeStorageExtension())

	_, err = NewLookupCache(context.Background(), &fakeSource{}, time.Minute, 0, true, &missing, host, newComponentID(t, "x"), "logs", zap.NewNop())
	require.Error(t, err)
}

func TestLookupCache_LoadAndClose(t *testing.T) {
	fs := &fakeSource{loadErr: errors.New("load fail")}
	c, err := NewLookupCache(context.Background(), fs, time.Minute, 0, true, nil, nil, newComponentID(t, "load"), "logs", zap.NewNop())
	require.NoError(t, err)

	require.Error(t, c.Load())
	require.NoError(t, c.Close())
}
