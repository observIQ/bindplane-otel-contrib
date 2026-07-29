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

package snapshot

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestCopyLogsTail(t *testing.T) {
	// Two resources with two scopes of three records each, all uniquely
	// numbered in traversal order.
	makeLogs := func() plog.Logs {
		ld := plog.NewLogs()
		n := 0
		for r := 0; r < 2; r++ {
			rl := ld.ResourceLogs().AppendEmpty()
			rl.Resource().Attributes().PutStr("resource", "r"+strconv.Itoa(r))
			rl.SetSchemaUrl("https://example.com/resource-schema")
			for s := 0; s < 2; s++ {
				sl := rl.ScopeLogs().AppendEmpty()
				sl.Scope().SetName("scope-" + strconv.Itoa(s))
				sl.SetSchemaUrl("https://example.com/scope-schema")
				for i := 0; i < 3; i++ {
					sl.LogRecords().AppendEmpty().Body().SetStr(strconv.Itoa(n))
					n++
				}
			}
		}
		return ld
	}

	// collectBodies returns the record bodies in traversal order.
	collectBodies := func(ld plog.Logs) []string {
		var bodies []string
		for ri := 0; ri < ld.ResourceLogs().Len(); ri++ {
			sls := ld.ResourceLogs().At(ri).ScopeLogs()
			for si := 0; si < sls.Len(); si++ {
				lrs := sls.At(si).LogRecords()
				for li := 0; li < lrs.Len(); li++ {
					bodies = append(bodies, lrs.At(li).Body().Str())
				}
			}
		}
		return bodies
	}

	t.Run("zero skip copies everything", func(t *testing.T) {
		src := makeLogs()
		dst := plog.NewLogs()
		copyLogsTail(src, dst, 0)

		require.Equal(t, 12, dst.LogRecordCount())
		assert.Equal(t, collectBodies(src), collectBodies(dst))
		// Whole-group copies preserve schema URLs.
		assert.Equal(t, "https://example.com/resource-schema", dst.ResourceLogs().At(0).SchemaUrl())
		assert.Equal(t, "https://example.com/scope-schema", dst.ResourceLogs().At(0).ScopeLogs().At(0).SchemaUrl())
	})

	t.Run("skip spanning groups keeps the newest records in order", func(t *testing.T) {
		src := makeLogs()
		dst := plog.NewLogs()
		copyLogsTail(src, dst, 7)

		require.Equal(t, 5, dst.LogRecordCount())
		assert.Equal(t, []string{"7", "8", "9", "10", "11"}, collectBodies(dst))
		// The partially copied group preserves resource, scope, and schema URLs.
		assert.Equal(t, "https://example.com/resource-schema", dst.ResourceLogs().At(0).SchemaUrl())
		assert.Equal(t, "https://example.com/scope-schema", dst.ResourceLogs().At(0).ScopeLogs().At(0).SchemaUrl())
		resourceVal, ok := dst.ResourceLogs().At(0).Resource().Attributes().Get("resource")
		require.True(t, ok)
		assert.Equal(t, "r1", resourceVal.Str())
	})

	t.Run("source is not mutated", func(t *testing.T) {
		src := makeLogs()
		dst := plog.NewLogs()
		copyLogsTail(src, dst, 7)
		assert.Equal(t, 12, src.LogRecordCount())
	})
}

