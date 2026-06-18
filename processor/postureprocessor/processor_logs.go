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

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottllog"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
)

type logsProcessor struct {
	core        *core
	conditions  []*expr.OTTLCondition[*ottllog.TransformContext] // aligned with cfg.Tiers
	next        consumer.Logs
	marshaler   plog.ProtoMarshaler
	unmarshaler plog.ProtoUnmarshaler
}

func (p *logsProcessor) classify(ctx context.Context, rl plog.ResourceLogs, sl plog.ScopeLogs, lr plog.LogRecord) int {
	for i, cond := range p.conditions {
		match, err := cond.Match(ctx, ottllog.NewTransformContextPtr(rl, sl, lr))
		if err == nil && match {
			return i
		}
	}
	return len(p.conditions) // default tier index
}

func (p *logsProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	level := p.core.currentLevel()
	buffered := make([]*plog.Logs, len(p.core.tiers))

	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		rl := ld.ResourceLogs().At(i)
		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			sl := rl.ScopeLogs().At(j)
			destScopes := make([]plog.ScopeLogs, len(p.core.tiers))
			created := make([]bool, len(p.core.tiers))

			sl.LogRecords().RemoveIf(func(lr plog.LogRecord) bool {
				idx := p.classify(ctx, rl, sl, lr)
				if level >= p.core.minLevelFor(idx) {
					return false // forward now
				}
				if !created[idx] {
					if buffered[idx] == nil {
						b := plog.NewLogs()
						buffered[idx] = &b
					}
					destRL := buffered[idx].ResourceLogs().AppendEmpty()
					rl.Resource().CopyTo(destRL.Resource())
					destRL.SetSchemaUrl(rl.SchemaUrl())
					ds := destRL.ScopeLogs().AppendEmpty()
					sl.Scope().CopyTo(ds.Scope())
					ds.SetSchemaUrl(sl.SchemaUrl())
					destScopes[idx] = ds
					created[idx] = true
				}
				lr.CopyTo(destScopes[idx].LogRecords().AppendEmpty())
				return true // remove from the forwarded set
			})
		}
	}

	pruneEmptyResourceLogs(ld)
	p.enqueueBuffered(ctx, buffered)
	return ld, nil
}

func (p *logsProcessor) enqueueBuffered(ctx context.Context, buffered []*plog.Logs) {
	for idx, b := range buffered {
		if b == nil {
			continue
		}
		payload, err := p.marshaler.MarshalLogs(*b)
		if err != nil {
			p.core.logger.Error("failed to marshal buffered logs", zap.String("tier", p.core.tiers[idx].name), zap.Error(err))
			continue
		}
		if err := p.core.queues[idx].enqueue(ctx, payload); err != nil {
			p.core.logger.Error("failed to buffer logs", zap.String("tier", p.core.tiers[idx].name), zap.Error(err))
		}
	}
}

func (p *logsProcessor) emit(ctx context.Context, payload []byte) error {
	ld, err := p.unmarshaler.UnmarshalLogs(payload)
	if err != nil {
		return err
	}
	return p.next.ConsumeLogs(ctx, ld)
}

func pruneEmptyResourceLogs(ld plog.Logs) {
	ld.ResourceLogs().RemoveIf(func(rl plog.ResourceLogs) bool {
		rl.ScopeLogs().RemoveIf(func(sl plog.ScopeLogs) bool {
			return sl.LogRecords().Len() == 0
		})
		return rl.ScopeLogs().Len() == 0
	})
}
