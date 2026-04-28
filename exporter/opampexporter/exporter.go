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

package opampexporter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/golang/snappy"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

type opampExporter struct {
	logger *zap.Logger

	exporterID        component.ID
	opampExtensionID  component.ID
	capability        string
	messageType       string
	maxQueuedMessages int

	mu                      sync.Mutex
	customCapabilityHandler opampcustommessages.CustomCapabilityHandler

	logsMarshaler    plog.ProtoMarshaler
	metricsMarshaler pmetric.ProtoMarshaler
	tracesMarshaler  ptrace.ProtoMarshaler

	started *atomic.Bool
	stopped *atomic.Bool
	done    chan struct{}
	wg      sync.WaitGroup
}

// errShuttingDown is returned by sendMessage when shutdown is observed
// while the send was waiting for a pending-send slot.
var errShuttingDown = errors.New("opamp exporter is shutting down")

func newOpAMPExporter(logger *zap.Logger, cfg *Config, exporterID component.ID) *opampExporter {
	return &opampExporter{
		logger:            logger,
		exporterID:        exporterID,
		opampExtensionID:  cfg.OpAMP,
		capability:        cfg.CustomMessage.Capability,
		messageType:       cfg.CustomMessage.Type,
		maxQueuedMessages: cfg.MaxQueuedMessages,
		started:           &atomic.Bool{},
		stopped:           &atomic.Bool{},
		done:              make(chan struct{}),
	}
}

func (e *opampExporter) start(_ context.Context, host component.Host) error {
	if e.started.Swap(true) {
		// start logic should only be run once per shared instance.
		return nil
	}

	ext, ok := host.GetExtensions()[e.opampExtensionID]
	if !ok {
		return fmt.Errorf("opamp extension %q does not exist", e.opampExtensionID)
	}

	registry, ok := ext.(opampcustommessages.CustomCapabilityRegistry)
	if !ok {
		return fmt.Errorf("extension %q is not a custom message registry", e.opampExtensionID)
	}

	handler, err := registry.Register(e.capability,
		opampcustommessages.WithMaxQueuedMessages(e.maxQueuedMessages))
	if err != nil {
		return fmt.Errorf("register custom capability: %w", err)
	}

	e.mu.Lock()
	e.customCapabilityHandler = handler
	e.mu.Unlock()

	return nil
}

func (e *opampExporter) shutdown(ctx context.Context) error {
	if e.stopped.Swap(true) {
		// shutdown logic should only be run once per shared instance.
		return nil
	}

	unregisterExporter(e.exporterID)

	// Signal in-flight sends to stop waiting for pending-send slots, then
	// wait for them to drain (bounded by the shutdown context's deadline)
	// before unregistering the handler so unregister doesn't race with an
	// in-progress SendMessage.
	close(e.done)

	drained := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(drained)
	}()

	var shutdownErr error
	select {
	case <-drained:
	case <-ctx.Done():
		shutdownErr = ctx.Err()
	}

	e.mu.Lock()
	handler := e.customCapabilityHandler
	e.customCapabilityHandler = nil
	e.mu.Unlock()

	if handler != nil {
		handler.Unregister()
	}

	return shutdownErr
}

func (e *opampExporter) consumeLogs(_ context.Context, ld plog.Logs) error {
	payload, err := e.logsMarshaler.MarshalLogs(ld)
	if err != nil {
		return fmt.Errorf("marshal logs: %w", err)
	}
	return e.sendMessage(snappy.Encode(nil, payload))
}

func (e *opampExporter) consumeMetrics(_ context.Context, md pmetric.Metrics) error {
	payload, err := e.metricsMarshaler.MarshalMetrics(md)
	if err != nil {
		return fmt.Errorf("marshal metrics: %w", err)
	}
	return e.sendMessage(snappy.Encode(nil, payload))
}

func (e *opampExporter) consumeTraces(_ context.Context, td ptrace.Traces) error {
	payload, err := e.tracesMarshaler.MarshalTraces(td)
	if err != nil {
		return fmt.Errorf("marshal traces: %w", err)
	}
	return e.sendMessage(snappy.Encode(nil, payload))
}

func (e *opampExporter) sendMessage(payload []byte) error {
	// Reject new sends once shutdown has been signaled so we don't add to
	// the wait group while shutdown is draining it.
	select {
	case <-e.done:
		return errShuttingDown
	default:
	}

	e.wg.Add(1)
	defer e.wg.Done()

	e.mu.Lock()
	handler := e.customCapabilityHandler
	e.mu.Unlock()

	if handler == nil {
		return errors.New("opamp custom capability handler is not registered")
	}

	for {
		msgSendChan, err := handler.SendMessage(e.messageType, payload)
		switch {
		case err == nil:
			e.logger.Debug("OTLP message scheduled to send via OpAMP")
			return nil
		case errors.Is(err, types.ErrCustomMessagePending):
			e.logger.Debug("Custom message pending, waiting for send slot.")
			select {
			case <-msgSendChan:
			case <-e.done:
				return errShuttingDown
			}
		default:
			return fmt.Errorf("send opamp custom message: %w", err)
		}
	}
}
