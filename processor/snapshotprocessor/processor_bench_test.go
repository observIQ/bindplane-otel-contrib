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

package snapshotprocessor

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// These benchmarks measure the per-batch cost of the snapshot processor's hot
// path (processLogs/processMetrics/processTraces) and of the OpAMP snapshot
// request path. Run with:
//
//	go test -run=^$ -bench=. -benchmem ./...
//
// In addition to the standard -benchmem metrics (B/op, allocs/op), each
// benchmark reports:
//
//	gc-cycles/op   - GC cycles triggered per operation
//	gc-pause-ns/op - total stop-the-world pause time incurred per operation
//
// Use benchstat over -count=10 runs when comparing changes.

// benchGCMetrics runs loop (which must iterate b.N times itself) and reports
// GC activity attributable to it.
func benchGCMetrics(b *testing.B, loop func()) {
	b.Helper()
	b.ReportAllocs()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	b.ResetTimer()
	loop()
	b.StopTimer()

	runtime.ReadMemStats(&after)
	b.ReportMetric(float64(after.NumGC-before.NumGC)/float64(b.N), "gc-cycles/op")
	b.ReportMetric(float64(after.PauseTotalNs-before.PauseTotalNs)/float64(b.N), "gc-pause-ns/op")
}

// benchProcessor constructs a processor directly, bypassing the factory so the
// shared processors map is not involved and no OpAMP extension is required.
// It uses the default configuration, so once the buffer is full the steady
// state measured is the default rate-limited admission path.
func benchProcessor(enabled bool) *snapshotProcessor {
	cfg := createDefaultConfig().(*Config)
	cfg.Enabled = enabled
	return newSnapshotProcessor(zap.NewNop(), cfg, component.MustNewID("snapshotprocessor"))
}

// benchText returns n bytes of deterministic, moderately compressible text.
func benchText(n int) string {
	const words = "error warning request latency connection timeout retry succeeded failed processing batch queue export "
	rng := rand.New(rand.NewPCG(17, 42))
	buf := make([]byte, 0, n+16)
	for len(buf) < n {
		start := rng.IntN(len(words) - 8)
		buf = append(buf, words[start:start+8]...)
	}
	return string(buf[:n])
}

// benchLogs generates a single-resource, single-scope batch with the given
// number of records, attributes per record, and body size in bytes.
func benchLogs(records, attrs, bodyBytes int) plog.Logs {
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	resAttrs := rl.Resource().Attributes()
	resAttrs.PutStr("service.name", "bench-service")
	resAttrs.PutStr("host.name", "bench-host-01")
	resAttrs.PutStr("os.type", "linux")

	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("bench-scope")

	body := benchText(bodyBytes)
	now := pcommon.NewTimestampFromTime(time.Now())

	lrs := sl.LogRecords()
	lrs.EnsureCapacity(records)
	for i := 0; i < records; i++ {
		lr := lrs.AppendEmpty()
		lr.SetTimestamp(now)
		lr.SetObservedTimestamp(now)
		lr.SetSeverityNumber(plog.SeverityNumberInfo)
		lr.SetSeverityText("INFO")
		lr.Body().SetStr(strconv.Itoa(i) + " " + body)

		m := lr.Attributes()
		m.EnsureCapacity(attrs)
		for j := 0; j < attrs; j++ {
			switch j % 3 {
			case 0:
				m.PutStr("attr.str."+strconv.Itoa(j), "value-"+strconv.Itoa(i%50))
			case 1:
				m.PutInt("attr.int."+strconv.Itoa(j), int64(i*j))
			case 2:
				m.PutBool("attr.bool."+strconv.Itoa(j), i%2 == 0)
			}
		}
	}
	return ld
}

