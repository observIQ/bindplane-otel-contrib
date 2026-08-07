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

package topologyprocessor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang/snappy"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

// opampReporter aggregates topology state from every topology processor in the
// collector into a single opamp custom message per interval, matching the
// payload the bindplane extension produces. Only `opamp`-configured
// processors feed it. It is set up by the processor carrying the `global`
// config block; without one it stays dormant and nothing is reported.
type opampReporter struct {
	registry *ResettableTopologyRegistry
	refs     int

	logger         *zap.Logger
	capRegistry    opampcustommessages.CustomCapabilityRegistry
	handler        opampcustommessages.CustomCapabilityHandler
	interval       time.Duration
	configuration  string
	organizationID string
	accountID      string
	doneChan       chan struct{}
	wg             *sync.WaitGroup
}

// reporter is the single reporter shared by all `opamp`-configured topology
// processors. The first of them to start creates it; the last one to shut
// down tears it down.
var (
	reporterMux sync.Mutex
	reporter    *opampReporter
)

// registerWithOpAMPReporter registers the processor's topology state with the
// shared reporter, creating it (dormant) if it doesn't exist yet. Only
// processors with `opamp` set feed the reporter.
func registerWithOpAMPReporter(tp *topologyProcessor) {
	reporterMux.Lock()
	defer reporterMux.Unlock()

	if reporter == nil {
		reporter = &opampReporter{
			registry: NewResettableTopologyRegistry(),
		}
	}

	reporter.refs++
	if err := reporter.registry.RegisterTopologyState(tp.processorID.String(), tp.topology); err != nil {
		// Duplicate registration can't happen in practice: the package-level
		// processors map guarantees one instance per component ID.
		tp.logger.Warn("Failed to register topology state with opamp reporter.", zap.Error(err))
	}
}

// configureOpAMPReporter sets up the shared reporter: it registers the custom
// capability with the opamp extension and starts the report loop. Only the
// processor carrying the `global` config block calls it; if more than one
// does, the last one to start wins and the previous setup is torn down. Must
// be called after registerWithOpAMPReporter.
func configureOpAMPReporter(host component.Host, logger *zap.Logger, opampID component.ID, global GlobalConfig) error {
	capRegistry, err := getCustomCapabilityRegistry(host, opampID)
	if err != nil {
		return err
	}

	reporterMux.Lock()
	defer reporterMux.Unlock()

	reporter.stopLocked()
	reporter.logger = logger
	reporter.capRegistry = capRegistry
	reporter.interval = global.Interval
	reporter.configuration = global.Configuration
	reporter.organizationID = global.OrganizationID
	reporter.accountID = global.AccountID
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

	handler, err := r.capRegistry.Register(ReportTopologyCapability)
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
				r.logger.Error("Failed to report topology.", zap.Error(err))
			}
		case <-r.doneChan:
			return
		}
	}
}

func (r *opampReporter) report() error {
	ts := r.registry.TopologyInfos()

	// Stamp the gateway identity from the `global` config block on every
	// source; the per-processor GatewayID is preserved.
	for i := range ts {
		ts[i].GatewaySource.Configuration = r.configuration
		ts[i].GatewaySource.OrganizationID = r.organizationID
		ts[i].GatewaySource.AccountID = r.accountID
	}

	// Send topology state snappy-encoded
	marshalled, err := json.Marshal(ts)
	if err != nil {
		return fmt.Errorf("marshal topology state: %w", err)
	}

	encoded := snappy.Encode(nil, marshalled)
	for {
		sendingChannel, err := r.handler.SendMessage(ReportTopologyType, encoded)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, types.ErrCustomMessagePending):
			<-sendingChannel
			continue
		default:
			return fmt.Errorf("send custom topology message: %w", err)
		}
	}
}
