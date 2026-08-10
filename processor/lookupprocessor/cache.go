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
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.uber.org/zap"
)

// cacheEntry represents a cached lookup result with an absolute expiry time.
type cacheEntry struct {
	Data      map[string]string `json:"data"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// LookupCache wraps a LookupSource with caching. When a storage extension is
// configured, the cache persists through it; otherwise it falls back to a
// per-instance in-memory map. Each processor instance owns its cache state,
// so there are no cross-pipeline collisions.
type LookupCache struct {
	source  LookupSource
	storage storage.Client
	mem     map[string]cacheEntry
	memMu   sync.RWMutex
	// maxEntries bounds mem. Reads never mutate the map, so reclamation happens
	// on insert: when an insert pushes the map over maxEntries, expired entries
	// are evicted first, then arbitrary ones. Applies only to the in-memory
	// backend; a storage extension manages its own retention.
	maxEntries int
	ttl        time.Duration
	enabled    bool
	logger     *zap.Logger
	// clock backs TTL expiry checks; real in production, a fake clock in tests so
	// expiry can be exercised without sleeping.
	clock clockwork.Clock
}

// NewLookupCache wraps source with TTL caching. When enabled is false, the
// returned cache is a pass-through. signal is used to namespace the storage
// extension client per pipeline signal kind (logs/metrics/traces) so closing
// one processor instance's client does not affect another. maxEntries bounds
// the in-memory backend; values < 1 fall back to defaultCacheMaxEntries.
func NewLookupCache(
	ctx context.Context,
	source LookupSource,
	ttl time.Duration,
	maxEntries int,
	enabled bool,
	storageID *component.ID,
	host component.Host,
	componentID component.ID,
	signal string,
	logger *zap.Logger,
) (*LookupCache, error) {
	if maxEntries < 1 {
		maxEntries = defaultCacheMaxEntries
	}
	cache := &LookupCache{
		source:     source,
		ttl:        ttl,
		maxEntries: maxEntries,
		enabled:    enabled,
		logger:     logger,
		clock:      clockwork.NewRealClock(),
	}

	if !enabled {
		return cache, nil
	}

	if storageID != nil {
		client, err := getStorageClient(ctx, host, *storageID, componentID, signal)
		if err != nil {
			return nil, fmt.Errorf("failed to get storage client: %w", err)
		}
		cache.storage = client
		return cache, nil
	}

	cache.mem = make(map[string]cacheEntry)
	return cache, nil
}

// Lookup checks the cache first, then falls back to the source on miss.
func (c *LookupCache) Lookup(ctx context.Context, key string) (map[string]string, error) {
	if !c.enabled {
		return c.source.Lookup(ctx, key)
	}

	cachedData, found, err := c.get(ctx, key)
	if err != nil {
		c.logger.Debug("cache lookup error, falling back to source", zap.Error(err))
	} else if found {
		c.logger.Debug("cache hit", zap.String("key", key))
		return cachedData, nil
	}

	c.logger.Debug("cache miss", zap.String("key", key))
	data, err := c.source.Lookup(ctx, key)
	if err != nil {
		return nil, err
	}

	if storeErr := c.set(ctx, key, data); storeErr != nil {
		c.logger.Debug("failed to cache result", zap.Error(storeErr))
	}

	return data, nil
}

// Load forwards to the wrapped source.
func (c *LookupCache) Load() error {
	return c.source.Load()
}

// Close cleans up resources for both source and cache backend. The in-memory
// map needs no teardown; the storage client is owned per processor instance.
func (c *LookupCache) Close() error {
	var errs []error

	if c.source != nil {
		if err := c.source.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close source: %w", err))
		}
	}

	if c.storage != nil {
		if err := c.storage.Close(context.Background()); err != nil {
			errs = append(errs, fmt.Errorf("failed to close storage client: %w", err))
		}
	}

	return errors.Join(errs...)
}

func (c *LookupCache) get(ctx context.Context, key string) (map[string]string, bool, error) {
	cacheKey := fmt.Sprintf("lookup:%s", key)

	if c.storage != nil {
		data, err := c.storage.Get(ctx, cacheKey)
		if err != nil {
			return nil, false, err
		}
		if data == nil {
			return nil, false, nil
		}
		var entry cacheEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil, false, fmt.Errorf("failed to unmarshal cache entry: %w", err)
		}
		if c.clock.Now().After(entry.ExpiresAt) {
			c.logger.Debug("cache entry expired", zap.String("key", key))
			if delErr := c.storage.Delete(ctx, cacheKey); delErr != nil {
				c.logger.Debug("failed to delete expired cache entry", zap.String("key", key), zap.Error(delErr))
			}
			return nil, false, nil
		}
		return entry.Data, true, nil
	}

	// Reads take only the shared lock and never mutate the map, so concurrent
	// hits do not serialize. An expired entry is reported as a miss and left in
	// place; it is reclaimed when overwritten by the refreshed value or when an
	// insert pushes the map over maxEntries.
	c.memMu.RLock()
	entry, ok := c.mem[cacheKey]
	c.memMu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	if c.clock.Now().After(entry.ExpiresAt) {
		c.logger.Debug("cache entry expired", zap.String("key", key))
		return nil, false, nil
	}
	return entry.Data, true, nil
}

func (c *LookupCache) set(ctx context.Context, key string, data map[string]string) error {
	cacheKey := fmt.Sprintf("lookup:%s", key)
	entry := cacheEntry{
		Data:      data,
		ExpiresAt: c.clock.Now().Add(c.ttl),
	}

	if c.storage != nil {
		entryData, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("failed to marshal cache entry: %w", err)
		}
		return c.storage.Set(ctx, cacheKey, entryData)
	}

	c.memMu.Lock()
	c.mem[cacheKey] = entry
	if len(c.mem) > c.maxEntries {
		c.evictLocked(cacheKey)
	}
	c.memMu.Unlock()
	return nil
}

// evictLocked reclaims in-memory entries after an insert exceeds maxEntries.
// Expired entries are swept first; if the map is still over 90% of the cap,
// arbitrary entries are evicted down to that mark. Evicting in a batch keeps
// the O(n) sweep rare instead of running on every insert once the cap is hit.
// Map-order eviction is deliberate: an LRU would need a write on every read,
// which is what the shared read lock exists to avoid. keep is the key that was
// just inserted and is never evicted. Callers must hold memMu.
func (c *LookupCache) evictLocked(keep string) {
	now := c.clock.Now()
	for k, e := range c.mem {
		if now.After(e.ExpiresAt) {
			delete(c.mem, k)
		}
	}

	target := c.maxEntries * 9 / 10
	for k := range c.mem {
		if len(c.mem) <= target {
			break
		}
		if k == keep {
			continue
		}
		delete(c.mem, k)
	}
}

func getStorageClient(ctx context.Context, host component.Host, storageID component.ID, componentID component.ID, signal string) (storage.Client, error) {
	extension, ok := host.GetExtensions()[storageID]
	if !ok {
		return nil, fmt.Errorf("storage extension '%s' not found", storageID)
	}

	storageExtension, ok := extension.(storage.Extension)
	if !ok {
		return nil, fmt.Errorf("extension '%s' is not a storage extension", storageID)
	}

	client, err := storageExtension.GetClient(ctx, component.KindProcessor, componentID, signal)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage client: %w", err)
	}

	return client, nil
}
