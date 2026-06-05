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

package blitzpdata

import (
	"context"
	"testing"
	"time"

	"github.com/observiq/blitz/embed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap/zaptest"
)

func intPtr(v int64) *int64       { return &v }
func floatPtr(v float64) *float64 { return &v }

func TestMetricAdapter_EmptyBatch_NoPush(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), nil))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{}))
	assert.Equal(t, 0, sink.DataPointCount(), "empty batches must not produce a downstream consume call")
}

func TestMetricAdapter_Gauge_IntValue(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	ts := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name:        "system.cpu.utilization",
		Description: "CPU utilization",
		Unit:        "1",
		Type:        embed.MetricTypeGauge,
		IntValue:    intPtr(42),
		Metadata:    embed.MetricPointMetadata{Timestamp: ts},
	}}))
	m := firstMetric(t, sink.AllMetrics())
	assert.Equal(t, "system.cpu.utilization", m.Name())
	assert.Equal(t, "CPU utilization", m.Description())
	assert.Equal(t, "1", m.Unit())
	require.Equal(t, pmetric.MetricTypeGauge, m.Type())
	dp := m.Gauge().DataPoints().At(0)
	assert.Equal(t, int64(42), dp.IntValue())
	assert.Equal(t, pcommon.NewTimestampFromTime(ts), dp.Timestamp())
}

func TestMetricAdapter_Sum_NonMonotonic(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name:        "system.memory.usage",
		Type:        embed.MetricTypeSum,
		DoubleValue: floatPtr(1024.5),
	}}))
	m := firstMetric(t, sink.AllMetrics())
	require.Equal(t, pmetric.MetricTypeSum, m.Type())
	assert.False(t, m.Sum().IsMonotonic(), "sum maps to non-monotonic")
	assert.Equal(t, pmetric.AggregationTemporalityCumulative, m.Sum().AggregationTemporality())
	assert.Equal(t, 1024.5, m.Sum().DataPoints().At(0).DoubleValue())
}

func TestMetricAdapter_Counter_Monotonic(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name:     "system.network.packets",
		Type:     embed.MetricTypeCounter,
		IntValue: intPtr(998877),
	}}))
	m := firstMetric(t, sink.AllMetrics())
	require.Equal(t, pmetric.MetricTypeSum, m.Type())
	assert.True(t, m.Sum().IsMonotonic(), "counter maps to monotonic sum")
	assert.Equal(t, int64(998877), m.Sum().DataPoints().At(0).IntValue())
}

func TestMetricAdapter_Histogram(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name:                  "http.server.duration",
		Type:                  embed.MetricTypeHistogram,
		HistogramCount:        10,
		HistogramSum:          123.4,
		HistogramMin:          1.5,
		HistogramMax:          50.0,
		HistogramBucketBounds: []float64{5, 10, 25},
		HistogramBucketCounts: []uint64{2, 3, 4, 1},
	}}))
	m := firstMetric(t, sink.AllMetrics())
	require.Equal(t, pmetric.MetricTypeHistogram, m.Type())
	dp := m.Histogram().DataPoints().At(0)
	assert.Equal(t, uint64(10), dp.Count())
	assert.Equal(t, 123.4, dp.Sum())
	assert.Equal(t, 1.5, dp.Min())
	assert.Equal(t, 50.0, dp.Max())
	assert.Equal(t, []float64{5, 10, 25}, dp.ExplicitBounds().AsRaw())
	assert.Equal(t, []uint64{2, 3, 4, 1}, dp.BucketCounts().AsRaw())
	assert.Equal(t, pmetric.AggregationTemporalityCumulative, m.Histogram().AggregationTemporality())
}

func TestMetricAdapter_Histogram_MismatchedBucketsDropped(t *testing.T) {
	// OTel requires len(BucketCounts) == len(ExplicitBounds)+1. A
	// misconfigured generator emitting a mismatched pair (here 3 bounds
	// + 3 counts, should be 4) must not produce an invariant-violating
	// histogram downstream. The adapter drops the buckets and keeps the
	// aggregate stats.
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name:                  "broken.histogram",
		Type:                  embed.MetricTypeHistogram,
		HistogramCount:        9,
		HistogramSum:          42.0,
		HistogramBucketBounds: []float64{5, 10, 25},
		HistogramBucketCounts: []uint64{2, 3, 4}, // one short — should be 4 entries
	}}))
	m := firstMetric(t, sink.AllMetrics())
	require.Equal(t, pmetric.MetricTypeHistogram, m.Type())
	dp := m.Histogram().DataPoints().At(0)
	assert.Equal(t, uint64(9), dp.Count(), "aggregate stats preserved")
	assert.Equal(t, 42.0, dp.Sum())
	assert.Equal(t, 0, dp.ExplicitBounds().Len(), "mismatched bounds dropped")
	assert.Equal(t, 0, dp.BucketCounts().Len(), "mismatched counts dropped")
}

func TestMetricAdapter_Histogram_NoBuckets_OK(t *testing.T) {
	// Empty bounds + empty counts is the legitimate no-buckets case and
	// passes through without a warning or an invariant violation.
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name:           "bucketless.histogram",
		Type:           embed.MetricTypeHistogram,
		HistogramCount: 5,
		HistogramSum:   10.0,
	}}))
	m := firstMetric(t, sink.AllMetrics())
	dp := m.Histogram().DataPoints().At(0)
	assert.Equal(t, uint64(5), dp.Count())
	assert.Equal(t, 0, dp.ExplicitBounds().Len())
	assert.Equal(t, 0, dp.BucketCounts().Len())
}

