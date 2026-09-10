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
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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

func TestRedisSource_UnreachableAtStart_WarnsAndStarts(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)

	// 127.0.0.1:1 is reserved and refuses connections. The constructor must
	// still return a usable source: enrichment is best-effort and a downed
	// Redis must not keep the collector from starting.
	src, err := NewRedisSource(&RedisConfig{Address: "127.0.0.1:1"}, zap.New(core))
	require.NoError(t, err)
	require.NotNil(t, src)
	t.Cleanup(func() { _ = src.Close() })

	warns := logs.FilterLevelExact(zapcore.WarnLevel).All()
	require.Len(t, warns, 1, "exactly one startup warning")
	require.Contains(t, warns[0].ContextMap()["address"], "127.0.0.1:1")

	_, err = src.Lookup(context.Background(), "k")
	require.Error(t, err, "a lookup against a downed Redis fails; the processor passes the record through")
}

func TestRedisSource_ReachableAtStart_NoWarn(t *testing.T) {
	srv := miniredis.RunT(t)
	core, logs := observer.New(zapcore.DebugLevel)

	src, err := NewRedisSource(&RedisConfig{Address: srv.Addr()}, zap.New(core))
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	require.Empty(t, logs.FilterLevelExact(zapcore.WarnLevel).All())
}

func TestRedisSource_BuildKey(t *testing.T) {
	r := &RedisSource{keyPrefix: "p"}
	require.Equal(t, "p:abc", r.buildKey("abc"))

	r2 := &RedisSource{}
	require.Equal(t, "abc", r2.buildKey("abc"))
}

// flakyRedis is a minimal RESP server that answers HGETALL with WRONGTYPE and
// drops the connection on GET, so a lookup fails on the second command rather
// than the first. miniredis cannot fail one command and not another.
type flakyRedis struct {
	ln   net.Listener
	gets atomic.Int32
}

func startFlakyRedis(t *testing.T) *flakyRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &flakyRedis{ln: ln}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *flakyRedis) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *flakyRedis) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		args, err := readRESPArray(r)
		if err != nil {
			return
		}
		switch strings.ToUpper(args[0]) {
		case "PING":
			_, _ = conn.Write([]byte("+PONG\r\n"))
		case "HGETALL":
			_, _ = conn.Write([]byte("-WRONGTYPE Operation against a key holding the wrong kind of value\r\n"))
		case "GET":
			f.gets.Add(1)
			return // drop the connection mid-lookup
		case "HELLO":
			_, _ = conn.Write([]byte("-ERR unknown command 'HELLO'\r\n")) // go-redis falls back to RESP2
		default:
			_, _ = conn.Write([]byte("+OK\r\n"))
		}
	}
}

// readRESPArray parses one client command: *N then N bulk strings.
func readRESPArray(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected array, got %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if _, err := r.ReadString('\n'); err != nil { // $len
			return nil, err
		}
		s, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		args = append(args, strings.TrimSuffix(s, "\r\n"))
	}
	return args, nil
}

func TestRedisSource_GETFailureAfterHGETALLMarksDown(t *testing.T) {
	srv := startFlakyRedis(t)
	core, logs := observer.New(zapcore.DebugLevel)

	src, err := NewRedisSource(&RedisConfig{Address: srv.ln.Addr().String()}, zap.New(core))
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	require.Empty(t, logs.FilterLevelExact(zapcore.WarnLevel).All(), "PING succeeded, source starts up")

	_, err = src.Lookup(context.Background(), "k")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to execute GET", "HGETALL answered WRONGTYPE, so the failure must come from GET")
	require.Positive(t, srv.gets.Load())
	require.Len(t, logs.FilterLevelExact(zapcore.WarnLevel).All(), 1, "a GET failure marks the source down")

	_, err = src.Lookup(context.Background(), "k")
	require.ErrorIs(t, err, errSourceUnavailable, "the next lookup inside the window is skipped")
}
