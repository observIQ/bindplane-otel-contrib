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
	"testing"
	"time"

	"github.com/observiq/blitz/embed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap/zaptest"
)

const (
	testTraceID = "0123456789abcdef0123456789abcdef"
	testSpanID  = "0123456789abcdef"
	testParent  = "fedcba9876543210"
)

func TestTraceAdapter_EmptyBatch_NoPush(t *testing.T) {
	sink := &consumertest.TracesSink{}
	a := NewTraceAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeTraces(context.Background(), nil))
	require.NoError(t, a.ConsumeTraces(context.Background(), []embed.Span{}))
	assert.Equal(t, 0, sink.SpanCount(), "empty batches must not produce a downstream consume call")
}

func TestTraceAdapter_FullSpan(t *testing.T) {
	sink := &consumertest.TracesSink{}
	a := NewTraceAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	start := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	end := start.Add(150 * time.Millisecond)
	require.NoError(t, a.ConsumeTraces(context.Background(), []embed.Span{{
		TraceID:       testTraceID,
		SpanID:        testSpanID,
		ParentSpanID:  testParent,
		Name:          "GET /checkout",
		Kind:          embed.SpanKindServer,
		StartTime:     start,
		EndTime:       end,
		StatusCode:    2,
		StatusMessage: "upstream timeout",
	}}))
	s := firstSpan(t, sink.AllTraces())
	assert.Equal(t, testTraceID, s.TraceID().String())
	assert.Equal(t, testSpanID, s.SpanID().String())
	assert.Equal(t, testParent, s.ParentSpanID().String())
	assert.Equal(t, "GET /checkout", s.Name())
	assert.Equal(t, ptrace.SpanKindServer, s.Kind())
	assert.Equal(t, pcommon.NewTimestampFromTime(start), s.StartTimestamp())
	assert.Equal(t, pcommon.NewTimestampFromTime(end), s.EndTimestamp())
	assert.Equal(t, ptrace.StatusCodeError, s.Status().Code())
	assert.Equal(t, "upstream timeout", s.Status().Message())
}

func TestTraceAdapter_RootSpan_NoParent(t *testing.T) {
	sink := &consumertest.TracesSink{}
	a := NewTraceAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeTraces(context.Background(), []embed.Span{{
		TraceID: testTraceID,
		SpanID:  testSpanID,
		Name:    "root",
		Kind:    embed.SpanKindInternal,
	}}))
	s := firstSpan(t, sink.AllTraces())
	assert.True(t, s.ParentSpanID().IsEmpty(), "empty ParentSpanID stays zero")
}

func TestTraceAdapter_StatusCodes(t *testing.T) {
	cases := []struct {
		code int
		want ptrace.StatusCode
	}{
		{0, ptrace.StatusCodeUnset},
		{1, ptrace.StatusCodeOk},
		{2, ptrace.StatusCodeError},
		{99, ptrace.StatusCodeUnset}, // out-of-contract value → Unset
	}
	for _, tc := range cases {
		sink := &consumertest.TracesSink{}
		a := NewTraceAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
		require.NoError(t, a.ConsumeTraces(context.Background(), []embed.Span{{
			TraceID:    testTraceID,
			SpanID:     testSpanID,
			StatusCode: tc.code,
		}}))
		s := firstSpan(t, sink.AllTraces())
		assert.Equal(t, tc.want, s.Status().Code(), "status code %d", tc.code)
	}
}

func TestTraceAdapter_SpanKinds(t *testing.T) {
	cases := []struct {
		kind embed.SpanKind
		want ptrace.SpanKind
	}{
		{embed.SpanKindInternal, ptrace.SpanKindInternal},
		{embed.SpanKindServer, ptrace.SpanKindServer},
		{embed.SpanKindClient, ptrace.SpanKindClient},
		{embed.SpanKindProducer, ptrace.SpanKindProducer},
		{embed.SpanKindConsumer, ptrace.SpanKindConsumer},
		{embed.SpanKind("alien"), ptrace.SpanKindInternal}, // unknown → Internal
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, spanKindFor(tc.kind), "kind %q", tc.kind)
	}
}

