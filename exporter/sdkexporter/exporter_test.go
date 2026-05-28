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
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exportertest"
	"go.opentelemetry.io/collector/pdata/pmetric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/observiq/bindplane-otel-contrib/exporter/sdkexporter/internal/metadata"
)

// testHarness wires the exporter to an SDK MeterProvider with a ManualReader
// so tests can inspect what the exporter recorded.
type testHarness struct {
	t      *testing.T
	reader *sdkmetric.ManualReader
	exp    exporter.Metrics
}

func newHarness(t *testing.T, cfg *Config) *testHarness {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	set := exportertest.NewNopSettings(metadata.Type)
	set.MeterProvider = mp

	factory := NewFactory()
	if cfg == nil {
		cfg = factory.CreateDefaultConfig().(*Config)
	}
	exp, err := factory.CreateMetrics(context.Background(), set, cfg)
	require.NoError(t, err)
	require.NoError(t, exp.Start(context.Background(), componenttest.NewNopHost()))
	t.Cleanup(func() {
		_ = exp.Shutdown(context.Background())
	})
	return &testHarness{t: t, reader: reader, exp: exp}
}

func (h *testHarness) collect() metricdata.ResourceMetrics {
	h.t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(h.t, h.reader.Collect(context.Background(), &rm))
	return rm
}

func findMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for i := range rm.ScopeMetrics {
		for j := range rm.ScopeMetrics[i].Metrics {
			if rm.ScopeMetrics[i].Metrics[j].Name == name {
				return &rm.ScopeMetrics[i].Metrics[j]
			}
		}
	}
	return nil
}

func buildSum(name string, temporality pmetric.AggregationTemporality, monotonic bool, intVal int64, attrs map[string]string) pmetric.Metrics {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	sm := rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("scope.test")
	m := sm.Metrics().AppendEmpty()
	m.SetName(name)
	sum := m.SetEmptySum()
	sum.SetAggregationTemporality(temporality)
	sum.SetIsMonotonic(monotonic)
	dp := sum.DataPoints().AppendEmpty()
	dp.SetIntValue(intVal)
	for k, v := range attrs {
		dp.Attributes().PutStr(k, v)
	}
	return md
}

func TestDeltaMonotonicIntSum(t *testing.T) {
	h := newHarness(t, nil)
	md := buildSum("requests", pmetric.AggregationTemporalityDelta, true, 5, map[string]string{"k": "v"})
	require.NoError(t, h.exp.ConsumeMetrics(context.Background(), md))

	rm := h.collect()
	m := findMetric(rm, "requests")
	require.NotNil(t, m, "metric not found")
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64], got %T", m.Data)
	require.True(t, sum.IsMonotonic)
	require.Len(t, sum.DataPoints, 1)
	require.Equal(t, int64(5), sum.DataPoints[0].Value)
}

func TestDeltaNonMonotonicFloatSum(t *testing.T) {
	h := newHarness(t, nil)
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	sm := rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("scope.test")
	m := sm.Metrics().AppendEmpty()
	m.SetName("queue.depth.delta")
	sum := m.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	sum.SetIsMonotonic(false)
	dp := sum.DataPoints().AppendEmpty()
	dp.SetDoubleValue(2.5)

	require.NoError(t, h.exp.ConsumeMetrics(context.Background(), md))

	got := h.collect()
	mt := findMetric(got, "queue.depth.delta")
	require.NotNil(t, mt)
	s, ok := mt.Data.(metricdata.Sum[float64])
	require.True(t, ok, "expected Sum[float64], got %T", mt.Data)
	require.False(t, s.IsMonotonic)
	require.Len(t, s.DataPoints, 1)
	require.InDelta(t, 2.5, s.DataPoints[0].Value, 1e-9)
}

func TestGauge(t *testing.T) {
	h := newHarness(t, nil)
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	sm := rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("scope.test")

	mInt := sm.Metrics().AppendEmpty()
	mInt.SetName("temp.int")
	mInt.SetEmptyGauge().DataPoints().AppendEmpty().SetIntValue(42)

	mFloat := sm.Metrics().AppendEmpty()
	mFloat.SetName("temp.float")
	mFloat.SetEmptyGauge().DataPoints().AppendEmpty().SetDoubleValue(3.14)

	require.NoError(t, h.exp.ConsumeMetrics(context.Background(), md))

	got := h.collect()
	gi, ok := findMetric(got, "temp.int").Data.(metricdata.Gauge[int64])
	require.True(t, ok)
	require.Equal(t, int64(42), gi.DataPoints[0].Value)
	gf, ok := findMetric(got, "temp.float").Data.(metricdata.Gauge[float64])
	require.True(t, ok)
	require.InDelta(t, 3.14, gf.DataPoints[0].Value, 1e-9)
}

