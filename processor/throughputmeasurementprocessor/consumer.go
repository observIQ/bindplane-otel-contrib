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
//
// When count_on_delivery is false, it records a sampled payload when the
// payload arrives, then forwards it.
//
// When count_on_delivery is true, it measures a sampled payload before it
// forwards it, then records the measurement as delivered or rejected. The
// payload is delivered when the next consumer returns nil, or when a throughput
// processor further down marks the delivery tracker (see deliveryTracker). The
// measurement is taken before the forward because downstream components can
// move data out of the payload.
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
	if !c.tmp.countOnDelivery {
		if c.tmp.sample() {
			c.tmp.measurements.AddLogs(ctx, ld, c.tmp.measureLogRawBytes)
		}
		return c.next.ConsumeLogs(ctx, ld)
	}

	// A disabled processor passes the parent tracker through unchanged.
	if !c.tmp.enabled {
		return c.next.ConsumeLogs(ctx, ld)
	}

	sampled := c.tmp.sample()
	var m measurements.Measurement
	if sampled {
		m = measurements.MeasureLogs(ld, c.tmp.measureLogRawBytes)
	}

	own := &deliveryTracker{}
	err := c.next.ConsumeLogs(contextWithTracker(ctx, own), ld)
	delivered := settle(ctx, own, err)

	if sampled {
		if delivered {
			c.tmp.measurements.RecordLogs(ctx, m)
		} else {
			c.tmp.measurements.RecordRejectedLogs(ctx, m)
		}
	}
	return err
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
	if !c.tmp.countOnDelivery {
		if c.tmp.sample() {
			c.tmp.measurements.AddMetrics(ctx, md)
		}
		return c.next.ConsumeMetrics(ctx, md)
	}

	// A disabled processor passes the parent tracker through unchanged.
	if !c.tmp.enabled {
		return c.next.ConsumeMetrics(ctx, md)
	}

	sampled := c.tmp.sample()
	var m measurements.Measurement
	if sampled {
		m = measurements.MeasureMetrics(md)
	}

	own := &deliveryTracker{}
	err := c.next.ConsumeMetrics(contextWithTracker(ctx, own), md)
	delivered := settle(ctx, own, err)

	if sampled {
		if delivered {
			c.tmp.measurements.RecordMetrics(ctx, m)
		} else {
			c.tmp.measurements.RecordRejectedMetrics(ctx, m)
		}
	}
	return err
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
	if !c.tmp.countOnDelivery {
		if c.tmp.sample() {
			c.tmp.measurements.AddTraces(ctx, td)
		}
		return c.next.ConsumeTraces(ctx, td)
	}

	// A disabled processor passes the parent tracker through unchanged.
	if !c.tmp.enabled {
		return c.next.ConsumeTraces(ctx, td)
	}

	sampled := c.tmp.sample()
	var m measurements.Measurement
	if sampled {
		m = measurements.MeasureTraces(td)
	}

	own := &deliveryTracker{}
	err := c.next.ConsumeTraces(contextWithTracker(ctx, own), td)
	delivered := settle(ctx, own, err)

	if sampled {
		if delivered {
			c.tmp.measurements.RecordTraces(ctx, m)
		} else {
			c.tmp.measurements.RecordRejectedTraces(ctx, m)
		}
	}
	return err
}

// settle reports whether the forward delivered the payload. The payload is
// delivered when the forward returned nil or when a branch below marked own.
// On delivery, settle marks the parent tracker in ctx, so that the processor
// above also sees the delivery. It marks the parent also for payloads that
// were not sampled.
func settle(ctx context.Context, own *deliveryTracker, err error) bool {
	if err != nil && !own.wasDelivered() {
		return false
	}
	trackerFromContext(ctx).markDelivered()
	return true
}