func TestTraceAdapter_MalformedIDs_ZeroWithWarning(t *testing.T) {
	sink := &consumertest.TracesSink{}
	a := NewTraceAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeTraces(context.Background(), []embed.Span{{
		TraceID: "not-hex-at-all",
		SpanID:  "tooshort",
		Name:    "still flows",
	}}))
	s := firstSpan(t, sink.AllTraces())
	assert.True(t, s.TraceID().IsEmpty(), "malformed trace ID → zero")
	assert.True(t, s.SpanID().IsEmpty(), "malformed span ID → zero")
	assert.Equal(t, "still flows", s.Name(), "span still flows through")
}

func TestTraceAdapter_PerSpanAttributes_MergeAndLock(t *testing.T) {
	sink := &consumertest.TracesSink{}
	attrs := LockableAttrs{
		Base:   map[string]any{"service.team": "ops", "run.id": "base"},
		Locked: map[string]struct{}{"service.team": {}},
	}
	a := NewTraceAdapter(sink, LockableAttrs{}, attrs, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeTraces(context.Background(), []embed.Span{{
		TraceID: testTraceID,
		SpanID:  testSpanID,
		Metadata: embed.SpanMetadata{
			Attributes: map[string]any{
				"service.team": "blitz-team", // locked — dropped
				"run.id":       "blitz-run",  // unlocked — wins
				"http.method":  "GET",        // blitz-only — lands
			},
		},
	}}))
	s := firstSpan(t, sink.AllTraces())
	got := s.Attributes().AsRaw()
	assert.Equal(t, "ops", got["service.team"])
	assert.Equal(t, "blitz-run", got["run.id"])
	assert.Equal(t, "GET", got["http.method"])
}

func TestTraceAdapter_PerSpanResource_MergeLockAndGroup(t *testing.T) {
	sink := &consumertest.TracesSink{}
	resource := LockableAttrs{Base: map[string]any{"cluster.name": "gargantua"}}
	a := NewTraceAdapter(sink, resource, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeTraces(context.Background(), []embed.Span{
		{TraceID: testTraceID, SpanID: testSpanID, Name: "a",
			Metadata: embed.SpanMetadata{Resource: map[string]string{"host.name": "h1"}}},
		{TraceID: testTraceID, SpanID: testParent, Name: "b",
			Metadata: embed.SpanMetadata{Resource: map[string]string{"host.name": "h2"}}},
		{TraceID: testTraceID, SpanID: testSpanID, Name: "c",
			Metadata: embed.SpanMetadata{Resource: map[string]string{"host.name": "h1"}}},
	}))
	traces := sink.AllTraces()
	require.Len(t, traces, 1)
	require.Equal(t, 2, traces[0].ResourceSpans().Len(), "two distinct resources → two ResourceSpans")
	rs0 := traces[0].ResourceSpans().At(0)
	res0 := rs0.Resource().Attributes().AsRaw()
	assert.Equal(t, "h1", res0["host.name"])
	assert.Equal(t, "gargantua", res0["cluster.name"], "configured base flows into every group")
	assert.Equal(t, 2, rs0.ScopeSpans().At(0).Spans().Len(), "h1 group carries spans a and c")
	rs1 := traces[0].ResourceSpans().At(1)
	assert.Equal(t, "h2", rs1.Resource().Attributes().AsRaw()["host.name"])
	assert.Equal(t, 1, rs1.ScopeSpans().At(0).Spans().Len())
}

func TestTraceAdapter_NilLogger_NoPanic(t *testing.T) {
	sink := &consumertest.TracesSink{}
	a := NewTraceAdapter(sink, LockableAttrs{}, LockableAttrs{}, nil)
	require.NotPanics(t, func() {
		_ = a.ConsumeTraces(context.Background(), []embed.Span{{TraceID: testTraceID, SpanID: testSpanID}})
	})
}

// firstSpan pulls the single span out of a sink that expects exactly
// one batch with one resource, one scope, and one span.
func firstSpan(t *testing.T, traces []ptrace.Traces) ptrace.Span {
	t.Helper()
	require.Len(t, traces, 1, "expected exactly one ptrace.Traces batch")
	require.Equal(t, 1, traces[0].ResourceSpans().Len())
	rs := traces[0].ResourceSpans().At(0)
	require.Equal(t, 1, rs.ScopeSpans().Len())
	ss := rs.ScopeSpans().At(0)
	require.Equal(t, 1, ss.Spans().Len())
	return ss.Spans().At(0)
}
