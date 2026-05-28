// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdkexporter

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

type sdkExporter struct {
	cfg    *Config
	logger *zap.Logger
	cache  *instrumentCache
}

func newSDKExporter(cfg *Config, set exporter.Settings) *sdkExporter {
	return &sdkExporter{
		cfg:    cfg,
		logger: set.Logger,
		cache:  newInstrumentCache(set.MeterProvider, set.Logger),
	}
}

func (e *sdkExporter) start(_ context.Context, _ component.Host) error {
	return nil
}

func (e *sdkExporter) shutdown(_ context.Context) error {
	return nil
}

// consumeMetrics dispatches each pdata Metric onto the SDK MeterProvider.
// Returns nil unconditionally — failing back to the pipeline would turn
// self-telemetry republishing into a load-bearing dependency, which is the
// opposite of what callers want.
func (e *sdkExporter) consumeMetrics(ctx context.Context, md pmetric.Metrics) error {
	rms := md.ResourceMetrics()
	for i := 0; i < rms.Len(); i++ {
		rm := rms.At(i)
		resourceAttrs := resourceAttrsForConfig(rm.Resource(), e.cfg)
		sms := rm.ScopeMetrics()
		for j := 0; j < sms.Len(); j++ {
			sm := sms.At(j)
			scope := sm.Scope()
			sk := scopeKey{name: scope.Name(), version: scope.Version()}
			meter := e.cache.meter(scope.Name(), scope.Version(), sm.SchemaUrl())
			ms := sm.Metrics()
			for k := 0; k < ms.Len(); k++ {
				e.consumeMetric(ctx, meter, sk, ms.At(k), resourceAttrs)
			}
		}
	}
	return nil
}

func (e *sdkExporter) consumeMetric(
	ctx context.Context,
	meter metric.Meter,
	sk scopeKey,
	m pmetric.Metric,
	resourceAttrs []attribute.KeyValue,
) {
	switch m.Type() {
	case pmetric.MetricTypeSum:
		e.consumeSum(ctx, meter, sk, m, resourceAttrs)
	case pmetric.MetricTypeGauge:
		e.consumeGauge(ctx, meter, sk, m, resourceAttrs)
	case pmetric.MetricTypeHistogram:
		e.warnUnsupported("histogram", m.Name())
	case pmetric.MetricTypeExponentialHistogram:
		e.warnUnsupported("exponential_histogram", m.Name())
	case pmetric.MetricTypeSummary:
		e.warnUnsupported("summary", m.Name())
	case pmetric.MetricTypeEmpty:
		// nothing to do
	}
}

func (e *sdkExporter) consumeSum(
	ctx context.Context,
	meter metric.Meter,
	sk scopeKey,
	m pmetric.Metric,
	resourceAttrs []attribute.KeyValue,
) {
	sum := m.Sum()
	if sum.AggregationTemporality() != pmetric.AggregationTemporalityDelta {
		e.warnUnsupported("sum_cumulative", m.Name())
		return
	}
	isMonotonic := sum.IsMonotonic()
	dps := sum.DataPoints()
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		attrs := dpAttrsToOtel(dp.Attributes(), resourceAttrs)
		switch dp.ValueType() {
		case pmetric.NumberDataPointValueTypeInt:
			e.recordSumInt(ctx, meter, sk, m, dp.IntValue(), attrs, isMonotonic)
		case pmetric.NumberDataPointValueTypeDouble:
			e.recordSumFloat(ctx, meter, sk, m, dp.DoubleValue(), attrs, isMonotonic)
		case pmetric.NumberDataPointValueTypeEmpty:
			// nothing to record
		}
	}
}

func (e *sdkExporter) recordSumInt(
	ctx context.Context, meter metric.Meter, sk scopeKey, m pmetric.Metric,
	val int64, attrs []attribute.KeyValue, isMonotonic bool,
) {
	if isMonotonic {
		inst, err := e.cache.int64Counter(meter, sk, m.Name(), m.Unit(), m.Description())
		if err != nil {
			e.warnInstrumentErr("int64_counter", m.Name(), err)
			return
		}
		inst.Add(ctx, val, metric.WithAttributes(attrs...))
		return
	}
	inst, err := e.cache.int64UpDownCounter(meter, sk, m.Name(), m.Unit(), m.Description())
	if err != nil {
		e.warnInstrumentErr("int64_updown_counter", m.Name(), err)
		return
	}
	inst.Add(ctx, val, metric.WithAttributes(attrs...))
}

func (e *sdkExporter) recordSumFloat(
	ctx context.Context, meter metric.Meter, sk scopeKey, m pmetric.Metric,
	val float64, attrs []attribute.KeyValue, isMonotonic bool,
) {
	if isMonotonic {
		inst, err := e.cache.float64Counter(meter, sk, m.Name(), m.Unit(), m.Description())
		if err != nil {
			e.warnInstrumentErr("float64_counter", m.Name(), err)
			return
		}
		inst.Add(ctx, val, metric.WithAttributes(attrs...))
		return
	}
	inst, err := e.cache.float64UpDownCounter(meter, sk, m.Name(), m.Unit(), m.Description())
	if err != nil {
		e.warnInstrumentErr("float64_updown_counter", m.Name(), err)
		return
	}
	inst.Add(ctx, val, metric.WithAttributes(attrs...))
}

func (e *sdkExporter) consumeGauge(
	ctx context.Context,
	meter metric.Meter,
	sk scopeKey,
	m pmetric.Metric,
	resourceAttrs []attribute.KeyValue,
) {
	dps := m.Gauge().DataPoints()
	for i := 0; i < dps.Len(); i++ {
		dp := dps.At(i)
		attrs := dpAttrsToOtel(dp.Attributes(), resourceAttrs)
		switch dp.ValueType() {
		case pmetric.NumberDataPointValueTypeInt:
			inst, err := e.cache.int64Gauge(meter, sk, m.Name(), m.Unit(), m.Description())
			if err != nil {
				e.warnInstrumentErr("int64_gauge", m.Name(), err)
				continue
			}
			inst.Record(ctx, dp.IntValue(), metric.WithAttributes(attrs...))
		case pmetric.NumberDataPointValueTypeDouble:
			inst, err := e.cache.float64Gauge(meter, sk, m.Name(), m.Unit(), m.Description())
			if err != nil {
				e.warnInstrumentErr("float64_gauge", m.Name(), err)
				continue
			}
			inst.Record(ctx, dp.DoubleValue(), metric.WithAttributes(attrs...))
		case pmetric.NumberDataPointValueTypeEmpty:
			// nothing to record
		}
	}
}

func (e *sdkExporter) warnUnsupported(kind, name string) {
	e.logger.Warn("metric type not supported by sdkexporter; dropped",
		zap.String("type", kind),
		zap.String("metric", name),
	)
}

func (e *sdkExporter) warnInstrumentErr(kind, name string, err error) {
	e.logger.Warn("failed to create SDK instrument",
		zap.String("kind", kind),
		zap.String("metric", name),
		zap.Error(err),
	)
}
