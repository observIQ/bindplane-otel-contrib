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
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// unreachableAddr is a reserved port that refuses connections immediately.
const unreachableAddr = "127.0.0.1:1"

// freeAddr reserves a loopback port and releases it, so a test can start a
// server on a known address after first exercising the "nothing listening"
// path against it.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// oneRecord builds a log batch carrying a single record whose attribute
// "ip" is set to key, ready for an attributes-context lookup.
func oneRecord(key string) plog.Logs {
	ld := plog.NewLogs()
	ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty().Attributes().PutStr("ip", key)
	return ld
}

func firstRecordAttrs(ld plog.Logs) map[string]any {
	return ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().AsRaw()
}

func TestStart_RedisUnreachable_StartsAndPassesThrough(t *testing.T) {
	p, logs := startedProcessor(t, &Config{
		Context: attributesContext, Field: "ip",
		Redis:        &RedisConfig{Address: unreachableAddr},
		CacheEnabled: true, CacheTTL: defaultCacheTTL, CacheMaxEntries: defaultCacheMaxEntries,
	})

	warns := logs.FilterLevelExact(zapcore.WarnLevel).All()
	require.NotEmpty(t, warns, "an unreachable Redis at startup must be logged at Warn")
	require.Contains(t, warns[0].Message, "redis", "warning must name the source")

	out, err := p.processLogs(context.Background(), oneRecord("10.0.0.1"))
	require.NoError(t, err, "records must flow while Redis is down")
	require.Equal(t, map[string]any{"ip": "10.0.0.1"}, firstRecordAttrs(out), "record must pass through un-enriched")
}

func TestStart_RedisUnreachable_ReconnectsWithoutRestart(t *testing.T) {
	addr := freeAddr(t)

	p, _ := startedProcessor(t, &Config{
		Context: attributesContext, Field: "ip",
		Redis: &RedisConfig{Address: addr, KeyPrefix: "ip"},
		// Cache off so the assertion below observes the source, not a cached miss.
		CacheEnabled: false,
	})

	out, err := p.processLogs(context.Background(), oneRecord("10.0.0.1"))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"ip": "10.0.0.1"}, firstRecordAttrs(out))

	srv := miniredis.NewMiniRedis()
	require.NoError(t, srv.StartAddr(addr))
	t.Cleanup(srv.Close)
	srv.HSet("ip:10.0.0.1", "host", "web-1")

	require.Eventually(t, func() bool {
		out, err := p.processLogs(context.Background(), oneRecord("10.0.0.1"))
		return err == nil && firstRecordAttrs(out)["host"] == "web-1"
	}, 10*time.Second, 50*time.Millisecond, "records must be enriched once Redis is reachable, without a restart")
}

func TestStart_APIUnreachable_Starts(t *testing.T) {
	p, _ := startedProcessor(t, &Config{
		Context: attributesContext, Field: "ip",
		API:          &APIConfig{URL: "http://" + unreachableAddr + "/lookup/${key}", MaxRetries: 1},
		CacheEnabled: true, CacheTTL: defaultCacheTTL, CacheMaxEntries: defaultCacheMaxEntries,
	})

	out, err := p.processLogs(context.Background(), oneRecord("10.0.0.1"))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"ip": "10.0.0.1"}, firstRecordAttrs(out))
}

func TestStart_CSVMissing_StartsAndLogsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.csv")

	p, logs := startedProcessor(t, &Config{
		Context: attributesContext, Field: "ip", CSV: missing,
	})

	// The reload goroutine reports the missing file; a missing CSV is a deploy
	// error and keeps erroring rather than being downgraded.
	require.Eventually(t, func() bool {
		return len(logs.FilterLevelExact(zapcore.ErrorLevel).All()) > 0
	}, 5*time.Second, 20*time.Millisecond, "missing CSV must be reported at Error")

	out, err := p.processLogs(context.Background(), oneRecord("10.0.0.1"))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"ip": "10.0.0.1"}, firstRecordAttrs(out))
}

func TestRedisSource_OutageLogsOneWarnAndOneRecovery(t *testing.T) {
	addr := freeAddr(t)
	srv := miniredis.NewMiniRedis()
	require.NoError(t, srv.StartAddr(addr))
	srv.HSet("k", "f", "v")

	core, logs := observer.New(zapcore.DebugLevel)
	src, err := NewRedisSource(&RedisConfig{Address: addr}, zap.New(core))
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	_, err = src.Lookup(context.Background(), "k")
	require.NoError(t, err)
	require.Empty(t, logs.FilterLevelExact(zapcore.WarnLevel).All())

	srv.Close()
	for i := 0; i < 3; i++ {
		_, err = src.Lookup(context.Background(), "k")
		require.Error(t, err)
	}
	require.Len(t, logs.FilterLevelExact(zapcore.WarnLevel).All(), 1, "one Warn per outage, not per record")

	srv = miniredis.NewMiniRedis()
	require.NoError(t, srv.StartAddr(addr))
	t.Cleanup(srv.Close)
	srv.HSet("k", "f", "v")
	require.Eventually(t, func() bool {
		_, err := src.Lookup(context.Background(), "k")
		return err == nil
	}, 10*time.Second, 50*time.Millisecond)

	recovered := logs.FilterMessageSnippet("reachable again").All()
	require.Len(t, recovered, 1)
	require.Equal(t, zapcore.InfoLevel, recovered[0].Level)
	require.Len(t, logs.FilterLevelExact(zapcore.WarnLevel).All(), 1, "recovery must not add warnings")
}

func TestAPISource_OutageLogsOneWarn_NotFoundDoesNot(t *testing.T) {
	addr := freeAddr(t)
	l, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	var status atomic.Int32
	status.Store(http.StatusOK)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"host":"web-1"}`))
	})
	ts := httptest.NewUnstartedServer(handler)
	ts.Listener = l
	ts.Start()

	core, logs := observer.New(zapcore.DebugLevel)
	src, err := NewAPISource(&APIConfig{URL: "http://" + addr + "/${key}", MaxRetries: 1}, zap.New(core))
	require.NoError(t, err)

	_, err = src.Lookup(context.Background(), "k")
	require.NoError(t, err)

	status.Store(http.StatusNotFound)
	_, err = src.Lookup(context.Background(), "k")
	require.Error(t, err)
	require.Empty(t, logs.FilterLevelExact(zapcore.WarnLevel).All(), "a 404 is a per-key miss, not an outage")

	ts.Close()
	for i := 0; i < 3; i++ {
		_, err = src.Lookup(context.Background(), "k")
		require.Error(t, err)
	}
	require.Len(t, logs.FilterLevelExact(zapcore.WarnLevel).All(), 1, "one Warn per outage, not per record")

	l, err = net.Listen("tcp", addr)
	require.NoError(t, err)
	status.Store(http.StatusOK)
	ts = httptest.NewUnstartedServer(handler)
	ts.Listener = l
	ts.Start()
	t.Cleanup(ts.Close)

	require.Eventually(t, func() bool {
		_, err := src.Lookup(context.Background(), "k")
		return err == nil
	}, 10*time.Second, 50*time.Millisecond, "one probe per retry window must find the endpoint again")
	require.Len(t, logs.FilterMessageSnippet("reachable again").All(), 1)
}
