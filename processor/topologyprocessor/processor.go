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
	"fmt"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

const (
	organizationIDHeader = "X-Bindplane-Organization-ID"
	accountIDHeader      = "X-Bindplane-Account-ID"
	configurationHeader  = "X-Bindplane-Configuration"
	resourceNameHeader   = "X-Bindplane-Resource-Name"
)

type topologyProcessor struct {
	logger           *zap.Logger
	topology         *TopoState
	processorID      component.ID
	opampExtensionID component.ID
	global           *GlobalConfig
	// bindplaneExtensionID exists only for backwards compatibility with Bindplane
	// servers that don't render `opamp`; delete with BPOP-5623.
	bindplaneExtensionID *component.ID

	// registeredWithReporter records that start registered with the shared
	// opamp reporter, so shutdown knows to release it.
	registeredWithReporter bool

	started *atomic.Bool
	stopped *atomic.Bool
}

// newTopologyProcessor creates a new topology processor
func newTopologyProcessor(logger *zap.Logger, cfg *Config, processorID component.ID) (*topologyProcessor, error) {
	destGw := GatewayInfo{
		GatewayID:      strings.TrimPrefix(processorID.String(), "topology/"),
		Configuration:  cfg.Configuration,
		AccountID:      cfg.AccountID,
		OrganizationID: cfg.OrganizationID,
	}
	topology, err := NewTopologyState(destGw)
	if err != nil {
		return nil, fmt.Errorf("create topology state: %w", err)
	}

	return &topologyProcessor{
		logger:               logger,
		topology:             topology,
		processorID:          processorID,
		opampExtensionID:     cfg.OpAMP,
		global:               cfg.Global,
		bindplaneExtensionID: cfg.BindplaneExtension,

		started: &atomic.Bool{},
		stopped: &atomic.Bool{},
	}, nil
}

func (tp *topologyProcessor) start(_ context.Context, host component.Host) error {
	if tp.started.Swap(true) {
		// Start logic should only be run once
		return nil
	}

	var emptyID component.ID
	if tp.global != nil && tp.opampExtensionID == emptyID {
		tp.logger.Warn("global is set but opamp is not; ignoring global settings.")
	}

	switch {
	case tp.opampExtensionID != emptyID:
		if tp.bindplaneExtensionID != nil {
			tp.logger.Warn("Both opamp and bindplane_extension are set; using opamp. bindplane_extension is deprecated.")
		}

		registerWithOpAMPReporter(tp)
		tp.registeredWithReporter = true

		// Only the processor carrying the `global` block sets up the reporter;
		// if no processor carries one, topology state feeds the reporter but
		// nothing is reported.
		if tp.global != nil {
			return configureOpAMPReporter(host, tp.logger, tp.opampExtensionID, *tp.global)
		}

		// The extension reference must still resolve, even on processors that
		// don't set up the reporter.
		_, err := getCustomCapabilityRegistry(host, tp.opampExtensionID)
		return err

	// Both fallback cases below exist only for backwards compatibility with
	// Bindplane servers that don't render `opamp`; delete them (and make opamp
	// reporting the only path) with BPOP-5623.
	case tp.bindplaneExtensionID != nil:
		tp.logger.Warn("bindplane_extension is deprecated; configure opamp instead.")
		ext, ok := host.GetExtensions()[*tp.bindplaneExtensionID]
		if !ok {
			// Old Bindplane servers render bindplane_extension without instantiating
			// the extension (v1 agents ignored the field entirely); treat this the
			// same as the neither-set case below.
			tp.registerWithAgentRegistry()
			return nil
		}

		registry, ok := ext.(TopoRegistry)
		if !ok {
			return fmt.Errorf("extension %q is not an topology state registry", tp.bindplaneExtensionID)
		}

		if err := registry.RegisterTopologyState(tp.processorID.String(), tp.topology); err != nil {
			return fmt.Errorf("register topology state: %w", err)
		}

	default:
		// Neither opamp nor bindplane_extension is configured, meaning this is a
		// v1 bindplane agent (or a standalone collector).
		tp.registerWithAgentRegistry()
	}

	return nil
}

// registerWithAgentRegistry registers the topology state with the package-level
// registry that the v1 bindplane agent runtime reads. Never fatal: duplicate
// registration (e.g. a config reload without a registry reset) only warns, and
// outside a v1 agent the registration is simply inert.
func (tp *topologyProcessor) registerWithAgentRegistry() {
	if err := BindplaneAgentTopologyRegistry.RegisterTopologyState(tp.processorID.String(), tp.topology); err != nil {
		tp.logger.Warn("Failed to register topology state with bindplane agent registry.", zap.Error(err))
	}
}

func (tp *topologyProcessor) processTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	tp.processTopologyHeaders(ctx)
	return td, nil
}

func (tp *topologyProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	tp.processTopologyHeaders(ctx)
	return ld, nil
}

func (tp *topologyProcessor) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	tp.processTopologyHeaders(ctx)
	return md, nil
}

func (tp *topologyProcessor) processTopologyHeaders(ctx context.Context) {
	headers := client.FromContext(ctx).Metadata
	var configuration, accountID, organizationID, resourceName string

	configurationHeaders := headers.Get(configurationHeader)
	if len(configurationHeaders) > 0 {
		configuration = configurationHeaders[0]
	} else {
		return
	}

	accountIDHeaders := headers.Get(accountIDHeader)
	if len(accountIDHeaders) > 0 {
		accountID = accountIDHeaders[0]
	} else {
		return
	}

	organizationIDHeaders := headers.Get(organizationIDHeader)
	if len(organizationIDHeaders) > 0 {
		organizationID = organizationIDHeaders[0]
	} else {
		return
	}

	resourceNameHeaders := headers.Get(resourceNameHeader)
	if len(resourceNameHeaders) > 0 {
		resourceName = resourceNameHeaders[0]
	} else {
		return
	}

	// only upsert if all headers are present
	if configuration != "" && accountID != "" && organizationID != "" && resourceName != "" {
		gw := GatewayInfo{
			Configuration:  configuration,
			AccountID:      accountID,
			OrganizationID: organizationID,
			GatewayID:      resourceName,
		}
		tp.topology.UpsertRoute(ctx, gw)
	}
}

func (tp *topologyProcessor) shutdown(ctx context.Context) error {
	if tp.stopped.Swap(true) {
		// Stop logic should only be run once
		return nil
	}

	unregisterProcessor(tp.processorID)

	if tp.registeredWithReporter {
		return releaseOpAMPReporter(ctx)
	}

	return nil
}
