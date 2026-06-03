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

package blitzpdata // import "github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/internal/blitzpdata"

import (
	"context"
	"encoding/hex"

	"github.com/observiq/blitz/embed"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// TraceAdapter implements embed.TraceConsumer. Each ConsumeTraces call
// builds a fresh ptrace.Traces from the batch and pushes it to the
// receiver's downstream consumer.
//
// Shape mirrors LogAdapter / MetricAdapter: per-receiver-entry
// LockableAttrs maps for resource and per-span attributes; blitz's per-span
// `Metadata.Resource` and `Metadata.Attributes` merge over the configured
// bases with locked keys preserved; spans whose merged resource maps
// fingerprint differently are emitted under separate `ResourceSpans`
// (Q1 resource grouping).
type TraceAdapter struct {
	consumer consumer.Traces
	resource LockableAttrs
	attrs    LockableAttrs
	logger   *zap.Logger
}

// NewTraceAdapter constructs an adapter that emits to the given
// consumer. resource and attrs follow the same per-key locking semantics as
// NewLogAdapter. A nil logger yields a no-op logger.
func NewTraceAdapter(c consumer.Traces, resource, attrs LockableAttrs, logger *zap.Logger) *TraceAdapter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TraceAdapter{
		consumer: c,
		resource: resource,
		attrs:    attrs,
		logger:   logger,
	}
}

// ConsumeTraces satisfies embed.TraceConsumer. Spans are grouped by
// merged-resource fingerprint (one ResourceSpans per unique merged
// resource, first-occurrence ordered). Empty batches are no-ops.
func (a *TraceAdapter) ConsumeTraces(ctx context.Context, spans []embed.Span) error {
	if len(spans) == 0 {
		return nil
	}

	type group struct {
		merged map[string]any
		spans  []int
	}
	groups := make(map[string]*group)
	order := make([]string, 0)
	for i := range spans {
		merged := a.resource.MergeWithStringOverlay(spans[i].Metadata.Resource)
		fp := FingerprintMap(merged)
		g, exists := groups[fp]
		if !exists {
			g = &group{merged: merged}
			groups[fp] = g
			order = append(order, fp)
		}
		g.spans = append(g.spans, i)
	}

	traces := ptrace.NewTraces()
	for _, fp := range order {
		g := groups[fp]
		rs := traces.ResourceSpans().AppendEmpty()
		if err := rs.Resource().Attributes().FromRaw(g.merged); err != nil {
			a.logger.Warn("blitzpdata: failed to set resource attributes", zap.Error(err))
		}
		ss := rs.ScopeSpans().AppendEmpty()
		ss.Scope().SetName(scopeName)
		for _, idx := range g.spans {
			a.appendSpan(ss.Spans().AppendEmpty(), &spans[idx])
		}
	}
	return a.consumer.ConsumeTraces(ctx, traces)
}

// appendSpan populates a fresh ptrace.Span from a single embed.Span.
//
// TraceID / SpanID / ParentSpanID are hex strings in the embed
// contract; malformed IDs decode to the zero ID with a warning (the
// span still flows — dropping it silently would make a misbehaving
// generator invisible, and downstream pipelines surface zero-ID spans
// loudly).
func (a *TraceAdapter) appendSpan(s ptrace.Span, sp *embed.Span) {
	s.SetTraceID(a.parseTraceID(sp.TraceID))
	s.SetSpanID(a.parseSpanID(sp.SpanID))
	if sp.ParentSpanID != "" {
		s.SetParentSpanID(a.parseSpanID(sp.ParentSpanID))
	}
	s.SetName(sp.Name)
	s.SetKind(spanKindFor(sp.Kind))
	s.SetStartTimestamp(pcommon.NewTimestampFromTime(sp.StartTime))
	s.SetEndTimestamp(pcommon.NewTimestampFromTime(sp.EndTime))

	switch sp.StatusCode {
	case 1:
		s.Status().SetCode(ptrace.StatusCodeOk)
	case 2:
		s.Status().SetCode(ptrace.StatusCodeError)
	default:
		s.Status().SetCode(ptrace.StatusCodeUnset)
	}
	if sp.StatusMessage != "" {
		s.Status().SetMessage(sp.StatusMessage)
	}

	mergedAttrs := a.attrs.MergeWithAnyOverlay(sp.Metadata.Attributes)
	if err := s.Attributes().FromRaw(mergedAttrs); err != nil {
		a.logger.Warn("blitzpdata: failed to set span attributes", zap.Error(err))
	}
}

// parseTraceID decodes a 32-hex-char trace ID string. Malformed input
// yields the zero TraceID with a warning.
func (a *TraceAdapter) parseTraceID(s string) pcommon.TraceID {
	var id pcommon.TraceID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		a.logger.Warn("blitzpdata: malformed trace ID; using zero ID", zap.String("trace_id", s))
		return pcommon.NewTraceIDEmpty()
	}
	copy(id[:], b)
	return id
}

// parseSpanID decodes a 16-hex-char span ID string. Malformed input
// yields the zero SpanID with a warning.
func (a *TraceAdapter) parseSpanID(s string) pcommon.SpanID {
	var id pcommon.SpanID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		a.logger.Warn("blitzpdata: malformed span ID; using zero ID", zap.String("span_id", s))
		return pcommon.NewSpanIDEmpty()
	}
	copy(id[:], b)
	return id
}

// spanKindFor maps the embed contract's SpanKind strings to ptrace
// span kinds. Unknown values map to Internal — the safest default for
// generated telemetry (it implies no client/server relationship that
// downstream service-graph processors would act on).
func spanKindFor(k embed.SpanKind) ptrace.SpanKind {
	switch k {
	case embed.SpanKindServer:
		return ptrace.SpanKindServer
	case embed.SpanKindClient:
		return ptrace.SpanKindClient
	case embed.SpanKindProducer:
		return ptrace.SpanKindProducer
	case embed.SpanKindConsumer:
		return ptrace.SpanKindConsumer
	case embed.SpanKindInternal:
		return ptrace.SpanKindInternal
	default:
		return ptrace.SpanKindInternal
	}
}
