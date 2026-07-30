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
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/snappy"
	"github.com/jonboulle/clockwork"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
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
	// bindplaneExtensionID exists only for backwards compatibility with Bindplane
	// servers that don't render `opamp`; delete with BPOP-5622.
	bindplaneExtensionID component.ID
	interval             time.Duration
	measureLogRawBytes   bool

	clock                   clockwork.Clock
	customCapabilityHandler opampcustommessages.CustomCapabilityHandler
	lastReportedSequence    int64

	started  *atomic.Bool
	stopped  *atomic.Bool
	doneChan chan struct{}
	wg       *sync.WaitGroup
}

func newThroughputMeasurementProcessor(logger *zap.Logger, mp metric.MeterProvider, cfg *Config, processorID component.ID) (*throughputMeasurementProcessor, error) {
	measurements, err := measurements.NewThroughputMeasurements(mp, processorID.String(), cfg.ExtraLabels)
	if err != nil {
		return nil, fmt.Errorf("create throughput measurements: %w", err)
	}

	return &throughputMeasurementProcessor{
		logger:               logger,
		enabled:              cfg.Enabled,
		measurements:         measurements,
		samplingCutOffRatio:  cfg.SamplingRatio,
		processorID:          processorID,
		opampExtensionID:     cfg.OpAMP,
		bindplaneExtensionID: cfg.BindplaneExtension,
		interval:             cfg.Interval,
		measureLogRawBytes:   cfg.MeasureLogRawBytes,

		clock: clockwork.NewRealClock(),

		started:  &atomic.Bool{},
		stopped:  &atomic.Bool{},
		doneChan: make(chan struct{}),
		wg:       &sync.WaitGroup{},
	}, nil
}

func (tmp *throughputMeasurementProcessor) start(_ context.Context, host component.Host) error {
	if tmp.started.Swap(true) {
		// Start logic should only be run once
		return nil
	}

	var emptyID component.ID
	switch {
	case tmp.opampExtensionID != emptyID:
		if tmp.bindplaneExtensionID != emptyID {
			tmp.logger.Warn("Both opamp and bindplane_extension are set; using opamp. bindplane_extension is deprecated.")
		}
		return tmp.startOpAMPReporting(host)

	// Both fallback cases below exist only for backwards compatibility with
	// Bindplane servers that don't render `opamp`; delete them (and make opamp
	// reporting the only path) with BPOP-5622.
	case tmp.bindplaneExtensionID != emptyID:
		tmp.logger.Warn("bindplane_extension is deprecated; configure opamp instead.")
		ext, ok := host.GetExtensions()[tmp.bindplaneExtensionID]
		if !ok {
			// Old Bindplane servers render bindplane_extension without instantiating
			// the extension (v1 agents ignored the field entirely); treat this the
			// same as the neither-set case below.
			tmp.registerWithAgentRegistry()
			return nil
		}

		registry, ok := ext.(measurements.ThroughputMeasurementsRegistry)
		if !ok {
			return fmt.Errorf("extension %q is not an throughput message registry", tmp.bindplaneExtensionID)
		}

		if err := registry.RegisterThroughputMeasurements(tmp.processorID.String(), tmp.measurements); err != nil {
			return fmt.Errorf("register throughput measurements: %w", err)
		}

	default:
		// Neither opamp nor bindplane_extension is configured, meaning this is a
		// v1 bindplane agent (or a standalone collector).
		tmp.registerWithAgentRegistry()
	}

	return nil
}

// registerWithAgentRegistry registers the measurements with the package-level
// registry that the v1 bindplane agent runtime reads. Never fatal: duplicate
// registration (e.g. a config reload without a registry reset) only warns, and
// outside a v1 agent the registration is simply inert.
func (tmp *throughputMeasurementProcessor) registerWithAgentRegistry() {
	if err := measurements.BindplaneAgentThroughputMeasurementsRegistry.RegisterThroughputMeasurements(tmp.processorID.String(), tmp.measurements); err != nil {
		tmp.logger.Warn("Failed to register measurements with bindplane agent registry.", zap.Error(err))
	}
}

func (tmp *throughputMeasurementProcessor) startOpAMPReporting(host component.Host) error {
	ext, ok := host.GetExtensions()[tmp.opampExtensionID]
	if !ok {
		return fmt.Errorf("opamp extension %q does not exist", tmp.opampExtensionID)
	}

	registry, ok := ext.(opampcustommessages.CustomCapabilityRegistry)
	if !ok {
		return fmt.Errorf("extension %q is not an custom message registry", tmp.opampExtensionID)
	}

	var err error
	tmp.customCapabilityHandler, err = registry.Register(measurements.ReportMeasurementsV1Capability)
	if err != nil {
		return fmt.Errorf("register custom capability: %w", err)
	}

	tmp.wg.Add(1)
	go tmp.reportMeasurementsLoop()

	return nil
}

func (tmp *throughputMeasurementProcessor) reportMeasurementsLoop() {
	defer tmp.wg.Done()

	t := tmp.clock.NewTicker(tmp.interval)
	defer t.Stop()

	for {
		select {
		case <-t.Chan():
			if err := tmp.reportMeasurements(); err != nil {
				tmp.logger.Error("Failed to report throughput measurements.", zap.Error(err))
			}
		case <-tmp.doneChan:
			return
		}
	}
}

func (tmp *throughputMeasurementProcessor) reportMeasurements() error {
	seq := tmp.measurements.SequenceNumber()
	if seq == tmp.lastReportedSequence {
		// No new measurements since the last report
		return nil
	}

	m := pmetric.NewMetrics()
	sm := m.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty()
	measurements.OTLPThroughputMeasurements(tmp.measurements, false, nil).MoveAndAppendTo(sm.Metrics())

	// Send metrics as snappy-encoded otlp proto
	marshaller := pmetric.ProtoMarshaler{}
	marshalled, err := marshaller.MarshalMetrics(m)
	if err != nil {
		return fmt.Errorf("marshal metrics: %w", err)
	}

	encoded := snappy.Encode(nil, marshalled)
	for {
		sendingChannel, err := tmp.customCapabilityHandler.SendMessage(measurements.ReportMeasurementsType, encoded)
		switch {
		case err == nil:
			tmp.lastReportedSequence = seq
			return nil
		case errors.Is(err, types.ErrCustomMessagePending):
			<-sendingChannel
			continue
		default:
			return fmt.Errorf("send custom throughput message: %w", err)
		}
	}
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

	close(tmp.doneChan)

	waitgroupDone := make(chan struct{})
	go func() {
		tmp.wg.Wait()
		close(waitgroupDone)
	}()

	select {
	case <-waitgroupDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	if tmp.customCapabilityHandler != nil {
		tmp.customCapabilityHandler.Unregister()
	}

	return nil
}
