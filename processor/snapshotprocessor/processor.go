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

package snapshotprocessor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/observiq/bindplane-otel-contrib/pkg/snapshot"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	snapshotCapability  = "com.bindplane.snapshot"
	snapshotRequestType = "requestSnapshot"
	snapshotReportType  = "reportSnapshot"

	// armIdleWindow is how long on-demand buffering stays armed after the
	// most recent snapshot request. It must comfortably exceed the snapshot
	// view's poll interval so buffering does not flap mid-session.
	armIdleWindow = 60 * time.Second
)

type snapshotProcessor struct {
	logger *zap.Logger

	processorID      component.ID
	enabled          bool
	opampExtensionID component.ID

	customCapabilityHandler opampcustommessages.CustomCapabilityHandler

	logBuffer    *snapshot.LogBuffer
	metricBuffer *snapshot.MetricBuffer
	traceBuffer  *snapshot.TraceBuffer

	// bufferSize is the per-signal buffer capacity in records/data points/spans.
	bufferSize int
	// refreshInterval bounds how often a batch is admitted to a full buffer.
	// Zero admits every batch.
	refreshInterval time.Duration

	// admit* re-arm once per refreshInterval (from the processOpAMPMessages
	// goroutine) and are consumed by the first batch to CAS them off. The
	// rejected path costs two atomic loads and never takes a lock.
	admitLogs    atomic.Bool
	admitMetrics atomic.Bool
	admitTraces  atomic.Bool

	// onDemand indicates buffer_mode: on_demand. When set, armed is raised by
	// snapshot requests and lowered after armIdleWindow without one; while
	// down, batches pass through with a single atomic load and the buffers
	// hold no telemetry. In always mode, armed is permanently true.
	onDemand      bool
	armed         atomic.Bool
	lastRequestNs atomic.Int64

	started  *atomic.Bool
	stopped  *atomic.Bool
	doneChan chan struct{}
	wg       *sync.WaitGroup
}

// newSnapshotProcessor creates a new snapshot processor
func newSnapshotProcessor(logger *zap.Logger, cfg *Config, processorID component.ID) *snapshotProcessor {
	sp := &snapshotProcessor{
		logger: logger,

		enabled:          cfg.Enabled,
		processorID:      processorID,
		opampExtensionID: cfg.OpAMP,

		bufferSize:      cfg.BufferSize,
		refreshInterval: cfg.RefreshInterval,

		onDemand: cfg.BufferMode == bufferModeOnDemand,

		started:  &atomic.Bool{},
		stopped:  &atomic.Bool{},
		doneChan: make(chan struct{}),
		wg:       &sync.WaitGroup{},
	}

	// In always mode, buffering is permanently armed. In on_demand mode it
	// stays down until the first snapshot request arrives.
	sp.armed.Store(!sp.onDemand)

	// Buffers exist only for the configured signal types; pipelines for other
	// signal types pass telemetry through with no buffering cost.
	if cfg.buffersSignal("logs") {
		sp.logBuffer = snapshot.NewLogBuffer(cfg.BufferSize)
	}
	if cfg.buffersSignal("metrics") {
		sp.metricBuffer = snapshot.NewMetricBuffer(cfg.BufferSize)
	}
	if cfg.buffersSignal("traces") {
		sp.traceBuffer = snapshot.NewTraceBuffer(cfg.BufferSize)
	}

	return sp
}

func (sp *snapshotProcessor) start(_ context.Context, host component.Host) error {
	if sp.started.Swap(true) {
		// Start logic should only be run once
		return nil
	}

	ext, ok := host.GetExtensions()[sp.opampExtensionID]
	if !ok {
		return fmt.Errorf("opamp extension %q does not exist", sp.opampExtensionID)
	}

	registry, ok := ext.(opampcustommessages.CustomCapabilityRegistry)
	if !ok {
		return fmt.Errorf("extension %q is not an custom message registry", sp.opampExtensionID)
	}

	var err error
	sp.customCapabilityHandler, err = registry.Register(snapshotCapability)
	if err != nil {
		return fmt.Errorf("register custom capability: %w", err)
	}

	sp.wg.Add(1)
	go sp.processOpAMPMessages(sp.customCapabilityHandler)

	return nil
}

