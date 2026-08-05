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
// payload the bindplane extension produces.
type opampReporter struct {
	logger   *zap.Logger
	opampID  component.ID
	registry *measurements.ResettableThroughputMeasurementsRegistry
	handler  opampcustommessages.CustomCapabilityHandler
	refs     int

	doneChan chan struct{}
	wg       *sync.WaitGroup
}

// reporter is the single reporter shared by all throughput processors
// configured with `opamp`. The first processor to start creates it; the last
// one to shut down tears it down.
var (
	reporterMux sync.Mutex
	reporter    *opampReporter
)

// registerWithOpAMPReporter registers the processor's measurements with the
// shared reporter, creating and starting the reporter if it doesn't exist yet.
func registerWithOpAMPReporter(host component.Host, tmp *throughputMeasurementProcessor) error {
	reporterMux.Lock()
	defer reporterMux.Unlock()

	if reporter == nil {
		r, err := newOpAMPReporter(host, tmp.logger, tmp.opampExtensionID, tmp.interval)
		if err != nil {
			return err
		}
		reporter = r
	} else if reporter.opampID != tmp.opampExtensionID {
		tmp.logger.Warn("Throughput processors are configured with different opamp extensions; using the first one seen.",
			zap.Stringer("using", reporter.opampID), zap.Stringer("ignored", tmp.opampExtensionID))
	}

	reporter.refs++
	if err := reporter.registry.RegisterThroughputMeasurements(tmp.processorID.String(), tmp.measurements); err != nil {
		// Duplicate registration can't happen in practice: the package-level
		// processors map guarantees one instance per component ID.
		tmp.logger.Warn("Failed to register measurements with opamp reporter.", zap.Error(err))
	}

	return nil
}

// releaseOpAMPReporter drops one processor's reference to the shared reporter.
// The last release stops the report loop and unregisters the capability.
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

func newOpAMPReporter(host component.Host, logger *zap.Logger, opampID component.ID, interval time.Duration) (*opampReporter, error) {
	ext, ok := host.GetExtensions()[opampID]
	if !ok {
		return nil, fmt.Errorf("opamp extension %q does not exist", opampID)
	}

	capRegistry, ok := ext.(opampcustommessages.CustomCapabilityRegistry)
	if !ok {
		return nil, fmt.Errorf("extension %q is not an custom message registry", opampID)
	}

	handler, err := capRegistry.Register(measurements.ReportMeasurementsV1Capability)
	if err != nil {
		return nil, fmt.Errorf("register custom capability: %w", err)
	}

	r := &opampReporter{
		logger:   logger,
		opampID:  opampID,
		registry: measurements.NewResettableThroughputMeasurementsRegistry(false),
		handler:  handler,
		doneChan: make(chan struct{}),
		wg:       &sync.WaitGroup{},
	}

	r.wg.Add(1)
	go r.reportLoop(interval)

	return r, nil
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
