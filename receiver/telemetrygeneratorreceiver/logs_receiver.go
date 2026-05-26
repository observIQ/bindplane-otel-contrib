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
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/internal/blitzpdata"
)

type logsGeneratorReceiver struct {
	telemetryGeneratorReceiver
	nextConsumer consumer.Logs
	generators   []logGenerator
}

// newLogsReceiver creates a new logs specific receiver.
//
// The Start/Shutdown lifecycle (including blitz runner orchestration
// and ticker short-circuit for blitz-only configs) lives on the
// embedded telemetryGeneratorReceiver — the constructor's only job
// is to populate the fields that lifecycle reads: hasTickerGenerators
// and blitzRunner.
func newLogsReceiver(ctx context.Context, logger *zap.Logger, cfg *Config, nextConsumer consumer.Logs) (*logsGeneratorReceiver, error) {
	lr := &logsGeneratorReceiver{
		nextConsumer: nextConsumer,
	}
	r := newTelemetryGeneratorReceiver(ctx, logger, cfg, lr)

	lr.telemetryGeneratorReceiver = r

	var err error
	lr.generators, err = newLogsGenerators(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("new logs generators: %w", err)
	}
	lr.hasTickerGenerators = len(lr.generators) > 0

	if err := lr.buildBlitzRunner(logger, cfg); err != nil {
		return nil, err
	}

	return lr, nil
}

// buildBlitzRunner constructs the embed.Runner from all Type: "blitz"
// entries in cfg, if any. Each entry gets its own LogAdapter
// (preserving per-entry resource + attribute config) wired into the
// modules built for that entry; the modules from every entry are
// aggregated into a single Runner stored on the embedded base type
// so the shared Start/Shutdown lifecycle owns it.
func (r *logsGeneratorReceiver) buildBlitzRunner(logger *zap.Logger, cfg *Config) error {
	var modules []embed.ProducerModule
	for i, g := range cfg.Generators {
		if g.Type != generatorTypeBlitz {
			continue
		}
		// parse_body is validated in validateBlitzGeneratorConfig; the
		// type assertion here is safe — a non-bool would have been
		// rejected at validation time. Absent → false (raw mode).
		parseBody, _ := g.AdditionalConfig[blitzKeyParseBody].(bool)
		adapter := blitzpdata.NewLogAdapter(r.nextConsumer, g.ResourceAttributes, g.Attributes, parseBody, logger)
		mods, err := buildBlitzModules(logger, g, adapter)
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

// produce
func (r *logsGeneratorReceiver) produce() error {
	logs := plog.NewLogs()
	for _, g := range r.generators {
		l := g.generateLogs()
		for i := 0; i < l.ResourceLogs().Len(); i++ {
			src := l.ResourceLogs().At(i)
			src.CopyTo(logs.ResourceLogs().AppendEmpty())
		}
	}
	if logs.ResourceLogs().Len() == 0 {
		// Pure-blitz configs have zero pull-style generators; skip the
		// no-op consume call so downstream pipelines aren't woken every
		// tick. (Belt-and-suspenders: the lifecycle short-circuits the
		// ticker entirely when hasTickerGenerators is false, but this
		// guard also covers the produce-as-called case in tests.)
		return nil
	}
	return r.nextConsumer.ConsumeLogs(r.ctx, logs)
}
