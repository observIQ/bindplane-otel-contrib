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

package throughputmeasurementprocessor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processortest"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// The benchmark drives the processor through the public factory only.
// BenchmarkProcessor keeps the names and the default config of the before side,
// so one benchmark measures both sides. See docs/superpowers/specs/2026-09-15-throughput-count-on-success-design.md.

// benchConsumers holds the two next-consumer outcomes. The accepting consumer
// returns nil on every call. The rejecting consumer returns an error on every call.
var benchConsumers = []struct {
	name string
	next consumertest.Consumer
}{
	{name: "accepted", next: consumertest.NewNop()},
	{name: "rejected", next: consumertest.NewErr(errors.New("rejected"))},
}

// benchProcessorSeq gives each benchmark processor a unique component ID, so
// the package-level processor registry never hands back a shared instance.
var benchProcessorSeq atomic.Int64

// benchSettings returns processor settings with a real OTel SDK meter provider.
// Counter adds then cost what they cost in a collector. The reader is never
// collected; the SDK still aggregates on every add.
func benchSettings(b *testing.B) processor.Settings {
	b.Helper()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	b.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	set := processortest.NewNopSettings(componentType)
	set.ID = component.NewIDWithName(componentType, fmt.Sprintf("bench%d", benchProcessorSeq.Add(1)))
	set.TelemetrySettings.MeterProvider = mp
	return set
}

// benchConfig enables the processor, samples every payload, and sets no
// OpAMP extension, so the benchmark needs no host and no Start call.
func benchConfig(countOnDelivery bool) *Config {
	return &Config{Enabled: true, SamplingRatio: 1, CountOnDelivery: countOnDelivery}
}

// syntheticLogs builds one resource and one scope with n log records.
func syntheticLogs(n int) plog.Logs {
	logs := plog.NewLogs()
	sl := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	for i := 0; i < n; i++ {
		lr := sl.LogRecords().AppendEmpty()
		lr.Body().SetStr("benchmark log message with a body of moderate length")
		lr.Attributes().PutInt("index", int64(i))
	}
	return logs
}

// BenchmarkProcessor measures one ConsumeX call per iteration for each signal,
// payload, and next-consumer outcome, with count_on_delivery off (the default).
// Sub-benchmark names are stable so that benchstat pairs the before and after rows.
func BenchmarkProcessor(b *testing.B) {
	benchmarkSignals(b, false)
}

// BenchmarkProcessorCountOnDelivery is BenchmarkProcessor with count_on_delivery
// on. Its sub-benchmark names are the same, so benchstat can pair its rows with
// BenchmarkProcessor rows after a rename of the top-level name.
func BenchmarkProcessorCountOnDelivery(b *testing.B) {
	benchmarkSignals(b, true)
	b.Run("logs/fanout", benchmarkLogsFanout)
}

func benchmarkSignals(b *testing.B, countOnDelivery bool) {
	b.Run("logs", func(b *testing.B) { benchmarkLogs(b, countOnDelivery) })
	b.Run("metrics", func(b *testing.B) { benchmarkMetrics(b, countOnDelivery) })
	b.Run("traces", func(b *testing.B) { benchmarkTraces(b, countOnDelivery) })
}

// benchmarkLogsFanout measures a source processor above a fanout with one
// rejecting branch and one branch that has its own processor and accepts.
func benchmarkLogsFanout(b *testing.B) {
	goldenLogs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	branch, err := NewFactory().CreateLogs(ctx, benchSettings(b), benchConfig(true), consumertest.NewNop())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = branch.Shutdown(ctx) })

	rejecting := consumertest.NewErr(errors.New("rejected"))
	fanout, err := consumer.NewLogs(func(ctx context.Context, ld plog.Logs) error {
		return errors.Join(rejecting.ConsumeLogs(ctx, ld), branch.ConsumeLogs(ctx, ld))
	})
	if err != nil {
		b.Fatal(err)
	}

	source, err := NewFactory().CreateLogs(ctx, benchSettings(b), benchConfig(true), fanout)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = source.Shutdown(ctx) })

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = source.ConsumeLogs(ctx, goldenLogs)
	}
}

func benchmarkLogs(b *testing.B, countOnDelivery bool) {
	goldenLogs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	if err != nil {
		b.Fatal(err)
	}

	payloads := []struct {
		name string
		logs plog.Logs
	}{
		{name: "golden", logs: goldenLogs},
		{name: "100", logs: syntheticLogs(100)},
		{name: "1000", logs: syntheticLogs(1000)},
		{name: "10000", logs: syntheticLogs(10000)},
	}

	for _, p := range payloads {
		for _, c := range benchConsumers {
			b.Run(p.name+"/"+c.name, func(b *testing.B) {
				ctx := context.Background()
				proc, err := NewFactory().CreateLogs(ctx, benchSettings(b), benchConfig(countOnDelivery), c.next)
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { _ = proc.Shutdown(ctx) })

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = proc.ConsumeLogs(ctx, p.logs)
				}
			})
		}
	}
}

func benchmarkMetrics(b *testing.B, countOnDelivery bool) {
	goldenMetrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	if err != nil {
		b.Fatal(err)
	}

	for _, c := range benchConsumers {
		b.Run("golden/"+c.name, func(b *testing.B) {
			ctx := context.Background()
			proc, err := NewFactory().CreateMetrics(ctx, benchSettings(b), benchConfig(countOnDelivery), c.next)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = proc.Shutdown(ctx) })

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = proc.ConsumeMetrics(ctx, goldenMetrics)
			}
		})
	}
}

func benchmarkTraces(b *testing.B, countOnDelivery bool) {
	goldenTraces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	if err != nil {
		b.Fatal(err)
	}

	for _, c := range benchConsumers {
		b.Run("golden/"+c.name, func(b *testing.B) {
			ctx := context.Background()
			proc, err := NewFactory().CreateTraces(ctx, benchSettings(b), benchConfig(countOnDelivery), c.next)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = proc.Shutdown(ctx) })

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = proc.ConsumeTraces(ctx, goldenTraces)
			}
		})
	}
}
