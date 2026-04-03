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

package bytebatcherprocessor

import (
	"context"
	"sync"
	"time"

	"github.com/observiq/bindplane-otel-contrib/processor/bytebatcherprocessor/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processorhelper"
	"go.uber.org/zap"
)

// batch represents a single telemetry batch of type T.
type batch[T any] interface {
	add(item T)
	sizeBytes(item T) int
	flush(ctx context.Context) error
	len() int
	reset()
}

// queue manages batching and flushing of telemetry items.
type queue[T any] struct {
	mu          sync.Mutex
	items       []T
	currentSize int64

	cfg       *Config
	logger    *zap.Logger
	newBatch  func() batch[T]
	telemetry *metadata.TelemetryBuilder

	ctx      context.Context
	cancel   context.CancelFunc
	doneChan chan struct{}
	wg       sync.WaitGroup
}

// newQueue creates a new queue for batching telemetry.
func newQueue[T any](cfg *Config, logger *zap.Logger, telemetry *metadata.TelemetryBuilder, newBatchFunc func() batch[T]) *queue[T] {
	ctx, cancel := context.WithCancel(context.Background())
	return &queue[T]{
		cfg:       cfg,
		logger:    logger,
		newBatch:  newBatchFunc,
		telemetry: telemetry,
		ctx:       ctx,
		cancel:    cancel,
		doneChan:  make(chan struct{}),
	}
}

// start begins the flush goroutine.
func (q *queue[T]) start() {
	q.wg.Add(1)
	go q.flushLoop()
}

// shutdown gracefully stops the queue and flushes remaining items.
func (q *queue[T]) shutdown(ctx context.Context) error {
	close(q.doneChan)

	waitCh := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(waitCh)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-waitCh:
		return nil
	}
}

// add appends an item to the queue. If the accumulated size exceeds the threshold,
// items are flushed immediately without blocking the caller.
func (q *queue[T]) add(item T, b batch[T]) {
	size := int64(b.sizeBytes(item))

	q.mu.Lock()
	q.items = append(q.items, item)
	q.currentSize += size

	// Check if we've hit the size threshold
	if q.currentSize >= int64(q.cfg.Bytes) {
		items := q.items
		q.items = nil
		q.currentSize = 0
		q.mu.Unlock()

		q.doFlush(items, b)
		return
	}
	q.mu.Unlock()
}

// doFlush sends accumulated items to the next consumer.
func (q *queue[T]) doFlush(items []T, b batch[T]) {
	if len(items) == 0 {
		return
	}

	// Restore items to batch for flushing
	for _, item := range items {
		b.add(item)
	}

	if err := b.flush(q.ctx); err != nil {
		q.logger.Error("failed to flush batch", zap.Error(err))
	}

	b.reset()
}

// flushLoop runs the background flush goroutine.
func (q *queue[T]) flushLoop() {
	defer q.wg.Done()

	ticker := time.NewTicker(q.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			q.flushOnInterval()
		case <-q.doneChan:
			q.flushOnInterval() // Final flush on shutdown
			return
		}
	}
}

// flushOnInterval flushes items on the interval trigger.
func (q *queue[T]) flushOnInterval() {
	q.mu.Lock()
	if len(q.items) == 0 {
		q.mu.Unlock()
		return
	}

	items := q.items
	q.items = nil
	q.currentSize = 0
	q.mu.Unlock()

	// Use a temporary batch for flushing
	b := q.newBatch()
	q.doFlush(items, b)
}

// tracesProcessor processes incoming trace data.
type tracesProcessor struct {
	q *queue[ptrace.Traces]
}

func newTracesProcessor(cfg *Config, logger *zap.Logger, telemetry *metadata.TelemetryBuilder, newBatchFunc func() batch[ptrace.Traces]) *tracesProcessor {
	return &tracesProcessor{
		q: newQueue(cfg, logger, telemetry, newBatchFunc),
	}
}

func (p *tracesProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

func (p *tracesProcessor) Start(context.Context, component.Host) error {
	p.q.start()
	return nil
}

func (p *tracesProcessor) Shutdown(ctx context.Context) error {
	return p.q.shutdown(ctx)
}

func (p *tracesProcessor) processTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	b := p.q.newBatch()
	p.q.add(td, b)
	return td, processorhelper.ErrSkipProcessingData
}

// logsProcessor processes incoming log data.
type logsProcessor struct {
	q *queue[plog.Logs]
}

func newLogsProcessor(cfg *Config, logger *zap.Logger, telemetry *metadata.TelemetryBuilder, newBatchFunc func() batch[plog.Logs]) *logsProcessor {
	return &logsProcessor{
		q: newQueue(cfg, logger, telemetry, newBatchFunc),
	}
}

func (p *logsProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

func (p *logsProcessor) Start(context.Context, component.Host) error {
	p.q.start()
	return nil
}

func (p *logsProcessor) Shutdown(ctx context.Context) error {
	return p.q.shutdown(ctx)
}

func (p *logsProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	b := p.q.newBatch()
	p.q.add(ld, b)
	return ld, processorhelper.ErrSkipProcessingData
}

// metricsProcessor processes incoming metric data.
type metricsProcessor struct {
	q *queue[pmetric.Metrics]
}

func newMetricsProcessor(cfg *Config, logger *zap.Logger, telemetry *metadata.TelemetryBuilder, newBatchFunc func() batch[pmetric.Metrics]) *metricsProcessor {
	return &metricsProcessor{
		q: newQueue(cfg, logger, telemetry, newBatchFunc),
	}
}

func (p *metricsProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

func (p *metricsProcessor) Start(context.Context, component.Host) error {
	p.q.start()
	return nil
}

func (p *metricsProcessor) Shutdown(ctx context.Context) error {
	return p.q.shutdown(ctx)
}

func (p *metricsProcessor) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	b := p.q.newBatch()
	p.q.add(md, b)
	return md, processorhelper.ErrSkipProcessingData
}
