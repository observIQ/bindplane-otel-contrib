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

// Package measurements provides code to help manage throughput measurements for Bindplane and
// the throughput measurement processor.
package measurements

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ThroughputMeasurementsRegistry represents a registry for the throughputmeasurement processor to
// register their ThroughputMeasurements.
type ThroughputMeasurementsRegistry interface {
	// RegisterThroughputMeasurements registers the measurements for the given processor.
	// It should return an error if the processor has already been registered.
	RegisterThroughputMeasurements(processorID string, measurements *ThroughputMeasurements) error
}

// ThroughputMeasurements represents all captured throughput metrics.
// It allows for incrementing and querying the current values of throughput metrics.
// Delivered counters hold payloads the next consumer accepted.
// Rejected counters hold payloads the next consumer refused.
type ThroughputMeasurements struct {
	logSize, metricSize, traceSize      *int64Counter
	logCount, datapointCount, spanCount *int64Counter
	logRawBytes                         *int64Counter

	logSizeRejected, metricSizeRejected, traceSizeRejected      *int64Counter
	logCountRejected, datapointCountRejected, spanCountRejected *int64Counter
	logRawBytesRejected                                         *int64Counter

	attributes               attribute.Set
	collectionSequenceNumber atomic.Int64
}

// NewThroughputMeasurements initializes a new ThroughputMeasurements, adding metrics for the measurements to the meter provider.
func NewThroughputMeasurements(mp metric.MeterProvider, processorID string, extraAttributes map[string]string) (*ThroughputMeasurements, error) {
	meter := mp.Meter("github.com/observiq/bindplane-otel-contrib/pkg/measurements")
	attrs := createMeasurementsAttributeSet(processorID, extraAttributes)

	tm := &ThroughputMeasurements{attributes: attrs}

	counters := []struct {
		dst  **int64Counter
		name string
		desc string
		unit string
	}{
		{&tm.logSize, "log_data_size", "Size of the log payloads accepted downstream of the processor", "By"},
		{&tm.metricSize, "metric_data_size", "Size of the metric payloads accepted downstream of the processor", "By"},
		{&tm.traceSize, "trace_data_size", "Size of the trace payloads accepted downstream of the processor", "By"},
		{&tm.logCount, "log_count", "Count of the log records accepted downstream of the processor", "{logs}"},
		{&tm.datapointCount, "metric_count", "Count of the datapoints accepted downstream of the processor", "{datapoints}"},
		{&tm.spanCount, "trace_count", "Count of the spans accepted downstream of the processor", "{spans}"},
		{&tm.logRawBytes, "log_raw_bytes", "Size of the original log content accepted downstream of the processor", "By"},

		{&tm.logSizeRejected, "log_data_size_rejected", "Size of the log payloads refused downstream of the processor", "By"},
		{&tm.metricSizeRejected, "metric_data_size_rejected", "Size of the metric payloads refused downstream of the processor", "By"},
		{&tm.traceSizeRejected, "trace_data_size_rejected", "Size of the trace payloads refused downstream of the processor", "By"},
		{&tm.logCountRejected, "log_count_rejected", "Count of the log records refused downstream of the processor", "{logs}"},
		{&tm.datapointCountRejected, "metric_count_rejected", "Count of the datapoints refused downstream of the processor", "{datapoints}"},
		{&tm.spanCountRejected, "trace_count_rejected", "Count of the spans refused downstream of the processor", "{spans}"},
		{&tm.logRawBytesRejected, "log_raw_bytes_rejected", "Size of the original log content refused downstream of the processor", "By"},
	}

	for _, c := range counters {
		counter, err := meter.Int64Counter(
			metricName(c.name),
			metric.WithDescription(c.desc),
			metric.WithUnit(c.unit),
		)
		if err != nil {
			return nil, fmt.Errorf("create %s counter: %w", c.name, err)
		}
		*c.dst = newInt64Counter(counter, attrs)
	}

	return tm, nil
}

// AddLogs measures the logs payload and records it as delivered.
func (tm *ThroughputMeasurements) AddLogs(ctx context.Context, l plog.Logs, measureLogRawBytes bool) {
	tm.RecordLogs(ctx, MeasureLogs(l, measureLogRawBytes))
}

