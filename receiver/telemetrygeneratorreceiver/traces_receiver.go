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

package telemetrygeneratorreceiver //import "github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver"

import (
	"context"
	"fmt"

	"github.com/observiq/blitz/embed"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/internal/blitzpdata"
)

type tracesGeneratorReceiver struct {
	telemetryGeneratorReceiver
	nextConsumer consumer.Traces
	generators   []traceGenerator
}

// newTracesReceiver creates a new traces specific receiver.
//
// The Start/Shutdown lifecycle (including blitz runner orchestration
// and ticker short-circuit) lives on the embedded
// telemetryGeneratorReceiver — the constructor's only job is to
// populate the fields that lifecycle reads: hasTickerGenerators and
// blitzRunner.
func newTracesReceiver(ctx context.Context, logger *zap.Logger, cfg *Config, nextConsumer consumer.Traces) (*tracesGeneratorReceiver, error) {
	tr := &tracesGeneratorReceiver{
		nextConsumer: nextConsumer,
	}

	r := newTelemetryGeneratorReceiver(ctx, logger, cfg, tr)

	tr.telemetryGeneratorReceiver = r

	var err error
	tr.generators, err = newTraceGenerators(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("new traces generators: %w", err)
	}
	tr.hasTickerGenerators = len(tr.generators) > 0

	if err := tr.buildBlitzRunner(logger, cfg); err != nil {
		return nil, err
	}

	return tr, nil
}

// buildBlitzRunner constructs the embed.Runner from all Type: "blitz"
// entries in cfg, if any. Each entry gets its own TraceAdapter
// (preserving per-entry lockable resource + attribute config) wired into the
// modules built for that entry; the modules from every entry are
// aggregated into a single Runner stored on the embedded base type so
// the shared Start/Shutdown lifecycle owns it.
func (r *tracesGeneratorReceiver) buildBlitzRunner(logger *zap.Logger, cfg *Config) error {
	var modules []embed.ProducerModule
	for i, g := range cfg.Generators {
		if g.Type != generatorTypeBlitz {
			continue
		}
		// Attribute shapes were validated at config-validate time; re-parse
		// here to build the adapter's base + locked-key structures.
		resourceCfg, err := blitzpdata.ParseLockableAttrs(g.ResourceAttributes, "resource_attributes")
		if err != nil {
			return fmt.Errorf("blitz generator[%d]: %w", i, err)
		}
		attrsCfg, err := blitzpdata.ParseLockableAttrs(g.Attributes, "attributes")
		if err != nil {
			return fmt.Errorf("blitz generator[%d]: %w", i, err)
		}
		adapter := blitzpdata.NewTraceAdapter(r.nextConsumer, resourceCfg, attrsCfg, logger)
		mods, err := buildBlitzModules(logger, g, blitzConsumers{traces: adapter})
		if err != nil {
			return fmt.Errorf("blitz generator[%d]: %w", i, err)
		}
		modules = append(modules, mods...)
	}
	if len(modules) == 0 {
		return nil
	}
	runner, err := embed.New(embed.Config{Modules: modules})
	if err != nil {
		return fmt.Errorf("construct blitz runner: %w", err)
	}
	r.blitzRunner = runner
	return nil
}

// produce generates traces from each generator and sends them to the next consumer
func (r *tracesGeneratorReceiver) produce() error {
	traces := ptrace.NewTraces()
	for _, g := range r.generators {
		t := g.generateTraces()
		for i := 0; i < t.ResourceSpans().Len(); i++ {
			src := t.ResourceSpans().At(i)
			src.CopyTo(traces.ResourceSpans().AppendEmpty())
		}
	}
	return r.nextConsumer.ConsumeTraces(r.ctx, traces)
}
