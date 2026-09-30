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

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/observiq/bindplane-otel-contrib/pkg/measurements"
)

// logsConsumer wraps the next consumer in the pipeline.
// It measures a sampled payload before it forwards it, then records the
// measurement as delivered when the next consumer returns nil and as rejected
// when it returns an error. The measurement is taken before the forward
// because downstream components can move data out of the payload.
type logsConsumer struct {
	tmp  *throughputMeasurementProcessor
	next consumer.Logs
}

func newLogsConsumer(tmp *throughputMeasurementProcessor, next consumer.Logs) consumer.Logs {
	return &logsConsumer{tmp: tmp, next: next}
}

// Capabilities reports the capabilities of the wrapped consumer.
func (c *logsConsumer) Capabilities() consumer.Capabilities {
	return c.next.Capabilities()
}

// ConsumeLogs forwards the payload and records the outcome.
func (c *logsConsumer) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	if !c.tmp.sample() {
		return c.next.ConsumeLogs(ctx, ld)
	}

	m := measurements.MeasureLogs(ld, c.tmp.measureLogRawBytes)

	if err := c.next.ConsumeLogs(ctx, ld); err != nil {
		c.tmp.measurements.RecordRejectedLogs(ctx, m)
		return err
	}

	c.tmp.measurements.RecordLogs(ctx, m)
	return nil
}

// metricsConsumer is the metrics counterpart of logsConsumer.
type metricsConsumer struct {
	tmp  *throughputMeasurementProcessor
	next consumer.Metrics
}

func newMetricsConsumer(tmp *throughputMeasurementProcessor, next consumer.Metrics) consumer.Metrics {
	return &metricsConsumer{tmp: tmp, next: next}
}

// Capabilities reports the capabilities of the wrapped consumer.
func (c *metricsConsumer) Capabilities() consumer.Capabilities {
	return c.next.Capabilities()
}

// ConsumeMetrics forwards the payload and records the outcome.
func (c *metricsConsumer) ConsumeMetrics(ctx context.Context, md pmetric.Metrics) error {
	if !c.tmp.sample() {
		return c.next.ConsumeMetrics(ctx, md)
	}

	m := measurements.MeasureMetrics(md)

	if err := c.next.ConsumeMetrics(ctx, md); err != nil {
		c.tmp.measurements.RecordRejectedMetrics(ctx, m)
		return err
	}

	c.tmp.measurements.RecordMetrics(ctx, m)
	return nil
}

// tracesConsumer is the traces counterpart of logsConsumer.
type tracesConsumer struct {
	tmp  *throughputMeasurementProcessor
	next consumer.Traces
}

func newTracesConsumer(tmp *throughputMeasurementProcessor, next consumer.Traces) consumer.Traces {
	return &tracesConsumer{tmp: tmp, next: next}
}

// Capabilities reports the capabilities of the wrapped consumer.
func (c *tracesConsumer) Capabilities() consumer.Capabilities {
	return c.next.Capabilities()
}

// ConsumeTraces forwards the payload and records the outcome.
func (c *tracesConsumer) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	if !c.tmp.sample() {
		return c.next.ConsumeTraces(ctx, td)
	}

	m := measurements.MeasureTraces(td)

	if err := c.next.ConsumeTraces(ctx, td); err != nil {
		c.tmp.measurements.RecordRejectedTraces(ctx, m)
		return err
	}

	c.tmp.measurements.RecordTraces(ctx, m)
	return nil
}
