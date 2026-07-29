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
	"math/rand/v2"
	"runtime"
	"strconv"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// These benchmarks measure the buffer hot path (Add) and the snapshot request
// path (ConstructPayload, compress) in isolation from the processor. Run with:
//
//	go test -run=^$ -bench=. -benchmem ./...
//
// In addition to the standard -benchmem metrics, each benchmark reports:
//
//	gc-cycles/op   - GC cycles triggered per operation
//	gc-pause-ns/op - total stop-the-world pause time incurred per operation

const benchIdealSize = 100

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

// benchLogs generates a single-resource, single-scope payload with the given
// number of records, attributes per record, and body size in bytes.
func benchLogs(records, attrs, bodyBytes int) plog.Logs {
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	resAttrs := rl.Resource().Attributes()
	resAttrs.PutStr("service.name", "bench-service")
	resAttrs.PutStr("host.name", "bench-host-01")

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
			m.PutStr("attr."+strconv.Itoa(j), "value-"+strconv.Itoa(i%50))
		}
	}
	return ld
}

// benchMetrics generates a payload with the given total number of data
// points, spread across gauge and sum metrics with 10 data points each. The
// metric count matters: the buffer's size accounting (DataPointCount) walks
// and type-switches on every metric.
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

// benchTraces generates a single-resource, single-scope payload with the
// given number of spans and attributes per span.
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

// BenchmarkLogBufferAdd measures the buffer admission cost across payload
// sizes. Payloads below the ideal size exercise the eviction loop and its
// repeated Len() walks; payloads above it exercise the buffer-reset path.
func BenchmarkLogBufferAdd(b *testing.B) {
	for _, records := range []int{10, 100, 1_000, 10_000} {
		b.Run("records="+strconv.Itoa(records), func(b *testing.B) {
			buf := NewLogBuffer(benchIdealSize)
			ld := benchLogs(records, 10, 256)

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					buf.Add(ld)
				}
			})
		})
	}
}

// BenchmarkMetricBufferAdd measures the buffer admission cost across payload
// sizes. Size accounting uses DataPointCount, which walks and type-switches
// on every metric in every buffered payload.
func BenchmarkMetricBufferAdd(b *testing.B) {
	for _, dataPoints := range []int{10, 100, 1_000, 10_000} {
		b.Run("datapoints="+strconv.Itoa(dataPoints), func(b *testing.B) {
			buf := NewMetricBuffer(benchIdealSize)
			md := benchMetrics(dataPoints, 5)

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					buf.Add(md)
				}
			})
		})
	}
}

// BenchmarkTraceBufferAdd measures the buffer admission cost across payload
// sizes.
func BenchmarkTraceBufferAdd(b *testing.B) {
	for _, spans := range []int{10, 100, 1_000, 10_000} {
		b.Run("spans="+strconv.Itoa(spans), func(b *testing.B) {
			buf := NewTraceBuffer(benchIdealSize)
			td := benchTraces(spans, 10)

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					buf.Add(td)
				}
			})
		})
	}
}

// seededLogBuffer returns a LogBuffer filled to its ideal size with distinct
// payloads. Distinct payloads are required because ConstructPayload drains
// buffered payloads via MoveAndAppendTo; sharing one payload across entries
// would leave later entries empty.
func seededLogBuffer(idealSize int) *LogBuffer {
	buf := NewLogBuffer(idealSize)
	const perAdd = 10
	for i := 0; i < idealSize/perAdd; i++ {
		buf.Add(benchLogs(perAdd, 10, 256))
	}
	return buf
}

// BenchmarkLogBufferConstructPayload measures the snapshot request path:
// merge, filter, sample, JSON marshal, and gzip size checks. The buffer mutex
// is held for the full duration, so this is also the length of the pipeline
// stall a snapshot request causes.
func BenchmarkLogBufferConstructPayload(b *testing.B) {
	oneHourAgo := time.Now().Add(-time.Hour)

	cases := []struct {
		name             string
		searchQuery      *string
		minimumTimestamp *time.Time
		maxPayloadSize   int
	}{
		{
			name:           "unfiltered",
			maxPayloadSize: 10 * 1024 * 1024,
		},
		{
			// Matches every record via the resource attributes.
			name:           "matching_query",
			searchQuery:    asPtr("bench-service"),
			maxPayloadSize: 10 * 1024 * 1024,
		},
		{
			// Matches nothing: worst case, scans every attribute and body of
			// every record.
			name:           "nonmatching_query",
			searchQuery:    asPtr("no-such-token-anywhere"),
			maxPayloadSize: 10 * 1024 * 1024,
		},
		{
			// All records pass the timestamp filter, so every record is
			// copied by the filter pass.
			name:             "min_timestamp",
			minimumTimestamp: &oneHourAgo,
			maxPayloadSize:   10 * 1024 * 1024,
		},
		{
			// A limit small enough that full retention does not fit,
			// forcing the sampling/retry loop.
			name:           "sampled_tight_limit",
			maxPayloadSize: 1024,
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			buf := seededLogBuffer(benchIdealSize)
			marshaler := &plog.JSONMarshaler{}

			// Warm up once so the buffer reaches its steady post-request
			// state (a single merged payload).
			if _, err := buf.ConstructPayload(marshaler, tc.searchQuery, tc.minimumTimestamp, tc.maxPayloadSize); err != nil {
				b.Fatal(err)
			}

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					if _, err := buf.ConstructPayload(marshaler, tc.searchQuery, tc.minimumTimestamp, tc.maxPayloadSize); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkCompress measures the gzip compression cost, including the
// per-call allocation of a new gzip writer (~700KiB of compressor state).
func BenchmarkCompress(b *testing.B) {
	// Realistic input: OTLP/JSON-marshaled log payloads, tiled to size.
	marshaled, err := (&plog.JSONMarshaler{}).MarshalLogs(benchLogs(100, 10, 256))
	if err != nil {
		b.Fatal(err)
	}

	for _, size := range []int{4 * 1024, 256 * 1024, 1024 * 1024, 10 * 1024 * 1024} {
		b.Run("size="+strconv.Itoa(size/1024)+"KiB", func(b *testing.B) {
			data := make([]byte, 0, size)
			for len(data) < size {
				data = append(data, marshaled...)
			}
			data = data[:size]

			benchGCMetrics(b, func() {
				for i := 0; i < b.N; i++ {
					if _, err := Compress(data); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