// benchMetrics generates a batch with the given total number of data points,
// spread across gauge and sum metrics with 10 data points each.
func benchMetrics(dataPoints, attrs int) pmetric.Metrics {
	const dpsPerMetric = 10

	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	resAttrs := rm.Resource().Attributes()
	resAttrs.PutStr("service.name", "bench-service")
	resAttrs.PutStr("host.name", "bench-host-01")

	sm := rm.ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("bench-scope")

	now := pcommon.NewTimestampFromTime(time.Now())

	remaining := dataPoints
	for mi := 0; remaining > 0; mi++ {
		metric := sm.Metrics().AppendEmpty()
		metric.SetName("bench.metric." + strconv.Itoa(mi))
		metric.SetUnit("1")

		var dps pmetric.NumberDataPointSlice
		if mi%2 == 0 {
			dps = metric.SetEmptyGauge().DataPoints()
		} else {
			sum := metric.SetEmptySum()
			sum.SetIsMonotonic(true)
			sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
			dps = sum.DataPoints()
		}

		n := min(dpsPerMetric, remaining)
		dps.EnsureCapacity(n)
		for di := 0; di < n; di++ {
			dp := dps.AppendEmpty()
			dp.SetTimestamp(now)
			dp.SetDoubleValue(float64(mi*dpsPerMetric + di))
			m := dp.Attributes()
			m.EnsureCapacity(attrs)
			for j := 0; j < attrs; j++ {
				m.PutStr("attr."+strconv.Itoa(j), "value-"+strconv.Itoa(di))
			}
		}
		remaining -= n
	}
	return md
}

// benchTraces generates a single-resource, single-scope batch with the given
// number of spans and attributes per span.
func benchTraces(spans, attrs int) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	resAttrs := rs.Resource().Attributes()
	resAttrs.PutStr("service.name", "bench-service")
	resAttrs.PutStr("host.name", "bench-host-01")

	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("bench-scope")

	start := pcommon.NewTimestampFromTime(time.Now())
	end := pcommon.NewTimestampFromTime(time.Now().Add(50 * time.Millisecond))

	sps := ss.Spans()
	sps.EnsureCapacity(spans)
	for i := 0; i < spans; i++ {
		span := sps.AppendEmpty()
		span.SetName("bench-span-" + strconv.Itoa(i%20))
		span.SetKind(ptrace.SpanKindServer)
		span.SetStartTimestamp(start)
		span.SetEndTimestamp(end)
		span.SetTraceID([16]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
		span.SetSpanID([8]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8})

		m := span.Attributes()
		m.EnsureCapacity(attrs)
		for j := 0; j < attrs; j++ {
			m.PutStr("attr."+strconv.Itoa(j), "value-"+strconv.Itoa(i%50))
		}
	}
	return td
}

