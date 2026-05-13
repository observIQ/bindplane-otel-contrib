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
	"sync"
	"testing"
	"time"

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

func (f *fakeSource) Lookup(key string) (map[string]string, error) {
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
	c, err := NewLookupCache(context.Background(), fs, time.Minute, false, nil, nil, newComponentID(t, "disabled"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.Lookup("k")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"a": "1"}, got)
	require.Equal(t, 1, fs.calls)

	// Second call also hits source because cache is disabled.
	_, _ = c.Lookup("k")
	require.Equal(t, 2, fs.calls)
}

func TestLookupCache_InMemory_HitMissExpiry(t *testing.T) {
	fs := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	c, err := NewLookupCache(context.Background(), fs, 500*time.Millisecond, true, nil, nil, newComponentID(t, "mem"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// First call populates cache.
	_, err = c.Lookup("k")
	require.NoError(t, err)
	require.Equal(t, 1, fs.calls)

	// Second call within TTL is a hit.
	_, err = c.Lookup("k")
	require.NoError(t, err)
	require.Equal(t, 1, fs.calls)

	// Wait past TTL, expect another source call.
	time.Sleep(1100 * time.Millisecond)
	_, err = c.Lookup("k")
	require.NoError(t, err)
	require.Equal(t, 2, fs.calls)
}

func TestLookupCache_InMemory_ExpiredEntryDeleted(t *testing.T) {
	fs := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	c, err := NewLookupCache(context.Background(), fs, 50*time.Millisecond, true, nil, nil, newComponentID(t, "evict"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Lookup("k")
	require.NoError(t, err)
	require.Len(t, c.mem, 1)

	time.Sleep(100 * time.Millisecond)

	// Lookup a different key after expiry; the expired entry for "k" must be
	// evicted as a side effect of the get() path even though we are not
	// reading "k" again — verify by calling get("k") directly.
	_, found, err := c.get("k")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, c.mem, "expired entries must be deleted from the in-memory map")
}

func TestLookupCache_StorageExtension_ExpiredEntryDeleted(t *testing.T) {
	storageType, err := component.NewType("file_storage")
	require.NoError(t, err)
	storageID := component.NewID(storageType)

	ext := newFakeStorageExtension()
	host := newFakeHost(storageID, ext)

	fs := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	c, err := NewLookupCache(context.Background(), fs, 50*time.Millisecond, true, &storageID, host, newComponentID(t, "evict-stor"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Lookup("k")
	require.NoError(t, err)

	client := ext.clients["logs"]
	require.NotNil(t, client)
	require.Len(t, client.data, 1)

	time.Sleep(100 * time.Millisecond)

	_, found, err := c.get("k")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, client.data, "expired entries must be deleted from the storage client")
}

func TestLookupCache_SourceErrorNotCached(t *testing.T) {
	fs := &fakeSource{data: map[string]map[string]string{}}
	c, err := NewLookupCache(context.Background(), fs, time.Minute, true, nil, nil, newComponentID(t, "err"), "logs", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Lookup("missing")
	require.Error(t, err)

	_, err = c.Lookup("missing")
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
	logsCache, err := NewLookupCache(context.Background(), fs, 200*time.Millisecond, true, &storageID, host, cid, "logs", zap.NewNop())
	require.NoError(t, err)

	fs2 := &fakeSource{data: map[string]map[string]string{"k": {"a": "1"}}}
	tracesCache, err := NewLookupCache(context.Background(), fs2, 200*time.Millisecond, true, &storageID, host, cid, "traces", zap.NewNop())
	require.NoError(t, err)

	require.Equal(t, []string{"logs", "traces"}, ext.names, "GetClient must be called with the signal-specific name for each instance")

	// Populate via lookup, then second lookup must hit cache (no extra source call).
	_, err = logsCache.Lookup("k")
	require.NoError(t, err)
	_, err = logsCache.Lookup("k")
	require.NoError(t, err)
	require.Equal(t, 1, fs.calls)

	// Expire and re-fetch.
	time.Sleep(250 * time.Millisecond)
	_, err = logsCache.Lookup("k")
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

	_, err = NewLookupCache(context.Background(), &fakeSource{}, time.Minute, true, &missing, host, newComponentID(t, "x"), "logs", zap.NewNop())
	require.Error(t, err)
}

func TestLookupCache_LoadAndClose(t *testing.T) {
	fs := &fakeSource{loadErr: errors.New("load fail")}
	c, err := NewLookupCache(context.Background(), fs, time.Minute, true, nil, nil, newComponentID(t, "load"), "logs", zap.NewNop())
	require.NoError(t, err)

	require.Error(t, c.Load())
	require.NoError(t, c.Close())
}
