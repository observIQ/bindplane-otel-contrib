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
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRedisSource_HashLookup(t *testing.T) {
	srv := miniredis.RunT(t)
	srv.HSet("user:42", "name", "alice", "team", "sre")

	src, err := NewRedisSource(&RedisConfig{
		Address:   srv.Addr(),
		KeyPrefix: "user",
	}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	got, err := src.Lookup(context.Background(), "42")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"name": "alice", "team": "sre"}, got)
}

func TestRedisSource_JSONFallback(t *testing.T) {
	srv := miniredis.RunT(t)
	srv.Set("ip:0.0.0.0", `{"host":"h1","region":"us-west"}`)

	src, err := NewRedisSource(&RedisConfig{
		Address:   srv.Addr(),
		KeyPrefix: "ip",
	}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	got, err := src.Lookup(context.Background(), "0.0.0.0")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"host": "h1", "region": "us-west"}, got)
}

func TestRedisSource_NotFound(t *testing.T) {
	srv := miniredis.RunT(t)

	src, err := NewRedisSource(&RedisConfig{Address: srv.Addr()}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	_, err = src.Lookup(context.Background(), "missing")
	require.Error(t, err)
	require.True(t, errors.Is(err, errRedisKeyNotFound))
}

func TestRedisSource_StartPingFailIsFatal(t *testing.T) {
	// 127.0.0.1:1 is reserved and refuses connections. Constructor must fail
	// fast so a misconfigured Redis aborts processor start instead of leaving
	// lookups silently broken until the first request.
	src, err := NewRedisSource(&RedisConfig{Address: "127.0.0.1:1"}, zap.NewNop())
	require.Error(t, err)
	require.Nil(t, src)
}

func TestRedisSource_BuildKey(t *testing.T) {
	r := &RedisSource{keyPrefix: "p"}
	require.Equal(t, "p:abc", r.buildKey("abc"))

	r2 := &RedisSource{}
	require.Equal(t, "abc", r2.buildKey("abc"))
}