// AddMetrics measures the metrics payload and records it as delivered.
func (tm *ThroughputMeasurements) AddMetrics(ctx context.Context, m pmetric.Metrics) {
	tm.RecordMetrics(ctx, MeasureMetrics(m))
}

// AddTraces measures the traces payload and records it as delivered.
func (tm *ThroughputMeasurements) AddTraces(ctx context.Context, t ptrace.Traces) {
	tm.RecordTraces(ctx, MeasureTraces(t))
}

// RecordLogs records a logs measurement as delivered and advances the sequence number.
func (tm *ThroughputMeasurements) RecordLogs(ctx context.Context, m Measurement) {
	tm.collectionSequenceNumber.Add(1)
	if m.HasRawBytes {
		tm.logRawBytes.Add(ctx, m.RawBytes)
	}
	tm.logSize.Add(ctx, m.Size)
	tm.logCount.Add(ctx, m.Count)
}

// RecordRejectedLogs records a logs measurement as rejected. It does not advance the sequence number.
func (tm *ThroughputMeasurements) RecordRejectedLogs(ctx context.Context, m Measurement) {
	if m.HasRawBytes {
		tm.logRawBytesRejected.Add(ctx, m.RawBytes)
	}
	tm.logSizeRejected.Add(ctx, m.Size)
	tm.logCountRejected.Add(ctx, m.Count)
}

// RecordMetrics records a metrics measurement as delivered and advances the sequence number.
func (tm *ThroughputMeasurements) RecordMetrics(ctx context.Context, m Measurement) {
	tm.collectionSequenceNumber.Add(1)
	tm.metricSize.Add(ctx, m.Size)
	tm.datapointCount.Add(ctx, m.Count)
}

// RecordRejectedMetrics records a metrics measurement as rejected. It does not advance the sequence number.
func (tm *ThroughputMeasurements) RecordRejectedMetrics(ctx context.Context, m Measurement) {
	tm.metricSizeRejected.Add(ctx, m.Size)
	tm.datapointCountRejected.Add(ctx, m.Count)
}

// RecordTraces records a traces measurement as delivered and advances the sequence number.
func (tm *ThroughputMeasurements) RecordTraces(ctx context.Context, m Measurement) {
	tm.collectionSequenceNumber.Add(1)
	tm.traceSize.Add(ctx, m.Size)
	tm.spanCount.Add(ctx, m.Count)
}

// RecordRejectedTraces records a traces measurement as rejected. It does not advance the sequence number.
func (tm *ThroughputMeasurements) RecordRejectedTraces(ctx context.Context, m Measurement) {
	tm.traceSizeRejected.Add(ctx, m.Size)
	tm.spanCountRejected.Add(ctx, m.Count)
}

// SequenceNumber returns the current sequence number of this ThroughputMeasurements.
func (tm *ThroughputMeasurements) SequenceNumber() int64 {
	return tm.collectionSequenceNumber.Load()
}

// LogSize returns the total size in bytes of all log payloads added to this ThroughputMeasurements.
func (tm *ThroughputMeasurements) LogSize() int64 {
	return tm.logSize.Val()
}

// MetricSize returns the total size in bytes of all metric payloads added to this ThroughputMeasurements.
func (tm *ThroughputMeasurements) MetricSize() int64 {
	return tm.metricSize.Val()
}

// TraceSize returns the total size in bytes of all trace payloads added to this ThroughputMeasurements.
func (tm *ThroughputMeasurements) TraceSize() int64 {
	return tm.traceSize.Val()
}

// LogCount return the total number of log records that have been added to this ThroughputMeasurements.
func (tm *ThroughputMeasurements) LogCount() int64 {
	return tm.logCount.Val()
}

// DatapointCount return the total number of datapoints that have been added to this ThroughputMeasurements.
func (tm *ThroughputMeasurements) DatapointCount() int64 {
	return tm.datapointCount.Val()
}

// SpanCount return the total number of spans that have been added to this ThroughputMeasurements.
func (tm *ThroughputMeasurements) SpanCount() int64 {
	return tm.spanCount.Val()
}

// LogRawBytes returns the total size in bytes of all raw log content added to this ThroughputMeasurements.
func (tm *ThroughputMeasurements) LogRawBytes() int64 {
	return tm.logRawBytes.Val()
}

