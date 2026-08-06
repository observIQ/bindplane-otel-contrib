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

// opampReporter aggregates measurements from every throughput processor in the
// collector into a single opamp custom message per interval, matching the
// payload the bindplane extension produces. Every processor feeds it; it only
// reports once a processor carrying the `global` config block configures it.
type opampReporter struct {
	registry *measurements.ResettableThroughputMeasurementsRegistry
	refs     int

	// Configured state, applied by configureOpAMPReporter from the `global`
	// config block. handler is nil while the reporter is dormant.
	logger          *zap.Logger
	handler         opampcustommessages.CustomCapabilityHandler
	extraAttributes map[string]string
	doneChan        chan struct{}
	wg              *sync.WaitGroup
}

// reporter is the single reporter shared by all throughput processors. The
// first processor to start creates it; the last one to shut down tears it
// down.
var (
	reporterMux sync.Mutex
	reporter    *opampReporter
)

// registerWithOpAMPReporter registers the processor's measurements with the
// shared reporter, creating it (dormant) if it doesn't exist yet. Every
// throughput processor feeds the reporter; only a processor carrying the
// `global` config block configures it (see configureOpAMPReporter).
func registerWithOpAMPReporter(tmp *throughputMeasurementProcessor) {
	reporterMux.Lock()
	defer reporterMux.Unlock()

	if reporter == nil {
		reporter = &opampReporter{
			registry: measurements.NewResettableThroughputMeasurementsRegistry(false),
		}
	}

	reporter.refs++
	if err := reporter.registry.RegisterThroughputMeasurements(tmp.processorID.String(), tmp.measurements); err != nil {
		// Duplicate registration can't happen in practice: the package-level
		// processors map guarantees one instance per component ID.
		tmp.logger.Warn("Failed to register measurements with opamp reporter.", zap.Error(err))
	}
}

// configureOpAMPReporter applies the `global` config block to the shared
// reporter: it registers the custom capability with the opamp extension and
// starts the report loop. If the reporter is already configured (more than one
// processor carries a `global` block), the previous configuration is torn down
// first — the last processor to start wins. Must be called after
// registerWithOpAMPReporter.
func configureOpAMPReporter(host component.Host, logger *zap.Logger, global GlobalConfig) error {
	capRegistry, err := getCustomCapabilityRegistry(host, global.OpAMP)
	if err != nil {
		return err
	}

	// Measurements reporting is disabled if the interval is 0, matching the
	// bindplane extension; the opamp extension must still exist and support
	// custom messages (checked above).
	if global.Interval <= 0 {
		return nil
	}

	reporterMux.Lock()
	defer reporterMux.Unlock()

	// Last one wins: tear down any previous configuration.
	if reporter.handler != nil {
		close(reporter.doneChan)
		reporter.wg.Wait()
		reporter.handler.Unregister()
		reporter.handler = nil
	}

	handler, err := capRegistry.Register(measurements.ReportMeasurementsV1Capability)
	if err != nil {
		return fmt.Errorf("register custom capability: %w", err)
	}

	reporter.logger = logger
	reporter.handler = handler
	reporter.extraAttributes = global.ExtraMeasurementAttributes
	reporter.doneChan = make(chan struct{})
	reporter.wg = &sync.WaitGroup{}

	reporter.wg.Add(1)
	go reporter.reportLoop(global.Interval)

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