// BenchmarkProcessLogs measures the per-batch hot-path cost across batch
// sizes. The snapshot buffer retains ~100 records, so cost above that is pure
// overhead.
func BenchmarkProcessLogs(b *testing.B) {
	for _, records := range []int{1, 10, 100, 1_000, 10_000} {
		b.Run("records="+strconv.Itoa(records), func(b *testing.B) {
			sp := benchProcessor(true)
			ld := benchLogs(records, 10, 256)
			ctx := context.Background()

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					if _, err := sp.processLogs(ctx, ld); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkProcessLogs_BodySize measures how per-record size affects the
// hot-path cost at a fixed batch size.
func BenchmarkProcessLogs_BodySize(b *testing.B) {
	for _, bodyBytes := range []int{64, 512, 4_096} {
		b.Run("body="+strconv.Itoa(bodyBytes)+"B", func(b *testing.B) {
			sp := benchProcessor(true)
			ld := benchLogs(1_000, 10, bodyBytes)
			ctx := context.Background()

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					if _, err := sp.processLogs(ctx, ld); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkProcessLogs_AttributeCount isolates the per-attribute copy cost,
// which allocates per value in pdata's CopyTo path.
func BenchmarkProcessLogs_AttributeCount(b *testing.B) {
	for _, attrs := range []int{0, 10, 50} {
		b.Run("attrs="+strconv.Itoa(attrs), func(b *testing.B) {
			sp := benchProcessor(true)
			ld := benchLogs(1_000, attrs, 256)
			ctx := context.Background()

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					if _, err := sp.processLogs(ctx, ld); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkProcessLogs_AdmitEveryBatch measures the per-batch cost with rate
// limiting disabled (refresh_interval: 0), so every batch pays the bounded
// buffer copy.
func BenchmarkProcessLogs_AdmitEveryBatch(b *testing.B) {
	cfg := createDefaultConfig().(*Config)
	cfg.RefreshInterval = 0
	sp := newSnapshotProcessor(zap.NewNop(), cfg, component.MustNewID("snapshotprocessor"))
	ld := benchLogs(1_000, 10, 256)
	ctx := context.Background()

	benchGCMetrics(b, func() {
		for i := 0; i < b.N; i++ {
			if _, err := sp.processLogs(ctx, ld); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkProcessLogs_Disabled measures the cost of a batch passing through a
// disabled processor. This should be near zero.
func BenchmarkProcessLogs_Disabled(b *testing.B) {
	sp := benchProcessor(false)
	ld := benchLogs(1_000, 10, 256)
	ctx := context.Background()

	benchGCMetrics(b, func() {
		for i := 0; i < b.N; i++ {
			if _, err := sp.processLogs(ctx, ld); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkProcessLogs_Parallel measures contention on the shared buffer
// mutex when multiple pipelines/consumers push batches through the same
// processor instance concurrently.
func BenchmarkProcessLogs_Parallel(b *testing.B) {
	sp := benchProcessor(true)
	ld := benchLogs(100, 10, 256)
	ctx := context.Background()

	benchGCMetrics(b, func() {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := sp.processLogs(ctx, ld); err != nil {
					// b.Fatal must not be called from RunParallel goroutines
					b.Error(err)
					return
				}
			}
		})
	})
}

// BenchmarkProcessMetrics measures the per-batch hot-path cost across data
// point counts. Note that the metrics buffer's size accounting
// (DataPointCount) walks every metric, so cost scales with metric count as
// well as data point count.
func BenchmarkProcessMetrics(b *testing.B) {
	for _, dataPoints := range []int{1, 10, 100, 1_000, 10_000} {
		b.Run("datapoints="+strconv.Itoa(dataPoints), func(b *testing.B) {
			sp := benchProcessor(true)
			md := benchMetrics(dataPoints, 5)
			ctx := context.Background()

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					if _, err := sp.processMetrics(ctx, md); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkProcessTraces measures the per-batch hot-path cost across span
// counts.
func BenchmarkProcessTraces(b *testing.B) {
	for _, spans := range []int{1, 10, 100, 1_000, 10_000} {
		b.Run("spans="+strconv.Itoa(spans), func(b *testing.B) {
			sp := benchProcessor(true)
			td := benchTraces(spans, 10)
			ctx := context.Background()

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					if _, err := sp.processTraces(ctx, td); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// benchCapabilityHandler is a no-op CustomCapabilityHandler so the snapshot
// request path can be benchmarked without an OpAMP connection.
type benchCapabilityHandler struct{}

func (benchCapabilityHandler) Message() <-chan *protobufs.CustomMessage { return nil }

func (benchCapabilityHandler) SendMessage(string, []byte) (chan struct{}, error) {
	return nil, nil
}

func (benchCapabilityHandler) Unregister() {}

// BenchmarkSnapshotRequest measures the full snapshot request path: YAML
// request parsing, payload construction (filter, sample, JSON marshal, gzip
// size checks), report marshaling, and final gzip compression.
//
// This path also holds the buffer mutex during payload construction, so its
// duration is a direct measure of how long pipelines are stalled per request.
func BenchmarkSnapshotRequest(b *testing.B) {
	cases := []struct {
		name  string
		query string
	}{
		{name: "unfiltered"},
		// Matches every record via the resource attributes.
		{name: "matching_query", query: "bench-service"},
		// Matches nothing: worst case, scans every attribute of every record.
		{name: "nonmatching_query", query: "no-such-token-anywhere"},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			sp := benchProcessor(true)
			sp.customCapabilityHandler = benchCapabilityHandler{}
			ctx := context.Background()

			// Fill the buffer to its ideal size (100 records).
			if _, err := sp.processLogs(ctx, benchLogs(100, 10, 256)); err != nil {
				b.Fatal(err)
			}

			reqPayload := fmt.Sprintf(`{"processor":%q,"pipeline_type":"logs","session_id":"bench","maximum_payload_size":10485760}`, sp.processorID)
			if tc.query != "" {
				reqPayload = fmt.Sprintf(`{"processor":%q,"pipeline_type":"logs","session_id":"bench","search_query":%q,"maximum_payload_size":10485760}`, sp.processorID, tc.query)
			}
			cm := &protobufs.CustomMessage{
				Capability: snapshotCapability,
				Type:       snapshotRequestType,
				Data:       []byte(reqPayload),
			}

			// Warm up once so the buffer reaches its steady post-request
			// state (a single merged payload).
			sp.processSnapshotRequest(cm)

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					sp.processSnapshotRequest(cm)
				}
			})
		})
	}
}