func TestCopyMetricsTail(t *testing.T) {
	// One resource, one scope, three metrics with two data points each.
	makeMetrics := func() pmetric.Metrics {
		md := pmetric.NewMetrics()
		rm := md.ResourceMetrics().AppendEmpty()
		rm.Resource().Attributes().PutStr("resource", "r0")
		rm.SetSchemaUrl("https://example.com/resource-schema")
		sm := rm.ScopeMetrics().AppendEmpty()
		sm.Scope().SetName("scope")
		sm.SetSchemaUrl("https://example.com/scope-schema")

		n := 0
		for m := 0; m < 3; m++ {
			metric := sm.Metrics().AppendEmpty()
			metric.SetName("metric-" + strconv.Itoa(m))
			metric.SetDescription("description")
			metric.SetUnit("1")
			metric.Metadata().PutStr("meta", "value")
			sum := metric.SetEmptySum()
			sum.SetIsMonotonic(true)
			sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
			for i := 0; i < 2; i++ {
				sum.DataPoints().AppendEmpty().SetIntValue(int64(n))
				n++
			}
		}
		return md
	}

	t.Run("zero skip copies everything", func(t *testing.T) {
		src := makeMetrics()
		dst := pmetric.NewMetrics()
		copyMetricsTail(src, dst, 0)
		require.Equal(t, 6, dst.DataPointCount())
	})

	t.Run("skip spanning metrics keeps the newest data points", func(t *testing.T) {
		src := makeMetrics()
		dst := pmetric.NewMetrics()
		copyMetricsTail(src, dst, 3)

		require.Equal(t, 3, dst.DataPointCount())

		ms := dst.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
		require.Equal(t, 2, ms.Len())

		// First kept metric is metric-1's second data point.
		partial := ms.At(0)
		assert.Equal(t, "metric-1", partial.Name())
		assert.Equal(t, "description", partial.Description())
		assert.Equal(t, "1", partial.Unit())
		metaVal, ok := partial.Metadata().Get("meta")
		require.True(t, ok)
		assert.Equal(t, "value", metaVal.Str())
		assert.Equal(t, pmetric.MetricTypeSum, partial.Type())
		assert.True(t, partial.Sum().IsMonotonic())
		assert.Equal(t, pmetric.AggregationTemporalityCumulative, partial.Sum().AggregationTemporality())
		require.Equal(t, 1, partial.Sum().DataPoints().Len())
		assert.Equal(t, int64(3), partial.Sum().DataPoints().At(0).IntValue())

		// metric-2 is copied whole.
		whole := ms.At(1)
		assert.Equal(t, "metric-2", whole.Name())
		require.Equal(t, 2, whole.Sum().DataPoints().Len())

		// Group metadata survives the partial copy.
		assert.Equal(t, "https://example.com/resource-schema", dst.ResourceMetrics().At(0).SchemaUrl())
		assert.Equal(t, "https://example.com/scope-schema", dst.ResourceMetrics().At(0).ScopeMetrics().At(0).SchemaUrl())
	})
}

func TestCopyTracesTail(t *testing.T) {
	makeTraces := func() ptrace.Traces {
		td := ptrace.NewTraces()
		rs := td.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("resource", "r0")
		rs.SetSchemaUrl("https://example.com/resource-schema")
		ss := rs.ScopeSpans().AppendEmpty()
		ss.Scope().SetName("scope")
		ss.SetSchemaUrl("https://example.com/scope-schema")
		for i := 0; i < 6; i++ {
			ss.Spans().AppendEmpty().SetName("span-" + strconv.Itoa(i))
		}
		return td
	}

	t.Run("zero skip copies everything", func(t *testing.T) {
		src := makeTraces()
		dst := ptrace.NewTraces()
		copyTracesTail(src, dst, 0)
		require.Equal(t, 6, dst.SpanCount())
	})

	t.Run("skip keeps the newest spans in order", func(t *testing.T) {
		src := makeTraces()
		dst := ptrace.NewTraces()
		copyTracesTail(src, dst, 4)

		require.Equal(t, 2, dst.SpanCount())
		spans := dst.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
		assert.Equal(t, "span-4", spans.At(0).Name())
		assert.Equal(t, "span-5", spans.At(1).Name())
		assert.Equal(t, "https://example.com/resource-schema", dst.ResourceSpans().At(0).SchemaUrl())
		assert.Equal(t, "https://example.com/scope-schema", dst.ResourceSpans().At(0).ScopeSpans().At(0).SchemaUrl())
	})
}
