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

// redisDefaultLookupTimeout bounds each Redis call when the user has not set
// RedisConfig.LookupTimeout. Keeps a slow or unreachable server from stalling
// pipeline processing indefinitely.
const redisDefaultLookupTimeout = 5 * time.Second

// redisDefaultDialTimeout bounds the initial TCP/TLS dial when the user has
// not set RedisConfig.DialTimeout.
const redisDefaultDialTimeout = 2 * time.Second

// redisBootPingTimeout bounds the startup Ping used to surface connectivity
// and credential problems at boot. Failure is logged, not fatal: enrichment is
// best-effort and a downed Redis must not keep the collector from starting.
const redisBootPingTimeout = 2 * time.Second

var errRedisKeyNotFound = errors.New("key not found in redis")

// RedisSource implements LookupSource for Redis. Construction performs a
// short Ping so configuration errors (bad address, auth) are reported at boot,
// then the client connects lazily on the first lookup like the API source.
type RedisSource struct {
	client       *redis.Client
	keyPrefix    string
	lookupBudget time.Duration
	logger       *zap.Logger
	avail        *availability
}

// NewRedisSource creates a new RedisSource. An unreachable Redis is logged at
// Warn and the source starts anyway; records pass through un-enriched until
// the client can connect.
func NewRedisSource(cfg *RedisConfig, logger *zap.Logger) (*RedisSource, error) {
	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = redisDefaultDialTimeout
	}

	lookupBudget := cfg.LookupTimeout
	if lookupBudget <= 0 {
		lookupBudget = redisDefaultLookupTimeout
	}

	opts := &redis.Options{
		Addr:        cfg.Address,
		Username:    cfg.Username,
		Password:    cfg.Password,
		DB:          cfg.DB,
		DialTimeout: dialTimeout,
	}

	if cfg.TLS {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	logger = logger.With(zap.String("address", cfg.Address))
	src := &RedisSource{
		client:       redis.NewClient(opts),
		keyPrefix:    cfg.KeyPrefix,
		lookupBudget: lookupBudget,
		logger:       logger,
		avail:        newAvailability(logger, "redis"),
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), redisBootPingTimeout)
	defer cancel()
	if err := src.client.Ping(pingCtx).Err(); err != nil {
		src.avail.startDown()
		logger.Warn("redis unreachable at startup; collector continues and records pass through un-enriched until redis is reachable", zap.Error(err))
	} else {
		logger.Info("redis source ready")
	}

	return src, nil
}

// Lookup retrieves data from Redis for the given key. Tries HGETALL first,
// then falls back to GET with JSON decode if the key holds a string value.
// Honors the caller's context and applies a per-call timeout. While Redis is
// known to be down only one probe per retry window reaches the network.
func (r *RedisSource) Lookup(ctx context.Context, key string) (map[string]string, error) {
	if r.avail.skip() {
		return nil, errSourceUnavailable
	}

	ctx, cancel := context.WithTimeout(ctx, r.lookupBudget)
	defer cancel()

	redisKey := r.buildKey(key)

	hashResult, err := r.client.HGetAll(ctx, redisKey).Result()
	if err != nil && !isWrongTypeErr(err) {
		r.avail.markDown(err)
		return nil, fmt.Errorf("failed to execute HGETALL: %w", err)
	}
	r.avail.markUp()

	if err == nil && len(hashResult) > 0 {
		r.logger.Debug("redis hash lookup successful", zap.String("key", redisKey), zap.Int("fields", len(hashResult)))
		return hashResult, nil
	}

	jsonResult, err := r.client.Get(ctx, redisKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, errRedisKeyNotFound
		}
		r.avail.markDown(err)
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
