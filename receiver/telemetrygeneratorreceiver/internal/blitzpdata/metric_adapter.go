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

package blitzpdata // import "github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/internal/blitzpdata"

import (
	"context"
	"time"

	"github.com/observiq/blitz/embed"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

// MetricAdapter implements embed.MetricConsumer. Each ConsumeMetrics
// call builds a fresh pmetric.Metrics from the batch and pushes it to
// the receiver's downstream consumer.
//
// Shape mirrors LogAdapter: per-receiver-entry LockableAttrs maps for
// resource and per-point attributes; blitz's per-point
// `Metadata.Resource` and `Metadata.Attributes` merge over the configured
// bases with locked keys preserved; points whose merged resource maps
// fingerprint differently are emitted under separate `ResourceMetrics`
// (Q1 resource grouping).
//
// Each embed.MetricPoint becomes one pmetric.Metric carrying a single
// data point. Blitz emits pre-aggregated points (it is a generator,
// not an aggregating SDK), so the receiver does not attempt to
// coalesce same-name points into multi-point metrics — downstream
// processors can do that if a pipeline needs it.
type MetricAdapter struct {
	consumer consumer.Metrics
	resource LockableAttrs
	attrs    LockableAttrs
	logger   *zap.Logger
}

// NewMetricAdapter constructs an adapter that emits to the given
// consumer. resource and attrs follow the same per-key locking semantics as
// NewLogAdapter. A nil logger yields a no-op logger.
func NewMetricAdapter(c consumer.Metrics, resource, attrs LockableAttrs, logger *zap.Logger) *MetricAdapter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MetricAdapter{
		consumer: c,
		resource: resource,
		attrs:    attrs,
		logger:   logger,
	}
}

// ConsumeMetrics satisfies embed.MetricConsumer. Points are grouped by
// merged-resource fingerprint (one ResourceMetrics per unique merged
// resource, first-occurrence ordered); each point converts to one
// pmetric.Metric with a single data point. Empty batches are no-ops.
func (a *MetricAdapter) ConsumeMetrics(ctx context.Context, points []embed.MetricPoint) error {
	if len(points) == 0 {
		return nil
	}

	type group struct {
		merged map[string]any
		points []int
	}
	groups := make(map[string]*group)
	order := make([]string, 0)
	for i := range points {
		merged := a.resource.MergeWithStringOverlay(points[i].Metadata.Resource)
		fp := FingerprintMap(merged)
		g, exists := groups[fp]
		if !exists {
			g = &group{merged: merged}
			groups[fp] = g
			order = append(order, fp)
		}
		g.points = append(g.points, i)
	}

	metrics := pmetric.NewMetrics()
	for _, fp := range order {
		g := groups[fp]
		rm := metrics.ResourceMetrics().AppendEmpty()
		if err := rm.Resource().Attributes().FromRaw(g.merged); err != nil {
			a.logger.Warn("blitzpdata: failed to set resource attributes", zap.Error(err))
		}
		sm := rm.ScopeMetrics().AppendEmpty()
		sm.Scope().SetName(scopeName)
		for _, idx := range g.points {
			a.appendPoint(sm.Metrics().AppendEmpty(), &points[idx])
		}
	}
	return a.consumer.ConsumeMetrics(ctx, metrics)
}

