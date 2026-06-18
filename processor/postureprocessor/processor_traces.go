// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package postureprocessor

import (
	"context"

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottlspan"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
)

type tracesProcessor struct {
	core        *core
	conditions  []*expr.OTTLCondition[*ottlspan.TransformContext] // aligned with cfg.Tiers
	next        consumer.Traces
	marshaler   ptrace.ProtoMarshaler
	unmarshaler ptrace.ProtoUnmarshaler
}

func (p *tracesProcessor) classify(ctx context.Context, rs ptrace.ResourceSpans, ss ptrace.ScopeSpans, span ptrace.Span) int {
	for i, cond := range p.conditions {
		match, err := cond.Match(ctx, ottlspan.NewTransformContextPtr(rs, ss, span))
		if err == nil && match {
			return i
		}
	}
	return len(p.conditions)
}

func (p *tracesProcessor) processTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	level := p.core.currentLevel()
	buffered := make([]*ptrace.Traces, len(p.core.tiers))

	for i := 0; i < td.ResourceSpans().Len(); i++ {
		rs := td.ResourceSpans().At(i)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			ss := rs.ScopeSpans().At(j)
			destScopes := make([]ptrace.ScopeSpans, len(p.core.tiers))
			created := make([]bool, len(p.core.tiers))

			ss.Spans().RemoveIf(func(span ptrace.Span) bool {
				idx := p.classify(ctx, rs, ss, span)
				if level >= p.core.minLevelFor(idx) {
					return false
				}
				if !created[idx] {
					if buffered[idx] == nil {
						b := ptrace.NewTraces()
						buffered[idx] = &b
					}
					destRS := buffered[idx].ResourceSpans().AppendEmpty()
					rs.Resource().CopyTo(destRS.Resource())
					destRS.SetSchemaUrl(rs.SchemaUrl())
					ds := destRS.ScopeSpans().AppendEmpty()
					ss.Scope().CopyTo(ds.Scope())
					ds.SetSchemaUrl(ss.SchemaUrl())
					destScopes[idx] = ds
					created[idx] = true
				}
				span.CopyTo(destScopes[idx].Spans().AppendEmpty())
				return true
			})
		}
	}

	pruneEmptyResourceSpans(td)
	p.enqueueBuffered(ctx, buffered)
	return td, nil
}

func (p *tracesProcessor) enqueueBuffered(ctx context.Context, buffered []*ptrace.Traces) {
	for idx, b := range buffered {
		if b == nil {
			continue
		}
		payload, err := p.marshaler.MarshalTraces(*b)
		if err != nil {
			p.core.logger.Error("failed to marshal buffered traces", zap.String("tier", p.core.tiers[idx].name), zap.Error(err))
			continue
		}
		if err := p.core.queues[idx].enqueue(ctx, payload); err != nil {
			p.core.logger.Error("failed to buffer traces", zap.String("tier", p.core.tiers[idx].name), zap.Error(err))
		}
	}
}

func (p *tracesProcessor) emit(ctx context.Context, payload []byte) error {
	td, err := p.unmarshaler.UnmarshalTraces(payload)
	if err != nil {
		return err
	}
	return p.next.ConsumeTraces(ctx, td)
}

func pruneEmptyResourceSpans(td ptrace.Traces) {
	td.ResourceSpans().RemoveIf(func(rs ptrace.ResourceSpans) bool {
		rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool {
			return ss.Spans().Len() == 0
		})
		return rs.ScopeSpans().Len() == 0
	})
}
