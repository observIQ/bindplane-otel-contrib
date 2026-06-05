// Copyright observIQ, Inc.
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

package blitzpdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/observiq/blitz/embed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap/zaptest"
)

func TestLogAdapter_EmptyBatch_NoPush(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, false, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeLogs(context.Background(), nil))
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{}))
	assert.Equal(t, 0, sink.LogRecordCount(), "empty batches must not produce a downstream consume call")
}

func TestLogAdapter_RawMessage(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, false, zaptest.NewLogger(t))
	ts := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	err := a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message: "hello world",
		Metadata: embed.LogRecordMetadata{
			Timestamp: ts,
			Severity:  "INFO",
		},
	}})
	require.NoError(t, err)
	require.Equal(t, 1, sink.LogRecordCount())
	lr := firstLogRecord(t, sink.AllLogs())
	assert.Equal(t, "hello world", lr.Body().Str())
	assert.Equal(t, pcommon.NewTimestampFromTime(ts), lr.Timestamp())
	assert.Equal(t, "INFO", lr.SeverityText())
	assert.Equal(t, plog.SeverityNumberInfo, lr.SeverityNumber())
	assert.NotZero(t, lr.ObservedTimestamp(), "ObservedTimestamp should be set")
}

func TestLogAdapter_RawByDefault_IgnoresParseFunc(t *testing.T) {
	// parseBody=false (the receiver default) means ParseFunc is NOT
	// invoked even when blitz populates it on the LogRecord. The body
	// is the raw Message string. This protects downstream pipelines
	// from receiving structured maps when the operator intended raw
	// log lines (the typical telemetrygenerator-receiver use case).
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, false, zaptest.NewLogger(t))
	parseCalled := false
	parseFunc := func(_ string) (map[string]any, error) {
		parseCalled = true
		return map[string]any{"never": "applied"}, nil
	}
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message:   "192.0.2.1 - - [27/May/2026:12:00:00 +0000] \"GET / HTTP/1.1\" 200 0",
		ParseFunc: parseFunc,
	}}))
	lr := firstLogRecord(t, sink.AllLogs())
	assert.Equal(t, pcommon.ValueTypeStr, lr.Body().Type(), "raw mode must emit a string body")
	assert.Equal(t, "192.0.2.1 - - [27/May/2026:12:00:00 +0000] \"GET / HTTP/1.1\" 200 0", lr.Body().Str())
	assert.False(t, parseCalled, "ParseFunc must not be called when parseBody is false")
}

func TestLogAdapter_ParseFunc_Success(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, true, zaptest.NewLogger(t))
	parsed := map[string]any{"method": "GET", "status": int64(200), "path": "/healthz"}
	parseFunc := func(msg string) (map[string]any, error) {
		assert.Equal(t, "GET /healthz 200", msg)
		return parsed, nil
	}
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message:   "GET /healthz 200",
		ParseFunc: parseFunc,
		Metadata:  embed.LogRecordMetadata{Severity: "INFO"},
	}}))
	require.Equal(t, 1, sink.LogRecordCount())
	lr := firstLogRecord(t, sink.AllLogs())
	require.Equal(t, pcommon.ValueTypeMap, lr.Body().Type())
	body := lr.Body().Map().AsRaw()
	assert.Equal(t, "GET", body["method"])
	assert.Equal(t, int64(200), body["status"])
	assert.Equal(t, "/healthz", body["path"])
}

func TestLogAdapter_ParseFunc_Error_FallsBackToRaw(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, true, zaptest.NewLogger(t))
	parseFunc := func(_ string) (map[string]any, error) {
		return nil, errors.New("malformed input")
	}
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message:   "bad payload",
		ParseFunc: parseFunc,
	}}))
	lr := firstLogRecord(t, sink.AllLogs())
	assert.Equal(t, pcommon.ValueTypeStr, lr.Body().Type())
	assert.Equal(t, "bad payload", lr.Body().Str())
}

func TestLogAdapter_ParseFunc_EmptyMap_FallsBackToRaw(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, true, zaptest.NewLogger(t))
	parseFunc := func(_ string) (map[string]any, error) {
		return map[string]any{}, nil
	}
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message:   "raw",
		ParseFunc: parseFunc,
	}}))
	lr := firstLogRecord(t, sink.AllLogs())
	assert.Equal(t, "raw", lr.Body().Str())
}