func TestMetricAdapter_UnknownType_FallsBackToGauge(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name:        "mystery.metric",
		Type:        embed.MetricType("exotic"),
		DoubleValue: floatPtr(7),
	}}))
	m := firstMetric(t, sink.AllMetrics())
	require.Equal(t, pmetric.MetricTypeGauge, m.Type(), "unknown types emit as gauge")
	assert.Equal(t, 7.0, m.Gauge().DataPoints().At(0).DoubleValue())
}

func TestMetricAdapter_NoValue_EmitsZeroDouble(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name: "valueless.metric",
		Type: embed.MetricTypeGauge,
	}}))
	m := firstMetric(t, sink.AllMetrics())
	assert.Equal(t, 0.0, m.Gauge().DataPoints().At(0).DoubleValue())
}

func TestMetricAdapter_ZeroTimestamp_FallsBackToNow(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, zaptest.NewLogger(t))
	before := time.Now()
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name:     "no.ts",
		Type:     embed.MetricTypeGauge,
		IntValue: intPtr(1),
	}}))
	after := time.Now()
	m := firstMetric(t, sink.AllMetrics())
	ts := m.Gauge().DataPoints().At(0).Timestamp().AsTime()
	assert.False(t, ts.Before(before))
	assert.False(t, ts.After(after))
}

func TestMetricAdapter_PerPointAttributes_MergeAndLock(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	attrs := LockableAttrs{
		Base:   map[string]any{"service.team": "ops", "run.id": "base"},
		Locked: map[string]struct{}{"service.team": {}},
	}
	a := NewMetricAdapter(sink, LockableAttrs{}, attrs, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{
		Name:     "m",
		Type:     embed.MetricTypeGauge,
		IntValue: intPtr(1),
		Metadata: embed.MetricPointMetadata{
			Attributes: map[string]string{
				"service.team": "blitz-team", // locked — dropped
				"run.id":       "blitz-run",  // unlocked — wins
				"cpu":          "cpu0",       // blitz-only — lands
			},
		},
	}}))
	m := firstMetric(t, sink.AllMetrics())
	got := m.Gauge().DataPoints().At(0).Attributes().AsRaw()
	assert.Equal(t, "ops", got["service.team"])
	assert.Equal(t, "blitz-run", got["run.id"])
	assert.Equal(t, "cpu0", got["cpu"])
}

func TestMetricAdapter_PerPointResource_MergeLockAndGroup(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	resource := LockableAttrs{Base: map[string]any{"cluster.name": "gargantua"}}
	a := NewMetricAdapter(sink, resource, LockableAttrs{}, zaptest.NewLogger(t))
	require.NoError(t, a.ConsumeMetrics(context.Background(), []embed.MetricPoint{
		{Name: "a", Type: embed.MetricTypeGauge, IntValue: intPtr(1),
			Metadata: embed.MetricPointMetadata{Resource: map[string]string{"host.name": "h1"}}},
		{Name: "b", Type: embed.MetricTypeGauge, IntValue: intPtr(2),
			Metadata: embed.MetricPointMetadata{Resource: map[string]string{"host.name": "h2"}}},
		{Name: "c", Type: embed.MetricTypeGauge, IntValue: intPtr(3),
			Metadata: embed.MetricPointMetadata{Resource: map[string]string{"host.name": "h1"}}},
	}))
	metrics := sink.AllMetrics()
	require.Len(t, metrics, 1)
	require.Equal(t, 2, metrics[0].ResourceMetrics().Len(), "two distinct resources → two ResourceMetrics")
	rm0 := metrics[0].ResourceMetrics().At(0)
	res0 := rm0.Resource().Attributes().AsRaw()
	assert.Equal(t, "h1", res0["host.name"])
	assert.Equal(t, "gargantua", res0["cluster.name"], "configured base flows into every group")
	assert.Equal(t, 2, rm0.ScopeMetrics().At(0).Metrics().Len(), "h1 group carries points a and c")
	rm1 := metrics[0].ResourceMetrics().At(1)
	assert.Equal(t, "h2", rm1.Resource().Attributes().AsRaw()["host.name"])
	assert.Equal(t, 1, rm1.ScopeMetrics().At(0).Metrics().Len())
}

func TestMetricAdapter_NilLogger_NoPanic(t *testing.T) {
	sink := &consumertest.MetricsSink{}
	a := NewMetricAdapter(sink, LockableAttrs{}, LockableAttrs{}, nil)
	require.NotPanics(t, func() {
		_ = a.ConsumeMetrics(context.Background(), []embed.MetricPoint{{Name: "x", Type: embed.MetricTypeGauge}})
	})
}

// firstMetric pulls the single metric out of a sink that expects
// exactly one batch with one resource, one scope, and one metric.
func firstMetric(t *testing.T, metrics []pmetric.Metrics) pmetric.Metric {
	t.Helper()
	require.Len(t, metrics, 1, "expected exactly one pmetric.Metrics batch")
	require.Equal(t, 1, metrics[0].ResourceMetrics().Len())
	rm := metrics[0].ResourceMetrics().At(0)
	require.Equal(t, 1, rm.ScopeMetrics().Len())
	sm := rm.ScopeMetrics().At(0)
	require.Equal(t, 1, sm.Metrics().Len())
	return sm.Metrics().At(0)
}
