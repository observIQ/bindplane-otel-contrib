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

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

var errRedisKeyNotFound = errors.New("key not found in redis")

// RedisSource implements LookupSource for Redis.
type RedisSource struct {
	client    *redis.Client
	keyPrefix string
	logger    *zap.Logger
}

// NewRedisSource creates a new RedisSource. It establishes the connection eagerly
// so configuration errors surface during processor start.
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

	if err := client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	logger.Info("successfully connected to redis", zap.String("address", cfg.Address))

	return &RedisSource{
		client:    client,
		keyPrefix: cfg.KeyPrefix,
		logger:    logger,
	}, nil
}

// Lookup retrieves data from Redis for the given key. Tries HGETALL first, then
// falls back to GET with JSON decode.
func (r *RedisSource) Lookup(key string) (map[string]string, error) {
	ctx := context.Background()
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

// Load is a no-op for Redis (connection is established in constructor).
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
