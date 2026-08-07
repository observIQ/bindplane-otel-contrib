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
	"sync"
	"time"

	"github.com/golang/snappy"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/pkg/measurements"
)

// opampReporter aggregates measurements from every `opamp`-configured
// throughput processor in the collector into a single opamp custom message
// per interval, matching the payload the bindplane extension produces. Its
// settings come from the `global` config block carried by one processor
// (defaults otherwise).
type opampReporter struct {
	registry *measurements.ResettableThroughputMeasurementsRegistry
	refs     int

	logger          *zap.Logger
	opampID         component.ID
	capRegistry     opampcustommessages.CustomCapabilityRegistry
	handler         opampcustommessages.CustomCapabilityHandler
	interval        time.Duration
	extraAttributes map[string]string
	doneChan        chan struct{}
	wg              *sync.WaitGroup
}

// reporter is the single reporter shared by all `opamp`-configured throughput
// processors. The first of them to start creates it; the last one to shut
// down tears it down.
var (
	reporterMux sync.Mutex
	reporter    *opampReporter
)

// registerWithOpAMPReporter registers the processor's measurements with the
// shared reporter, creating it (dormant, with default settings) if it doesn't
// exist yet. Only processors with `opamp` set feed the reporter.
func registerWithOpAMPReporter(tmp *throughputMeasurementProcessor) {
	reporterMux.Lock()
	defer reporterMux.Unlock()

	if reporter == nil {
		reporter = &opampReporter{
			registry: measurements.NewResettableThroughputMeasurementsRegistry(false),
			interval: time.Minute,
		}
	}

	reporter.refs++
	if err := reporter.registry.RegisterThroughputMeasurements(tmp.processorID.String(), tmp.measurements); err != nil {
		// Duplicate registration can't happen in practice: the package-level
		// processors map guarantees one instance per component ID.
		tmp.logger.Warn("Failed to register measurements with opamp reporter.", zap.Error(err))
	}
}

// setOpAMPExtension points the shared reporter at the opamp extension and
// starts reporting. Called by every processor with `opamp` set: a repeat of
// the extension already in use is a no-op, a different one wins (last one to
// start). Must be called after registerWithOpAMPReporter.
func setOpAMPExtension(host component.Host, logger *zap.Logger, opampID component.ID) error {
	capRegistry, err := getCustomCapabilityRegistry(host, opampID)
	if err != nil {
		return err
	}

	reporterMux.Lock()
	defer reporterMux.Unlock()

	if reporter.capRegistry != nil && reporter.opampID == opampID {
		return nil
	}

	reporter.stopLocked()
	reporter.logger = logger
	reporter.opampID = opampID
	reporter.capRegistry = capRegistry
	return reporter.startLocked()
}

// applyGlobalSettings applies the `global` config block to the shared
// reporter, restarting the report loop if it is running. If more than one
// processor carries a `global` block, the last one to start wins. Must be
// called after registerWithOpAMPReporter.
func applyGlobalSettings(global GlobalConfig) error {
	reporterMux.Lock()
	defer reporterMux.Unlock()

	reporter.stopLocked()
	reporter.interval = global.Interval
	reporter.extraAttributes = global.ExtraMeasurementAttributes
	return reporter.startLocked()
}

// stopLocked stops the report loop and unregisters the capability. The
// reporter mutex must be held.
func (r *opampReporter) stopLocked() {
	if r.handler == nil {
		return
	}

	close(r.doneChan)
	r.wg.Wait()
	r.handler.Unregister()
	r.handler = nil
}

// startLocked registers the capability and starts the report loop, if an
// opamp extension is set and the interval is positive — reporting is disabled
// on a 0 interval, matching the bindplane extension. The reporter mutex must
// be held.
func (r *opampReporter) startLocked() error {
	if r.capRegistry == nil || r.interval <= 0 {
		return nil
	}

	handler, err := r.capRegistry.Register(measurements.ReportMeasurementsV1Capability)
	if err != nil {
		return fmt.Errorf("register custom capability: %w", err)
	}

	r.handler = handler
	r.doneChan = make(chan struct{})
	r.wg = &sync.WaitGroup{}

	r.wg.Add(1)
	go r.reportLoop(r.interval)

	return nil
}

// releaseOpAMPReporter drops one processor's reference to the shared reporter.
// The last release stops the report loop (if configured) and unregisters the
// capability.
func releaseOpAMPReporter(ctx context.Context) error {
	reporterMux.Lock()
	defer reporterMux.Unlock()

	if reporter == nil {
		return nil
	}

	reporter.refs--
	if reporter.refs > 0 {
		return nil
	}

	r := reporter
	reporter = nil

	if r.handler == nil {
		// Never configured; there is no loop to stop.
		return nil
	}

	close(r.doneChan)

	waitgroupDone := make(chan struct{})
	go func() {
		defer close(waitgroupDone)
		r.wg.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-waitgroupDone: // OK
	}

	r.handler.Unregister()
	return nil
}

// getCustomCapabilityRegistry resolves the opamp extension from the host as a
// custom capability registry.
func getCustomCapabilityRegistry(host component.Host, opampID component.ID) (opampcustommessages.CustomCapabilityRegistry, error) {
	ext, ok := host.GetExtensions()[opampID]
	if !ok {
		return nil, fmt.Errorf("opamp extension %q does not exist", opampID)
	}

	capRegistry, ok := ext.(opampcustommessages.CustomCapabilityRegistry)
	if !ok {
		return nil, fmt.Errorf("extension %q is not an custom message registry", opampID)
	}

	return capRegistry, nil
}

func (r *opampReporter) reportLoop(interval time.Duration) {
	defer r.wg.Done()

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			if err := r.report(); err != nil {
				r.logger.Error("Failed to report throughput measurements.", zap.Error(err))
			}
		case <-r.doneChan:
			return
		}
	}
}

func (r *opampReporter) report() error {
	m := r.registry.OTLPMeasurements(nil)
	r.applyExtraAttributes(m)

	// Send metrics as snappy-encoded otlp proto
	marshaller := pmetric.ProtoMarshaler{}
	marshalled, err := marshaller.MarshalMetrics(m)
	if err != nil {
		return fmt.Errorf("marshal metrics: %w", err)
	}

	encoded := snappy.Encode(nil, marshalled)
	for {
		sendingChannel, err := r.handler.SendMessage(measurements.ReportMeasurementsType, encoded)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, types.ErrCustomMessagePending):
			<-sendingChannel
			continue
		default:
			return fmt.Errorf("send custom throughput message: %w", err)
		}
	}
}

// applyExtraAttributes stamps the global extra measurement attributes on every
// datapoint. Attributes already present — including a processor's own
// extra_labels — win on conflicting keys.
func (r *opampReporter) applyExtraAttributes(m pmetric.Metrics) {
	if len(r.extraAttributes) == 0 {
		return
	}

	rms := m.ResourceMetrics()
	for i := 0; i < rms.Len(); i++ {
		sms := rms.At(i).ScopeMetrics()
		for j := 0; j < sms.Len(); j++ {
			ms := sms.At(j).Metrics()
			for k := 0; k < ms.Len(); k++ {
				dps := ms.At(k).Sum().DataPoints()
				for l := 0; l < dps.Len(); l++ {
					attrs := dps.At(l).Attributes()
					for key, value := range r.extraAttributes {
						if _, ok := attrs.Get(key); !ok {
							attrs.PutStr(key, value)
						}
					}
				}
			}
		}
	}
}