func TestLogAdapter_ParseFunc_Panic_FallsBackToRaw(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, true, zaptest.NewLogger(t))
	parseFunc := func(_ string) (map[string]any, error) {
		panic("recipe ParseFunc went sideways")
	}
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message:   "untrusted payload",
		ParseFunc: parseFunc,
	}}))
	lr := firstLogRecord(t, sink.AllLogs())
	assert.Equal(t, pcommon.ValueTypeStr, lr.Body().Type(),
		"a panicking ParseFunc must fall back to the raw Message body")
	assert.Equal(t, "untrusted payload", lr.Body().Str())
	assert.Equal(t, 1, sink.LogRecordCount(),
		"the record must still flow through despite the panic")
}

func TestLogAdapter_ResourceAndRecordAttributes(t *testing.T) {
	sink := &consumertest.LogsSink{}
	resource := LockableAttrs{Base: map[string]any{
		"service.name": "blitz-test",
		"deployment":   "ci",
	}}
	attrs := LockableAttrs{Base: map[string]any{
		"log.source": "apache",
	}}
	a := NewLogAdapter(sink, resource, attrs, false, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message: "first",
	}, {
		Message: "second",
	}}))
	logs := sink.AllLogs()
	require.Len(t, logs, 1, "batch should produce a single plog.Logs")
	rl := logs[0].ResourceLogs().At(0)
	resMap := rl.Resource().Attributes().AsRaw()
	assert.Equal(t, "blitz-test", resMap["service.name"])
	assert.Equal(t, "ci", resMap["deployment"])
	require.Equal(t, 1, rl.ScopeLogs().Len())
	sl := rl.ScopeLogs().At(0)
	assert.Equal(t, scopeName, sl.Scope().Name())
	require.Equal(t, 2, sl.LogRecords().Len())
	for i := 0; i < sl.LogRecords().Len(); i++ {
		lr := sl.LogRecords().At(i)
		got := lr.Attributes().AsRaw()
		assert.Equal(t, "apache", got["log.source"], "record %d should carry per-record attrs", i)
	}
}

func TestLogAdapter_ZeroTimestamp_FallsBackToNow(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, false, zaptest.NewLogger(t))
	before := time.Now()
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message: "no ts",
	}}))
	after := time.Now()
	lr := firstLogRecord(t, sink.AllLogs())
	ts := lr.Timestamp().AsTime()
	assert.False(t, ts.Before(before), "timestamp should not predate the ConsumeLogs call")
	assert.False(t, ts.After(after), "timestamp should not postdate the ConsumeLogs call")
}

func TestLogAdapter_NoSeverity_LeavesSeverityNumberUnspecified(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, false, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message: "no severity",
	}}))
	lr := firstLogRecord(t, sink.AllLogs())
	assert.Empty(t, lr.SeverityText())
	assert.Equal(t, plog.SeverityNumberUnspecified, lr.SeverityNumber())
}

func TestLogAdapter_NilLogger_NoPanic(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, false, nil)
	require.NotPanics(t, func() {
		_ = a.ConsumeLogs(context.Background(), []embed.LogRecord{{Message: "x"}})
	})
}

func TestSeverityNumberFor(t *testing.T) {
	tests := []struct {
		text string
		want plog.SeverityNumber
	}{
		// Case insensitivity + whitespace tolerance.
		{"INFO", plog.SeverityNumberInfo},
		{"info", plog.SeverityNumberInfo},
		{"  info  ", plog.SeverityNumberInfo},
		{"Information", plog.SeverityNumberInfo},
		{"informational", plog.SeverityNumberInfo},

		// OTel native short names.
		{"TRACE", plog.SeverityNumberTrace},
		{"DEBUG", plog.SeverityNumberDebug},
		{"WARN", plog.SeverityNumberWarn},
		{"ERROR", plog.SeverityNumberError},
		{"FATAL", plog.SeverityNumberFatal},

		// Common synonyms.
		{"dbg", plog.SeverityNumberDebug},
		{"warning", plog.SeverityNumberWarn},
		{"err", plog.SeverityNumberError},

		// Syslog-style high-severity levels.
		{"NOTICE", plog.SeverityNumberInfo2},
		{"CRITICAL", plog.SeverityNumberFatal2},
		{"crit", plog.SeverityNumberFatal2},
		{"ALERT", plog.SeverityNumberFatal3},
		{"EMERGENCY", plog.SeverityNumberFatal4},
		{"emerg", plog.SeverityNumberFatal4},
		{"panic", plog.SeverityNumberFatal4},

		// Unknowns yield Unspecified.
		{"", plog.SeverityNumberUnspecified},
		{"hilarious", plog.SeverityNumberUnspecified},
	}
	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			assert.Equal(t, tc.want, severityNumberFor(tc.text))
		})
	}
}