// All currently-unsupported pdata types should be silently dropped (with a
// sampled warn log) and never reach the SDK MeterProvider.
func TestUnsupportedTypesDropped(t *testing.T) {
	h := newHarness(t, nil)
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	sm := rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("scope.test")

	// Cumulative sum
	mCumSum := sm.Metrics().AppendEmpty()
	mCumSum.SetName("cum.sum")
	cs := mCumSum.SetEmptySum()
	cs.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	cs.SetIsMonotonic(true)
	cs.DataPoints().AppendEmpty().SetIntValue(99)

	// Histogram
	mHist := sm.Metrics().AppendEmpty()
	mHist.SetName("hist")
	hist := mHist.SetEmptyHistogram()
	hist.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	dp := hist.DataPoints().AppendEmpty()
	dp.ExplicitBounds().FromRaw([]float64{1, 5})
	dp.BucketCounts().FromRaw([]uint64{1, 2, 0})
	dp.SetCount(3)

	// Exp histogram
	mExp := sm.Metrics().AppendEmpty()
	mExp.SetName("expo")
	mExp.SetEmptyExponentialHistogram().DataPoints().AppendEmpty()

	// Summary
	mSummary := sm.Metrics().AppendEmpty()
	mSummary.SetName("summ")
	mSummary.SetEmptySummary().DataPoints().AppendEmpty()

	require.NoError(t, h.exp.ConsumeMetrics(context.Background(), md))
	got := h.collect()
	for _, name := range []string{"cum.sum", "hist", "expo", "summ"} {
		require.Nil(t, findMetric(got, name), "expected %q to be dropped", name)
	}
}

func TestIncludeResourceAttributes(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.IncludeResourceAttributes = true
	cfg.ResourceAttributeKeys = []string{"service.name"}
	h := newHarness(t, cfg)

	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	rm.Resource().Attributes().PutStr("service.name", "svc")
	rm.Resource().Attributes().PutStr("env", "prod") // not in allowlist
	sm := rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("scope.test")
	m := sm.Metrics().AppendEmpty()
	m.SetName("requests")
	sum := m.SetEmptySum()
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	sum.SetIsMonotonic(true)
	sum.DataPoints().AppendEmpty().SetIntValue(1)

	require.NoError(t, h.exp.ConsumeMetrics(context.Background(), md))
	got := h.collect()
	mt := findMetric(got, "requests")
	require.NotNil(t, mt)
	s := mt.Data.(metricdata.Sum[int64])
	require.Len(t, s.DataPoints, 1)
	dp := s.DataPoints[0]
	v, ok := dp.Attributes.Value("service.name")
	require.True(t, ok, "service.name should have been folded in")
	require.Equal(t, "svc", v.AsString())
	_, ok = dp.Attributes.Value("env")
	require.False(t, ok, "env should have been filtered out by allowlist")
}

func TestTwoScopesProduceTwoScopeMetrics(t *testing.T) {
	h := newHarness(t, nil)
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()

	sm1 := rm.ScopeMetrics().AppendEmpty()
	sm1.Scope().SetName("scope.a")
	m1 := sm1.Metrics().AppendEmpty()
	m1.SetName("a.requests")
	s1 := m1.SetEmptySum()
	s1.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	s1.SetIsMonotonic(true)
	s1.DataPoints().AppendEmpty().SetIntValue(1)

	sm2 := rm.ScopeMetrics().AppendEmpty()
	sm2.Scope().SetName("scope.b")
	m2 := sm2.Metrics().AppendEmpty()
	m2.SetName("b.requests")
	s2 := m2.SetEmptySum()
	s2.SetAggregationTemporality(pmetric.AggregationTemporalityDelta)
	s2.SetIsMonotonic(true)
	s2.DataPoints().AppendEmpty().SetIntValue(2)

	require.NoError(t, h.exp.ConsumeMetrics(context.Background(), md))
	rmOut := h.collect()
	scopes := map[string]bool{}
	for _, sm := range rmOut.ScopeMetrics {
		scopes[sm.Scope.Name] = true
	}
	require.True(t, scopes["scope.a"], "expected scope.a in output")
	require.True(t, scopes["scope.b"], "expected scope.b in output")
}

// Sanity check that a no-op (empty pmetric.Metrics) doesn't fail. The reader
// will still see exporterhelper's own self-telemetry (recorded against the
// same MeterProvider — which is the whole point of this exporter); we just
// assert no scope from us was registered.
func TestEmptyMetrics(t *testing.T) {
	h := newHarness(t, nil)
	require.NoError(t, h.exp.ConsumeMetrics(context.Background(), pmetric.NewMetrics()))
	rm := h.collect()
	for _, sm := range rm.ScopeMetrics {
		require.NotEqual(t, "scope.test", sm.Scope.Name, "should not have created our test scope")
	}
}