// appendPoint populates a fresh pmetric.Metric from a single
// embed.MetricPoint.
//
// Type mapping:
//   - gauge     → Gauge
//   - sum       → Sum, cumulative, non-monotonic (an up-down value)
//   - counter   → Sum, cumulative, monotonic (a classic counter)
//   - histogram → Histogram, cumulative, with explicit bucket bounds
//
// Unknown metric types are emitted as Gauge with a warning — dropping
// the point silently would make a misbehaving generator invisible, and
// failing the whole batch for one bad point is disproportionate for a
// telemetry generator.
func (a *MetricAdapter) appendPoint(m pmetric.Metric, pt *embed.MetricPoint) {
	m.SetName(pt.Name)
	m.SetDescription(pt.Description)
	m.SetUnit(pt.Unit)

	ts := pt.Metadata.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	pts := pcommon.NewTimestampFromTime(ts)

	mergedAttrs := a.attrs.MergeWithStringOverlay(pt.Metadata.Attributes)

	switch pt.Type {
	case embed.MetricTypeGauge:
		dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
		a.setNumberValue(dp, pt, pts, mergedAttrs)
	case embed.MetricTypeSum:
		sum := m.SetEmptySum()
		sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
		sum.SetIsMonotonic(false)
		dp := sum.DataPoints().AppendEmpty()
		a.setNumberValue(dp, pt, pts, mergedAttrs)
	case embed.MetricTypeCounter:
		sum := m.SetEmptySum()
		sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
		sum.SetIsMonotonic(true)
		dp := sum.DataPoints().AppendEmpty()
		a.setNumberValue(dp, pt, pts, mergedAttrs)
	case embed.MetricTypeHistogram:
		hist := m.SetEmptyHistogram()
		hist.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
		dp := hist.DataPoints().AppendEmpty()
		dp.SetTimestamp(pts)
		dp.SetCount(pt.HistogramCount)
		dp.SetSum(pt.HistogramSum)
		dp.SetMin(pt.HistogramMin)
		dp.SetMax(pt.HistogramMax)
		// OTel requires len(BucketCounts) == len(ExplicitBounds)+1.
		// blitz is a generator, not an aggregating SDK, so a
		// misconfigured generator can emit a mismatched pair. The
		// pcommon slice FromRaw setters are void (they cannot report
		// the violation), so writing a mismatched pair produces a
		// histogram that breaks the invariant and only fails much later,
		// deep in an exporter, with a confusing message. Validate here:
		// on match, set both bounds and counts; on mismatch, warn
		// (naming the metric + the lengths) and emit the point with
		// aggregate stats only (count/sum/min/max, no buckets), which is
		// still a valid — if less detailed — histogram. Empty/empty is
		// the legitimate no-buckets case and passes through silently.
		bounds := pt.HistogramBucketBounds
		counts := pt.HistogramBucketCounts
		switch {
		case len(counts) == len(bounds)+1:
			dp.ExplicitBounds().FromRaw(bounds)
			dp.BucketCounts().FromRaw(counts)
		case len(bounds) > 0 || len(counts) > 0:
			a.logger.Warn("blitzpdata: histogram bucket bounds/counts violate len(counts)==len(bounds)+1; emitting point without buckets",
				zap.String("metric", pt.Name),
				zap.Int("bounds", len(bounds)),
				zap.Int("counts", len(counts)))
		}
		if err := dp.Attributes().FromRaw(mergedAttrs); err != nil {
			a.logger.Warn("blitzpdata: failed to set histogram point attributes", zap.Error(err))
		}
	default:
		a.logger.Warn("blitzpdata: unknown metric type; emitting as gauge",
			zap.String("metric", pt.Name), zap.String("type", string(pt.Type)))
		dp := m.SetEmptyGauge().DataPoints().AppendEmpty()
		a.setNumberValue(dp, pt, pts, mergedAttrs)
	}
}

// setNumberValue writes the point's numeric value, timestamp, and
// merged attributes onto a NumberDataPoint. IntValue wins when both
// are non-nil (the embed contract documents them as mutually
// exclusive, so both-set is a generator bug; preferring the int keeps
// the choice deterministic). Neither set yields a zero-valued double
// point — emitting zero is more diagnosable downstream than dropping
// the point.
func (a *MetricAdapter) setNumberValue(dp pmetric.NumberDataPoint, pt *embed.MetricPoint, ts pcommon.Timestamp, mergedAttrs map[string]any) {
	dp.SetTimestamp(ts)
	switch {
	case pt.IntValue != nil:
		dp.SetIntValue(*pt.IntValue)
	case pt.DoubleValue != nil:
		dp.SetDoubleValue(*pt.DoubleValue)
	default:
		dp.SetDoubleValue(0)
	}
	if err := dp.Attributes().FromRaw(mergedAttrs); err != nil {
		a.logger.Warn("blitzpdata: failed to set point attributes", zap.Error(err))
	}
}
