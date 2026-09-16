// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package blobconsume //import "github.com/observiq/bindplane-otel-contrib/internal/blobconsume"

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// ErrDownstream marks an error as coming from the next consumer in the pipeline (a full
// sending queue, memory limiter, exporter backpressure) rather than from parsing the blob
// content, so a caller can tell a transient downstream failure apart from a permanent
// content error and retry instead of quarantining the blob. Every consumer's ConsumeCounted
// wraps its next-consumer error with it.
var ErrDownstream = errors.New("downstream consumer failed")

// Consumer is responsible for turning entities into OTLP data and sending to the next consumer.
//
//go:generate mockery --name Consumer --inpackage --with-expecter --filename mock_consumer.go --structname MockConsumer
type Consumer interface {
	// Consume turns entity contents into OTLP data and forwards it to the next consumer,
	// reporting only whether the operation failed. Callers that need the record counts use
	// ConsumeCounted; this is the simple form the rehydration receivers call.
	Consume(ctx context.Context, entityContent []byte) error
	// ConsumeCounted is Consume that also reports how many records it parsed (consumed) and how
	// many it forwarded downstream (emitted); consumed minus emitted is the records dropped as
	// malformed or not sent because of a downstream error.
	ConsumeCounted(ctx context.Context, entityContent []byte) (consumed int, emitted int, err error)
}

// MetricsConsumer consumes rehydrated metric entities and marshals them into pdata structures
type MetricsConsumer struct {
	nextConsumer consumer.Metrics

	unmarshaler *pmetric.JSONUnmarshaler
}

// NewMetricsConsumer creates a new metrics consumer
func NewMetricsConsumer(nextConsumer consumer.Metrics) *MetricsConsumer {
	return &MetricsConsumer{
		nextConsumer: nextConsumer,
		unmarshaler:  &pmetric.JSONUnmarshaler{},
	}
}

// Consume implements Consumer; it discards ConsumeCounted's record counts.
func (m *MetricsConsumer) Consume(ctx context.Context, entityContent []byte) error {
	_, _, err := m.ConsumeCounted(ctx, entityContent)
	return err
}

// ConsumeCounted unmarshals entityContent into pmetrics and consumes it, counting data points.
func (m *MetricsConsumer) ConsumeCounted(ctx context.Context, entityContent []byte) (int, int, error) {
	payload, err := m.unmarshaler.UnmarshalMetrics(entityContent)
	if err != nil {
		return 0, 0, fmt.Errorf("metrics consume: %w", err)
	}
	n := payload.DataPointCount()
	if err := m.nextConsumer.ConsumeMetrics(ctx, payload); err != nil {
		return n, 0, fmt.Errorf("metrics consume: %w: %w", ErrDownstream, err)
	}
	return n, n, nil
}

// LogsConsumer consumes rehydrated log entities and marshals them into pdata structures
type LogsConsumer struct {
	nextConsumer consumer.Logs

	unmarshaler *plog.JSONUnmarshaler
}

// NewLogsConsumer creates a new logs consumer
func NewLogsConsumer(nextConsumer consumer.Logs) *LogsConsumer {
	return &LogsConsumer{
		nextConsumer: nextConsumer,
		unmarshaler:  &plog.JSONUnmarshaler{},
	}
}

// Consume implements Consumer; it discards ConsumeCounted's record counts.
func (l *LogsConsumer) Consume(ctx context.Context, entityContent []byte) error {
	_, _, err := l.ConsumeCounted(ctx, entityContent)
	return err
}

// ConsumeCounted unmarshals entityContent into plogs and consumes it, counting log records.
func (l *LogsConsumer) ConsumeCounted(ctx context.Context, entityContent []byte) (int, int, error) {
	payload, err := l.unmarshaler.UnmarshalLogs(entityContent)
	if err != nil {
		return 0, 0, fmt.Errorf("logs consume: %w", err)
	}
	n := payload.LogRecordCount()
	if err := l.nextConsumer.ConsumeLogs(ctx, payload); err != nil {
		return n, 0, fmt.Errorf("logs consume: %w: %w", ErrDownstream, err)
	}
	return n, n, nil
}

// TracesConsumer consumes rehydrated trace entities and marshals them into pdata structures
type TracesConsumer struct {
	nextConsumer consumer.Traces

	unmarshaler *ptrace.JSONUnmarshaler
}

// NewTracesConsumer creates a new trace consumer
func NewTracesConsumer(nextConsumer consumer.Traces) *TracesConsumer {
	return &TracesConsumer{
		nextConsumer: nextConsumer,
		unmarshaler:  &ptrace.JSONUnmarshaler{},
	}
}

// Consume implements Consumer; it discards ConsumeCounted's record counts.
func (l *TracesConsumer) Consume(ctx context.Context, entityContent []byte) error {
	_, _, err := l.ConsumeCounted(ctx, entityContent)
	return err
}

// ConsumeCounted unmarshals entityContent into ptrace and consumes it, counting spans.
func (l *TracesConsumer) ConsumeCounted(ctx context.Context, entityContent []byte) (int, int, error) {
	payload, err := l.unmarshaler.UnmarshalTraces(entityContent)
	if err != nil {
		return 0, 0, fmt.Errorf("traces consume: %w", err)
	}
	n := payload.SpanCount()
	if err := l.nextConsumer.ConsumeTraces(ctx, payload); err != nil {
		return n, 0, fmt.Errorf("traces consume: %w: %w", ErrDownstream, err)
	}
	return n, n, nil
}