func (sp *snapshotProcessor) processOpAMPMessages(o opampcustommessages.CustomCapabilityHandler) {
	defer sp.wg.Done()

	// The admit ticker re-arms buffer admission once per refresh interval
	// (see admitBatch). A nil channel (refresh_interval of 0) never fires.
	var admitC <-chan time.Time
	if sp.refreshInterval > 0 {
		admitTicker := time.NewTicker(sp.refreshInterval)
		defer admitTicker.Stop()
		admitC = admitTicker.C
	}

	// The disarm ticker lowers on-demand buffering after an idle period (see
	// maybeDisarm). A nil channel (buffer_mode: always) never fires.
	var disarmC <-chan time.Time
	if sp.onDemand {
		disarmTicker := time.NewTicker(armIdleWindow / 4)
		defer disarmTicker.Stop()
		disarmC = disarmTicker.C
	}

	for {
		select {
		case msg := <-o.Message():
			switch msg.Type {
			case snapshotRequestType:
				sp.logger.Debug("got snapshot request message")
				sp.processSnapshotRequest(msg)
			default:
				sp.logger.Warn("Received message of unknown type.", zap.String("messageType", msg.Type))
			}
			continue
		case <-admitC:
			sp.admitLogs.Store(true)
			sp.admitMetrics.Store(true)
			sp.admitTraces.Store(true)
		case <-disarmC:
			sp.maybeDisarm()
		case <-sp.doneChan:
			return
		}
	}
}

// admitBatch reports whether a batch should be admitted to a buffer. Batches
// are admitted freely until the buffer reaches capacity so low-rate pipelines
// fill it promptly; after that, one batch is admitted per refresh interval.
// The rejected path is lock-free: one atomic buffer-length load plus one
// atomic flag load.
func (sp *snapshotProcessor) admitBatch(admit *atomic.Bool, buffered int) bool {
	if sp.refreshInterval <= 0 {
		return true
	}

	if buffered < sp.bufferSize {
		return true
	}

	// Load before the CAS so the common rejected case is a plain read that
	// causes no cross-core cache-line traffic.
	return admit.Load() && admit.CompareAndSwap(true, false)
}

// maybeDisarm lowers on-demand buffering and drops the buffered telemetry
// when no snapshot request has arrived within armIdleWindow.
func (sp *snapshotProcessor) maybeDisarm() {
	if !sp.onDemand || !sp.armed.Load() {
		return
	}
	if time.Since(time.Unix(0, sp.lastRequestNs.Load())) <= armIdleWindow {
		return
	}

	sp.armed.Store(false)
	if sp.logBuffer != nil {
		sp.logBuffer.Reset()
	}
	if sp.metricBuffer != nil {
		sp.metricBuffer.Reset()
	}
	if sp.traceBuffer != nil {
		sp.traceBuffer.Reset()
	}
}

