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
	memMu   sync.Mutex
	ttl     time.Duration
	enabled bool
	logger  *zap.Logger
}

// NewLookupCache wraps source with TTL caching. When enabled is false, the
// returned cache is a pass-through. signal is used to namespace the storage
// extension client per pipeline signal kind (logs/metrics/traces) so closing
// one processor instance's client does not affect another.
func NewLookupCache(
	ctx context.Context,
	source LookupSource,
	ttl time.Duration,
	enabled bool,
	storageID *component.ID,
	host component.Host,
	componentID component.ID,
	signal string,
	logger *zap.Logger,
) (*LookupCache, error) {
	cache := &LookupCache{
		source:  source,
		ttl:     ttl,
		enabled: enabled,
		logger:  logger,
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
		if time.Now().After(entry.ExpiresAt) {
			c.logger.Debug("cache entry expired", zap.String("key", key))
			if delErr := c.storage.Delete(ctx, cacheKey); delErr != nil {
				c.logger.Debug("failed to delete expired cache entry", zap.String("key", key), zap.Error(delErr))
			}
			return nil, false, nil
		}
		return entry.Data, true, nil
	}

	c.memMu.Lock()
	defer c.memMu.Unlock()
	entry, ok := c.mem[cacheKey]
	if !ok {
		return nil, false, nil
	}
	if time.Now().After(entry.ExpiresAt) {
		c.logger.Debug("cache entry expired", zap.String("key", key))
		delete(c.mem, cacheKey)
		return nil, false, nil
	}
	return entry.Data, true, nil
}

func (c *LookupCache) set(ctx context.Context, key string, data map[string]string) error {
	cacheKey := fmt.Sprintf("lookup:%s", key)
	entry := cacheEntry{
		Data:      data,
		ExpiresAt: time.Now().Add(c.ttl),
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
	c.memMu.Unlock()
	return nil
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
