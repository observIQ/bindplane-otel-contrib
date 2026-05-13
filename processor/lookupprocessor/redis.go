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
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// redisLookupTimeout bounds each Redis call so a slow or unreachable server
// cannot stall pipeline processing indefinitely.
const redisLookupTimeout = 5 * time.Second

// redisBootPingTimeout bounds the best-effort startup Ping. A failure here is
// logged as a warning so config errors (bad address, auth) surface at boot,
// but does not block source creation — the cache can serve through outages
// and uncached keys will error per-lookup.
const redisBootPingTimeout = 2 * time.Second

var errRedisKeyNotFound = errors.New("key not found in redis")

// RedisSource implements LookupSource for Redis. Construction performs a
// short, best-effort Ping so configuration errors surface at boot; a failed
// Ping is logged as a warning but does not block source creation, so the
// cache can serve through transient outages and uncached keys error per lookup.
type RedisSource struct {
	client    *redis.Client
	keyPrefix string
	logger    *zap.Logger
}

// NewRedisSource creates a new RedisSource. A short best-effort Ping runs
// against the configured server to surface address/auth errors in logs at
// boot; failure is warned, never fatal.
func NewRedisSource(cfg *RedisConfig, logger *zap.Logger) (*RedisSource, error) {
	opts := &redis.Options{
		Addr:     cfg.Address,
		Username: cfg.Username,
		Password: cfg.Password,
		DB:       cfg.DB,
	}

	if cfg.TLS {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(context.Background(), redisBootPingTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		logger.Warn("redis ping failed at start; source will retry per lookup, cached entries will still be served",
			zap.String("address", cfg.Address),
			zap.Error(err),
		)
	} else {
		logger.Info("redis source ready", zap.String("address", cfg.Address))
	}

	return &RedisSource{
		client:    client,
		keyPrefix: cfg.KeyPrefix,
		logger:    logger,
	}, nil
}

// Lookup retrieves data from Redis for the given key. Tries HGETALL first,
// then falls back to GET with JSON decode if the key holds a string value.
// Honors the caller's context and applies a per-call timeout.
func (r *RedisSource) Lookup(ctx context.Context, key string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, redisLookupTimeout)
	defer cancel()

	redisKey := r.buildKey(key)

	hashResult, err := r.client.HGetAll(ctx, redisKey).Result()
	if err != nil && !isWrongTypeErr(err) {
		return nil, fmt.Errorf("failed to execute HGETALL: %w", err)
	}

	if err == nil && len(hashResult) > 0 {
		r.logger.Debug("redis hash lookup successful", zap.String("key", redisKey), zap.Int("fields", len(hashResult)))
		return hashResult, nil
	}

	jsonResult, err := r.client.Get(ctx, redisKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, errRedisKeyNotFound
		}
		return nil, fmt.Errorf("failed to execute GET: %w", err)
	}

	var data map[string]string
	if err := json.Unmarshal([]byte(jsonResult), &data); err != nil {
		return nil, fmt.Errorf("failed to parse JSON from redis value: %w", err)
	}

	r.logger.Debug("redis string lookup successful", zap.String("key", redisKey))
	return data, nil
}

// Load is a no-op for Redis (connection is managed by the client).
func (r *RedisSource) Load() error {
	return nil
}

// Close closes the Redis connection.
func (r *RedisSource) Close() error {
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

// isWrongTypeErr reports whether err is a Redis WRONGTYPE error, which happens
// when HGETALL is issued against a key holding a string value.
func isWrongTypeErr(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "WRONGTYPE")
}

func (r *RedisSource) buildKey(key string) string {
	if r.keyPrefix != "" {
		return fmt.Sprintf("%s:%s", r.keyPrefix, key)
	}
	return key
}