// Attributes returns the full set of attributes used on each metric for this ThroughputMeasurements.
func (tm *ThroughputMeasurements) Attributes() attribute.Set {
	return tm.attributes
}

// int64Counter combines a metric.Int64Counter with a atomic.Int64 so that the value of the counter may be
// retrieved.
// The value of the metric counter and val are not guaranteed to be synchronized, but will be eventually consistent.
type int64Counter struct {
	counter    metric.Int64Counter
	val        atomic.Int64
	attributes attribute.Set
}

func newInt64Counter(counter metric.Int64Counter, attributes attribute.Set) *int64Counter {
	return &int64Counter{
		counter:    counter,
		attributes: attributes,
	}
}

func (i *int64Counter) Add(ctx context.Context, delta int64) {
	i.counter.Add(ctx, delta, metric.WithAttributeSet(i.attributes))
	i.val.Add(delta)
}

func (i *int64Counter) Val() int64 {
	return i.val.Load()
}

func metricName(metric string) string {
	return fmt.Sprintf("otelcol_processor_throughputmeasurement_%s", metric)
}

func createMeasurementsAttributeSet(processorID string, extraAttributes map[string]string) attribute.Set {
	attrs := make([]attribute.KeyValue, 0, len(extraAttributes)+1)

	attrs = append(attrs, attribute.String("processor", processorID))
	for k, v := range extraAttributes {
		attrs = append(attrs, attribute.String(k, v))
	}

	return attribute.NewSet(attrs...)
}

// processorMeasurements holds both the throughput measurements and the last collected sequence number for a processor
type processorMeasurements struct {
	measurements          *ThroughputMeasurements
	lastCollectedSequence int64
}

// ResettableThroughputMeasurementsRegistry is a concrete version of ThroughputMeasurementsRegistry that is able to be reset.
type ResettableThroughputMeasurementsRegistry struct {
	measurements     *sync.Map
	emitCountMetrics bool
}

// NewResettableThroughputMeasurementsRegistry creates a new ResettableThroughputMeasurementsRegistry
func NewResettableThroughputMeasurementsRegistry(emitCountMetrics bool) *ResettableThroughputMeasurementsRegistry {
	return &ResettableThroughputMeasurementsRegistry{
		measurements:     &sync.Map{},
		emitCountMetrics: emitCountMetrics,
	}
}

// RegisterThroughputMeasurements registers the ThroughputMeasurements with the registry.
func (ctmr *ResettableThroughputMeasurementsRegistry) RegisterThroughputMeasurements(processorID string, measurements *ThroughputMeasurements) error {
	_, alreadyExists := ctmr.measurements.LoadOrStore(processorID, &processorMeasurements{
		measurements:          measurements,
		lastCollectedSequence: 0,
	})
	if alreadyExists {
		return fmt.Errorf("measurements for processor %q was already registered", processorID)
	}

	return nil
}

// OTLPMeasurements returns all the measurements in this registry as OTLP metrics.
func (ctmr *ResettableThroughputMeasurementsRegistry) OTLPMeasurements(extraAttributes map[string]string) pmetric.Metrics {
	m := pmetric.NewMetrics()
	rm := m.ResourceMetrics().AppendEmpty()
	sm := rm.ScopeMetrics().AppendEmpty()

	ctmr.measurements.Range(func(_, value any) bool {
		pm := value.(*processorMeasurements)
		// Only include metrics collected after the last reported sequence
		if pm.measurements.SequenceNumber() > pm.lastCollectedSequence {
			OTLPThroughputMeasurements(pm.measurements, ctmr.emitCountMetrics, extraAttributes).MoveAndAppendTo(sm.Metrics())

			// Update the max sequence number if the current sequence number is greater
			// This keeps a high water mark of the sequence number that has been reported
			pm.lastCollectedSequence = pm.measurements.SequenceNumber()
		}
		return true
	})

	if m.DataPointCount() == 0 {
		// If there are no datapoints in the metric,
		// we don't want to have an empty ResourceMetrics in the metrics slice.
		return pmetric.NewMetrics()
	}

	return m
}

// Reset unregisters all throughput measurements in this registry
func (ctmr *ResettableThroughputMeasurementsRegistry) Reset() {
	ctmr.measurements = &sync.Map{}
}
