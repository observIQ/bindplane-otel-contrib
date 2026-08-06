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

package throughputmeasurementprocessor

import (
	"context"
	"fmt"
	"math/rand"
	"sync/atomic"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/pkg/measurements"
)

type throughputMeasurementProcessor struct {
	logger              *zap.Logger
	enabled             bool
	measurements        *measurements.ThroughputMeasurements
	samplingCutOffRatio float64
	processorID         component.ID
	opampExtensionID    component.ID
	global              *GlobalConfig
	// bindplane exists only for backwards compatibility with Bindplane
	// servers that don't render `opamp`; delete with BPOP-5622.
	bindplane          component.ID
	measureLogRawBytes bool

	// registeredWithReporter records that start registered with the shared
	// opamp reporter, so shutdown knows to release it.
	registeredWithReporter bool

	started *atomic.Bool
	stopped *atomic.Bool
}

func newThroughputMeasurementProcessor(logger *zap.Logger, mp metric.MeterProvider, cfg *Config, processorID component.ID) (*throughputMeasurementProcessor, error) {
	measurements, err := measurements.NewThroughputMeasurements(mp, processorID.String(), cfg.ExtraLabels)
	if err != nil {
		return nil, fmt.Errorf("create throughput measurements: %w", err)
	}

	return &throughputMeasurementProcessor{
		logger:              logger,
		enabled:             cfg.Enabled,
		measurements:        measurements,
		samplingCutOffRatio: cfg.SamplingRatio,
		processorID:         processorID,
		opampExtensionID:    cfg.OpAMP,
		global:              cfg.Global,
		bindplane:           cfg.BindplaneExtension,
		measureLogRawBytes:  cfg.MeasureLogRawBytes,

		started: &atomic.Bool{},
		stopped: &atomic.Bool{},
	}, nil
}

func (tmp *throughputMeasurementProcessor) start(_ context.Context, host component.Host) error {
	if tmp.started.Swap(true) {
		// Start logic should only be run once
		return nil
	}

	// Every throughput processor feeds the shared reporter.
	registerWithOpAMPReporter(tmp)
	tmp.registeredWithReporter = true

	// The `global` block's settings apply to the shared reporter regardless of
	// which processor carries it; applied before the extension is wired so a
	// processor carrying both starts the loop with its own settings.
	if tmp.global != nil {
		if err := applyGlobalSettings(*tmp.global); err != nil {
			return err
		}
	}

	var emptyID component.ID
	switch {
	case tmp.opampExtensionID != emptyID:
		if tmp.bindplane != emptyID {
			tmp.logger.Warn("Both opamp and bindplane_extension are set; using opamp. bindplane_extension is deprecated.")
		}
		return setOpAMPExtension(host, tmp.logger, tmp.opampExtensionID)

	// Both fallback cases below exist only for backwards compatibility with
	// Bindplane servers that don't render `opamp`; delete them (and make opamp
	// reporting the only path) with BPOP-5622.
	case tmp.bindplane != emptyID:
		tmp.logger.Warn("bindplane_extension is deprecated; configure opamp instead.")
		ext, ok := host.GetExtensions()[tmp.bindplane]
		if !ok {
			// Old Bindplane servers render bindplane_extension without instantiating
			// the extension (v1 agents ignored the field entirely); treat this the
			// same as the neither-set case below.
			tmp.registerWithV1AgentRegistry()
			return nil
		}

		// v2/byoc agent, use the configured Bindplane extension for backwards compatibility.
		if err := tmp.registerWithV2AgentRegistry(ext); err != nil {
			return fmt.Errorf("register with bindplane extension: %q", err)
		}

	default:
		// Neither opamp nor bindplane_extension is configured, meaning this is a
		// v1 bindplane agent or standalone collector.
		tmp.registerWithV1AgentRegistry()
	}

	return nil
}

// registerWithV1AgentRegistry registers the measurements with the package-level
// registry that the v1 bindplane agent runtime reads. Never fatal: duplicate
// registration (e.g. a config reload without a registry reset) only warns, and
// outside a v1 agent the registration is simply inert.
func (tmp *throughputMeasurementProcessor) registerWithV1AgentRegistry() {
	if err := measurements.BindplaneAgentThroughputMeasurementsRegistry.RegisterThroughputMeasurements(tmp.processorID.String(), tmp.measurements); err != nil {
		tmp.logger.Warn("Failed to register measurements with bindplane agent registry.", zap.Error(err))
	}
}

// registerWithV2AgentRegistry registers the measurements with the Bindplane extension. This follows the
// existing pattern used when the Bindplane extension is configured.
func (tmp *throughputMeasurementProcessor) registerWithV2AgentRegistry(bindplane component.Component) error {
	registry, ok := bindplane.(measurements.ThroughputMeasurementsRegistry)
	if !ok {
		return fmt.Errorf("extension %q is not an throughput message registry", tmp.bindplane)
	}

	if err := registry.RegisterThroughputMeasurements(tmp.processorID.String(), tmp.measurements); err != nil {
		return fmt.Errorf("register throughput measurements: %w", err)
	}

	return nil
}

func (tmp *throughputMeasurementProcessor) processTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	if tmp.enabled {
		//#nosec G404 -- randomly generated number is not used for security purposes. It's ok if it's weak
		if rand.Float64() <= tmp.samplingCutOffRatio {
			tmp.measurements.AddTraces(ctx, td)
		}
	}

	return td, nil
}

func (tmp *throughputMeasurementProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	if tmp.enabled {
		//#nosec G404 -- randomly generated number is not used for security purposes. It's ok if it's weak
		if rand.Float64() <= tmp.samplingCutOffRatio {
			tmp.measurements.AddLogs(ctx, ld, tmp.measureLogRawBytes)
		}
	}

	return ld, nil
}

func (tmp *throughputMeasurementProcessor) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	if tmp.enabled {
		//#nosec G404 -- randomly generated number is not used for security purposes. It's ok if it's weak
		if rand.Float64() <= tmp.samplingCutOffRatio {
			tmp.measurements.AddMetrics(ctx, md)
		}
	}

	return md, nil
}

func (tmp *throughputMeasurementProcessor) shutdown(ctx context.Context) error {
	if tmp.stopped.Swap(true) {
		// Stop logic should only be run once
		return nil
	}

	unregisterProcessor(tmp.processorID)

	if tmp.registeredWithReporter {
		return releaseOpAMPReporter(ctx)
	}

	return nil
}
