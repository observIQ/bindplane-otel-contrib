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
	"errors"
	"fmt"

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottllog"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottlmetric"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottlspan"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"

	"github.com/observiq/bindplane-otel-contrib/internal/posture"
	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
)

var componentType = component.MustNewType("posture")

const stability = component.StabilityLevelAlpha

var (
	consumerCapabilities = consumer.Capabilities{MutatesData: true}
	errInvalidConfigType = errors.New("config is not of type postureprocessor.Config")
)

// NewFactory creates a new factory for the posture processor.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		componentType,
		createDefaultConfig,
		processor.WithLogs(createLogsProcessor, stability),
		processor.WithMetrics(createMetricsProcessor, stability),
		processor.WithTraces(createTracesProcessor, stability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		Levels: posture.DefaultLevels,
		Drain: DrainConfig{
			Interval:       defaultDrainInterval,
			JitterFraction: defaultJitterFraction,
		},
		Buffer: BufferConfig{
			OverflowPolicy: overflowDropOldest,
		},
	}
}

// buildTiers resolves configured tiers plus the implicit default tier into the
// ordered tier slice used by the core (index aligned with the queues).
func buildTiers(cfg *Config, ls posture.LevelSet) ([]tier, error) {
	tiers := make([]tier, 0, len(cfg.Tiers)+1)
	for _, tc := range cfg.Tiers {
		lvl, err := ls.Parse(tc.MinLevel)
		if err != nil {
			return nil, fmt.Errorf("tier %q min_level: %w", tc.Name, err)
		}
		tiers = append(tiers, tier{name: tc.Name, minLevel: lvl})
	}
	defMin := ls.Max()
	if cfg.DefaultMinLevel != "" {
		l, err := ls.Parse(cfg.DefaultMinLevel)
		if err != nil {
			return nil, fmt.Errorf("default_min_level: %w", err)
		}
		defMin = l
	}
	tiers = append(tiers, tier{name: defaultTierName, minLevel: defMin})
	return tiers, nil
}

func setup(cfg component.Config, set processor.Settings, signal string) (*Config, posture.LevelSet, []tier, *core, error) {
	oCfg, ok := cfg.(*Config)
	if !ok {
		return nil, posture.LevelSet{}, nil, nil, errInvalidConfigType
	}
	ls, err := posture.NewLevelSet(oCfg.levelsOrDefault())
	if err != nil {
		return nil, posture.LevelSet{}, nil, nil, err
	}
	tiers, err := buildTiers(oCfg, ls)
	if err != nil {
		return nil, posture.LevelSet{}, nil, nil, err
	}
	cr := newCore(oCfg, ls, tiers, signal, set.ID, set.Logger)
	return oCfg, ls, tiers, cr, nil
}

func createLogsProcessor(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Logs) (processor.Logs, error) {
	oCfg, _, _, cr, err := setup(cfg, set, signalLogs)
	if err != nil {
		return nil, err
	}
	conds := make([]*expr.OTTLCondition[*ottllog.TransformContext], len(oCfg.Tiers))
	for i, tc := range oCfg.Tiers {
		c, err := expr.NewOTTLLogRecordCondition(tc.Condition, set.TelemetrySettings)
		if err != nil {
			return nil, fmt.Errorf("tier %q condition: %w", tc.Name, err)
		}
		conds[i] = c
	}
	lp := &logsProcessor{core: cr, conditions: conds, next: next}
	cr.emit = lp.emit
	return processorhelper.NewLogs(ctx, set, cfg, next, lp.processLogs,
		processorhelper.WithCapabilities(consumerCapabilities),
		processorhelper.WithStart(cr.start),
		processorhelper.WithShutdown(cr.shutdown),
	)
}

func createMetricsProcessor(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Metrics) (processor.Metrics, error) {
	oCfg, _, _, cr, err := setup(cfg, set, signalMetrics)
	if err != nil {
		return nil, err
	}
	conds := make([]*expr.OTTLCondition[*ottlmetric.TransformContext], len(oCfg.Tiers))
	for i, tc := range oCfg.Tiers {
		c, err := expr.NewOTTLMetricCondition(tc.Condition, set.TelemetrySettings)
		if err != nil {
			return nil, fmt.Errorf("tier %q condition: %w", tc.Name, err)
		}
		conds[i] = c
	}
	mp := &metricsProcessor{core: cr, conditions: conds, next: next}
	cr.emit = mp.emit
	return processorhelper.NewMetrics(ctx, set, cfg, next, mp.processMetrics,
		processorhelper.WithCapabilities(consumerCapabilities),
		processorhelper.WithStart(cr.start),
		processorhelper.WithShutdown(cr.shutdown),
	)
}

func createTracesProcessor(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Traces) (processor.Traces, error) {
	oCfg, _, _, cr, err := setup(cfg, set, signalTraces)
	if err != nil {
		return nil, err
	}
	conds := make([]*expr.OTTLCondition[*ottlspan.TransformContext], len(oCfg.Tiers))
	for i, tc := range oCfg.Tiers {
		c, err := expr.NewOTTLSpanCondition(tc.Condition, set.TelemetrySettings)
		if err != nil {
			return nil, fmt.Errorf("tier %q condition: %w", tc.Name, err)
		}
		conds[i] = c
	}
	tp := &tracesProcessor{core: cr, conditions: conds, next: next}
	cr.emit = tp.emit
	return processorhelper.NewTraces(ctx, set, cfg, next, tp.processTraces,
		processorhelper.WithCapabilities(consumerCapabilities),
		processorhelper.WithStart(cr.start),
		processorhelper.WithShutdown(cr.shutdown),
	)
}
