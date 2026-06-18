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

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottlmetric"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
)

// metricsProcessor classifies at metric granularity. (Datapoint-granularity
// classification is a possible future enhancement; today a metric's datapoints
// are kept together in whichever tier the metric matches.)
type metricsProcessor struct {
	core        *core
	conditions  []*expr.OTTLCondition[*ottlmetric.TransformContext] // aligned with cfg.Tiers
	next        consumer.Metrics
	marshaler   pmetric.ProtoMarshaler
	unmarshaler pmetric.ProtoUnmarshaler
}

func (p *metricsProcessor) classify(ctx context.Context, rm pmetric.ResourceMetrics, sm pmetric.ScopeMetrics, m pmetric.Metric) int {
	for i, cond := range p.conditions {
		match, err := cond.Match(ctx, ottlmetric.NewTransformContextPtr(rm, sm, m))
		if err == nil && match {
			return i
		}
	}
	return len(p.conditions)
}

func (p *metricsProcessor) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	level := p.core.currentLevel()
	buffered := make([]*pmetric.Metrics, len(p.core.tiers))

	for i := 0; i < md.ResourceMetrics().Len(); i++ {
		rm := md.ResourceMetrics().At(i)
		for j := 0; j < rm.ScopeMetrics().Len(); j++ {
			sm := rm.ScopeMetrics().At(j)
			destScopes := make([]pmetric.ScopeMetrics, len(p.core.tiers))
			created := make([]bool, len(p.core.tiers))

			sm.Metrics().RemoveIf(func(m pmetric.Metric) bool {
				idx := p.classify(ctx, rm, sm, m)
				if level >= p.core.minLevelFor(idx) {
					return false
				}
				if !created[idx] {
					if buffered[idx] == nil {
						b := pmetric.NewMetrics()
						buffered[idx] = &b
					}
					destRM := buffered[idx].ResourceMetrics().AppendEmpty()
					rm.Resource().CopyTo(destRM.Resource())
					destRM.SetSchemaUrl(rm.SchemaUrl())
					ds := destRM.ScopeMetrics().AppendEmpty()
					sm.Scope().CopyTo(ds.Scope())
					ds.SetSchemaUrl(sm.SchemaUrl())
					destScopes[idx] = ds
					created[idx] = true
				}
				m.CopyTo(destScopes[idx].Metrics().AppendEmpty())
				return true
			})
		}
	}

	pruneEmptyResourceMetrics(md)
	p.enqueueBuffered(ctx, buffered)
	return md, nil
}

func (p *metricsProcessor) enqueueBuffered(ctx context.Context, buffered []*pmetric.Metrics) {
	for idx, b := range buffered {
		if b == nil {
			continue
		}
		payload, err := p.marshaler.MarshalMetrics(*b)
		if err != nil {
			p.core.logger.Error("failed to marshal buffered metrics", zap.String("tier", p.core.tiers[idx].name), zap.Error(err))
			continue
		}
		if err := p.core.queues[idx].enqueue(ctx, payload); err != nil {
			p.core.logger.Error("failed to buffer metrics", zap.String("tier", p.core.tiers[idx].name), zap.Error(err))
		}
	}
}

func (p *metricsProcessor) emit(ctx context.Context, payload []byte) error {
	md, err := p.unmarshaler.UnmarshalMetrics(payload)
	if err != nil {
		return err
	}
	return p.next.ConsumeMetrics(ctx, md)
}

func pruneEmptyResourceMetrics(md pmetric.Metrics) {
	md.ResourceMetrics().RemoveIf(func(rm pmetric.ResourceMetrics) bool {
		rm.ScopeMetrics().RemoveIf(func(sm pmetric.ScopeMetrics) bool {
			return sm.Metrics().Len() == 0
		})
		return rm.ScopeMetrics().Len() == 0
	})
}