// PIPE-1021 per-record metadata tests — blitz-supplied Metadata.Resource
// and Metadata.Attributes merging over the receiver-config lockable base.

func TestLogAdapter_PerRecordResource_MergesOverBase(t *testing.T) {
	sink := &consumertest.LogsSink{}
	resource := LockableAttrs{Base: map[string]any{
		"host.name":    "from-config",
		"cluster.name": "gargantua",
	}}
	a := NewLogAdapter(sink, resource, LockableAttrs{}, false, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message: "x",
		Metadata: embed.LogRecordMetadata{
			Resource: map[string]string{
				"host.name":        "from-blitz", // overrides unlocked base
				"telemetry.source": "nginx",      // new key, lands as-is
			},
		},
	}}))
	logs := sink.AllLogs()
	require.Len(t, logs, 1)
	resMap := logs[0].ResourceLogs().At(0).Resource().Attributes().AsRaw()
	assert.Equal(t, "from-blitz", resMap["host.name"], "unlocked: blitz wins")
	assert.Equal(t, "gargantua", resMap["cluster.name"], "no blitz value: base stays")
	assert.Equal(t, "nginx", resMap["telemetry.source"], "blitz-only key lands")
}

func TestLogAdapter_PerRecordResource_LockedKeyStays(t *testing.T) {
	sink := &consumertest.LogsSink{}
	resource := LockableAttrs{
		Base:   map[string]any{"host.name": "pinned-host"},
		Locked: map[string]struct{}{"host.name": {}},
	}
	a := NewLogAdapter(sink, resource, LockableAttrs{}, false, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message: "x",
		Metadata: embed.LogRecordMetadata{
			Resource: map[string]string{"host.name": "blitz-host"},
		},
	}}))
	resMap := sink.AllLogs()[0].ResourceLogs().At(0).Resource().Attributes().AsRaw()
	assert.Equal(t, "pinned-host", resMap["host.name"], "locked: receiver-config wins")
}

func TestLogAdapter_PerRecordAttributes_MergeAndLock(t *testing.T) {
	sink := &consumertest.LogsSink{}
	attrs := LockableAttrs{
		Base: map[string]any{
			"service.team": "midnight-pizza-ops",
			"test.run.id":  "base-run",
		},
		Locked: map[string]struct{}{"service.team": {}},
	}
	a := NewLogAdapter(sink, LockableAttrs{}, attrs, false, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
		Message: "x",
		Metadata: embed.LogRecordMetadata{
			Attributes: map[string]any{
				"service.team":    "blitz-team", // locked — dropped
				"test.run.id":     "blitz-run",  // unlocked — wins
				"blitz.generator": "apache",     // blitz-only — lands
			},
		},
	}}))
	lr := firstLogRecord(t, sink.AllLogs())
	got := lr.Attributes().AsRaw()
	assert.Equal(t, "midnight-pizza-ops", got["service.team"])
	assert.Equal(t, "blitz-run", got["test.run.id"])
	assert.Equal(t, "apache", got["blitz.generator"])
}

