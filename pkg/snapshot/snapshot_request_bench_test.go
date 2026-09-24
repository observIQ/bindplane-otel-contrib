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

package snapshot

import (
	"runtime"
	"strconv"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

// These benchmarks measure what a snapshot request costs and returns while a
// pipeline is flowing, and what a buffer holds in memory between requests.
// They complement the hot-path benchmarks: a buffer can be cheap per batch and
// still return stale data or pin memory. Run with:
//
//	go test -run=^$ -bench='Freshness|RetainedHeap' -benchmem ./...
//
// Reported metrics:
//
//	age-ms/op       - mean age of the newest record in the returned payload,
//	                  measured at the moment the request completes
//	records/op      - records in the returned payload
//	retained-KB/op  - live heap held by one full buffer at rest

// benchProducer feeds buf at a fixed record rate from a goroutine until stop
// is closed. Every record carries the wall-clock ObservedTimestamp of the
// batch it was produced in, so payload freshness can be measured.
func benchProducer(buf *LogBuffer, batch int, recordsPerSecond int, stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	interval := time.Duration(float64(time.Second) * float64(batch) / float64(recordsPerSecond))
	ld := benchLogs(batch, 10, 256)
	lrs := ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				ts := pcommon.NewTimestampFromTime(now)
				for i := 0; i < lrs.Len(); i++ {
					lrs.At(i).SetObservedTimestamp(ts)
				}
				buf.Add(ld)
			}
		}
	}()
	return done
}

// newestObserved returns the latest ObservedTimestamp in a marshaled payload
// and the number of records it holds.
func newestObserved(b *testing.B, payload []byte) (time.Time, int) {
	b.Helper()
	ld, err := (&plog.ProtoUnmarshaler{}).UnmarshalLogs(payload)
	if err != nil {
		b.Fatal(err)
	}
	var newest pcommon.Timestamp
	count := 0
	rls := ld.ResourceLogs()
	for ri := 0; ri < rls.Len(); ri++ {
		sls := rls.At(ri).ScopeLogs()
		for si := 0; si < sls.Len(); si++ {
			lrs := sls.At(si).LogRecords()
			for li := 0; li < lrs.Len(); li++ {
				count++
				if ts := lrs.At(li).ObservedTimestamp(); ts > newest {
					newest = ts
				}
			}
		}
	}
	return newest.AsTime(), count
}

// BenchmarkLogBufferFreshness answers "how old is the data a snapshot request
// returns, and how long does the request take" while a pipeline is flowing at
// a given record rate with 100-record batches. Requests are issued back to
// back, which is harsher than Bindplane's one-per-second polling.
func BenchmarkLogBufferFreshness(b *testing.B) {
	for _, rate := range []int{200, 2_000, 20_000} {
		b.Run("rate="+strconv.Itoa(rate), func(b *testing.B) {
			buf := NewLogBuffer(benchIdealSize)
			stop := make(chan struct{})
			done := benchProducer(buf, 100, rate, stop)
			// Let the buffer reach its steady state before measuring: a fast
			// pipeline needs a few full windows to switch to on-demand mode.
			time.Sleep(3500 * time.Millisecond)

			var totalAge time.Duration
			var totalRecords int
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				payload, err := buf.ConstructPayload(&plog.ProtoMarshaler{}, nil, nil, 10*1024*1024)
				if err != nil {
					b.Fatal(err)
				}
				requestDone := time.Now()
				b.StopTimer()
				newest, n := newestObserved(b, payload)
				if n > 0 {
					totalAge += requestDone.Sub(newest)
				}
				totalRecords += n
				b.StartTimer()
			}
			b.StopTimer()
			close(stop)
			<-done

			b.ReportMetric(float64(totalAge.Milliseconds())/float64(b.N), "age-ms/op")
			b.ReportMetric(float64(totalRecords)/float64(b.N), "records/op")
		})
	}
}

// BenchmarkLogBufferRetainedHeap reports the live heap one buffer holds at
// rest. "continuous" fills a buffer to its ideal size with 256-byte bodies and
// ten attributes per record; "on_demand" is the same buffer after it switched
// to on-demand mode, which keeps the store as the last known snapshot. Every
// Bindplane pipeline carries three such buffers per signal.
func BenchmarkLogBufferRetainedHeap(b *testing.B) {
	ld := benchLogs(benchIdealSize, 10, 256)
	modes := []struct {
		name string
		make func() *LogBuffer
	}{
		{name: "continuous", make: func() *LogBuffer {
			buf := NewLogBuffer(benchIdealSize, WithRefreshInterval(0))
			buf.Add(ld)
			return buf
		}},
		{name: "on_demand", make: func() *LogBuffer {
			buf := NewLogBuffer(benchIdealSize)
			buf.Add(ld)
			forceOnDemand(b, buf)
			buf.Add(ld) // ignored: idle on-demand buffers collect nothing
			return buf
		}},
	}
	for _, mode := range modes {
		b.Run(mode.name, func(b *testing.B) {
			buffers := make([]*LogBuffer, 0, b.N)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buffers = append(buffers, mode.make())
			}
			b.StopTimer()

			runtime.GC()
			runtime.ReadMemStats(&after)
			retained := float64(after.HeapAlloc) - float64(before.HeapAlloc)
			b.ReportMetric(retained/1024/float64(b.N), "retained-KB/op")
			runtime.KeepAlive(buffers)
		})
	}
}