func (sp *snapshotProcessor) processSnapshotRequest(cm *protobufs.CustomMessage) {
	var req snapshotRequest
	err := yaml.Unmarshal(cm.Data, &req)
	if err != nil {
		sp.logger.Error("Got invalid snapshot request.", zap.Error(err))
		return
	}

	if req.Processor != sp.processorID {
		// // message is for a difference processor, skip.
		return
	}

	sp.logger.Debug("Processor ID on snapshot message matched", zap.Stringer("processor_id", req.Processor))

	// In on_demand mode, a snapshot request (re-)arms buffering. The first
	// request after an idle period returns an empty or partial snapshot; the
	// server's next poll returns a full one.
	if sp.onDemand {
		sp.lastRequestNs.Store(time.Now().UnixNano())
		sp.armed.Store(true)
	}

	// If not specified, default to 10MiB
	if req.MaximumPayloadSizeBytes <= 0 {
		req.MaximumPayloadSizeBytes = 10485760 //10MiB
	}

	var report snapshotReport
	switch req.PipelineType {
	case "logs":
		if sp.logBuffer == nil {
			sp.logger.Error("Snapshot requested for a signal type this processor does not buffer.", zap.String("PipelineType", req.PipelineType))
			return
		}
		telemetryPayload, err := sp.logBuffer.ConstructPayload(&plog.JSONMarshaler{}, req.SearchQuery, req.MinimumTimestamp, req.MaximumPayloadSizeBytes)
		if err != nil {
			sp.logger.Error("Failed to construct snapshot payload.", zap.Error(err))
			return
		}

		report = logsReport(req.SessionID, telemetryPayload)

	case "metrics":
		if sp.metricBuffer == nil {
			sp.logger.Error("Snapshot requested for a signal type this processor does not buffer.", zap.String("PipelineType", req.PipelineType))
			return
		}
		telemetryPayload, err := sp.metricBuffer.ConstructPayload(&pmetric.JSONMarshaler{}, req.SearchQuery, req.MinimumTimestamp, req.MaximumPayloadSizeBytes)
		if err != nil {
			sp.logger.Error("Failed to construct metrics snapshot payload.", zap.Error(err))
			return
		}

		report = metricsReport(req.SessionID, telemetryPayload)

	case "traces":
		if sp.traceBuffer == nil {
			sp.logger.Error("Snapshot requested for a signal type this processor does not buffer.", zap.String("PipelineType", req.PipelineType))
			return
		}
		telemetryPayload, err := sp.traceBuffer.ConstructPayload(&ptrace.JSONMarshaler{}, req.SearchQuery, req.MinimumTimestamp, req.MaximumPayloadSizeBytes)
		if err != nil {
			sp.logger.Error("Failed to construct traces payload.", zap.Error(err))
			return
		}

		report = tracesReport(req.SessionID, telemetryPayload)

	default:
		sp.logger.Error("Invalid pipeline type in snapshot request.", zap.String("PipelineType", req.PipelineType))
		return
	}

	sp.logger.Info("responding to report request", zap.String("session", req.SessionID))

	response, err := json.Marshal(report)
	if err != nil {
		sp.logger.Error("Could not marshal snapshot report.", zap.Error(err))
		return
	}

	compressedResponse, err := snapshot.Compress(response)
	if err != nil {
		sp.logger.Error("Failed to compress snapshot payload.", zap.Error(err))
		return
	}

	for {
		msgSendChan, err := sp.customCapabilityHandler.SendMessage(snapshotReportType, compressedResponse)
		switch {
		case err == nil: // Message is scheduled to send
			sp.logger.Debug("Message scheduled")
			return

		case errors.Is(err, types.ErrCustomMessagePending):
			// Wait until message is ready to send, then try again
			sp.logger.Debug("Custom message pending, will try sending again after message is clear.")
			<-msgSendChan

		default:
			sp.logger.Error("Failed to send snapshot payload message.", zap.Error(err))
			return
		}
	}
}

func (sp *snapshotProcessor) processTraces(_ context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	if sp.enabled && sp.armed.Load() && sp.traceBuffer != nil && sp.admitBatch(&sp.admitTraces, sp.traceBuffer.Len()) {
		// Add copies at most the buffer's ideal size out of td; the payload
		// itself is never retained or mutated.
		sp.traceBuffer.Add(td)
	}

	return td, nil
}

func (sp *snapshotProcessor) processLogs(_ context.Context, ld plog.Logs) (plog.Logs, error) {
	if sp.enabled && sp.armed.Load() && sp.logBuffer != nil && sp.admitBatch(&sp.admitLogs, sp.logBuffer.Len()) {
		// Add copies at most the buffer's ideal size out of ld; the payload
		// itself is never retained or mutated.
		sp.logBuffer.Add(ld)
	}

	return ld, nil
}

func (sp *snapshotProcessor) processMetrics(_ context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	if sp.enabled && sp.armed.Load() && sp.metricBuffer != nil && sp.admitBatch(&sp.admitMetrics, sp.metricBuffer.Len()) {
		// Add copies at most the buffer's ideal size out of md; the payload
		// itself is never retained or mutated.
		sp.metricBuffer.Add(md)
	}

	return md, nil
}

func (sp *snapshotProcessor) stop(ctx context.Context) error {
	if sp.stopped.Swap(true) {
		// Stop logic should only be run once
		return nil
	}

	unregisterProcessor(sp.processorID)

	if sp.customCapabilityHandler != nil {
		sp.customCapabilityHandler.Unregister()
	}

	if sp.doneChan != nil {
		close(sp.doneChan)
	}

	waitgroupDone := make(chan struct{})
	go func() {
		sp.wg.Wait()
		close(waitgroupDone)
	}()

	select {
	case <-waitgroupDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}