func TestLogAdapter_NilAndEmptyMetadata_Identical(t *testing.T) {
	// nil and empty Metadata maps both mean "no override".
	for _, meta := range []embed.LogRecordMetadata{
		{}, // nil maps
		{Resource: map[string]string{}, Attributes: map[string]any{}}, // empty maps
	} {
		sink := &consumertest.LogsSink{}
		resource := LockableAttrs{Base: map[string]any{"host.name": "h"}}
		attrs := LockableAttrs{Base: map[string]any{"a": "v"}}
		a := NewLogAdapter(sink, resource, attrs, false, zaptest.NewLogger(t))
		require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{{
			Message:  "x",
			Metadata: meta,
		}}))
		resMap := sink.AllLogs()[0].ResourceLogs().At(0).Resource().Attributes().AsRaw()
		assert.Equal(t, "h", resMap["host.name"])
		lr := firstLogRecord(t, sink.AllLogs())
		assert.Equal(t, "v", lr.Attributes().AsRaw()["a"])
	}
}

// Resource grouping tests — records with distinct effective resources
// split into separate ResourceLogs; identical resources share one.

func TestLogAdapter_ResourceGrouping_DistinctResources_Split(t *testing.T) {
	sink := &consumertest.LogsSink{}
	a := NewLogAdapter(sink, LockableAttrs{}, LockableAttrs{}, false, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{
		{Message: "a", Metadata: embed.LogRecordMetadata{Resource: map[string]string{"host.name": "h1"}}},
		{Message: "b", Metadata: embed.LogRecordMetadata{Resource: map[string]string{"host.name": "h2"}}},
		{Message: "c", Metadata: embed.LogRecordMetadata{Resource: map[string]string{"host.name": "h1"}}},
	}))
	logs := sink.AllLogs()
	require.Len(t, logs, 1)
	require.Equal(t, 2, logs[0].ResourceLogs().Len(), "two distinct resources → two ResourceLogs")
	// First-occurrence ordering: h1 group first (records a + c), then h2.
	rl0 := logs[0].ResourceLogs().At(0)
	assert.Equal(t, "h1", rl0.Resource().Attributes().AsRaw()["host.name"])
	require.Equal(t, 1, rl0.ScopeLogs().Len())
	assert.Equal(t, 2, rl0.ScopeLogs().At(0).LogRecords().Len(), "h1 group carries records a and c")
	rl1 := logs[0].ResourceLogs().At(1)
	assert.Equal(t, "h2", rl1.Resource().Attributes().AsRaw()["host.name"])
	assert.Equal(t, 1, rl1.ScopeLogs().At(0).LogRecords().Len())
}

func TestLogAdapter_ResourceGrouping_LockedKeyCollapsesGroups(t *testing.T) {
	// Resource-grouping × locking interaction: when the differing key is locked, all
	// records share the same effective resource and group together.
	sink := &consumertest.LogsSink{}
	resource := LockableAttrs{
		Base:   map[string]any{"host.name": "pinned"},
		Locked: map[string]struct{}{"host.name": {}},
	}
	a := NewLogAdapter(sink, resource, LockableAttrs{}, false, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeLogs(context.Background(), []embed.LogRecord{
		{Message: "a", Metadata: embed.LogRecordMetadata{Resource: map[string]string{"host.name": "h1"}}},
		{Message: "b", Metadata: embed.LogRecordMetadata{Resource: map[string]string{"host.name": "h2"}}},
	}))
	logs := sink.AllLogs()
	require.Len(t, logs, 1)
	require.Equal(t, 1, logs[0].ResourceLogs().Len(),
		"locked host.name collapses both records into one ResourceLogs")
	rl := logs[0].ResourceLogs().At(0)
	assert.Equal(t, "pinned", rl.Resource().Attributes().AsRaw()["host.name"])
	assert.Equal(t, 2, rl.ScopeLogs().At(0).LogRecords().Len())
}

// firstLogRecord pulls the single log record out of a sink that expects
// exactly one batch with exactly one record. Fails the test if the
// shape doesn't match.
func firstLogRecord(t *testing.T, logs []plog.Logs) plog.LogRecord {
	t.Helper()
	require.Len(t, logs, 1, "expected exactly one plog.Logs batch")
	require.Equal(t, 1, logs[0].ResourceLogs().Len())
	rl := logs[0].ResourceLogs().At(0)
	require.Equal(t, 1, rl.ScopeLogs().Len())
	sl := rl.ScopeLogs().At(0)
	require.Equal(t, 1, sl.LogRecords().Len())
	return sl.LogRecords().At(0)
}
