# Throughput Count-On-Success Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The throughput measurement processor records a payload only when the next consumer accepts it, and records refused payloads into new `_rejected` counters.

**Architecture:** `pkg/measurements` splits each `Add*` into a measure half that returns a `Measurement` value and a record half that adds it to counters, and gains rejected counters plus `RecordRejected*` methods. The processor stops measuring in its processorhelper process function and instead wraps the real next consumer: measure, forward, then record by outcome.

**Tech Stack:** Go, OpenTelemetry Collector `processorhelper` and `consumer` packages, OTel Go metrics SDK, testify, golden test data.

**Spec:** `docs/superpowers/specs/2026-09-15-throughput-count-on-success-design.md`

## Global Constraints

- Repo: `~/git/bindplane-otel-contrib.worktrees/briangardner/bpop-5831-throughput-count-on-success` (a gh stack; base branch `briangardner/bpop-5831-count-throughput-bytes-only-after-successful-exporter` holds the spec).
- Two stacked branches: `briangardner/bpop-5831-measurements-record-split` (Tasks 1 to 3) then `briangardner/bpop-5831-processor-count-on-success` (Tasks 4 to 7). Create each with `gh stack add <name>` from the worktree.
- Commit format: Conventional Commits with trailer `Assisted-by: Claude Fable 5.1`. Never `Co-authored-by`. Use plain `git commit` on the stack branch.
- Every new `.go` file starts with the observIQ Apache-2.0 license header copied from `pkg/measurements/throughput.go` lines 1 to 13.
- Behavior is always on. No new config field.
- Rejected counters are internal telemetry only. `OTLPThroughputMeasurements` and `OTLPMeasurements` must not emit them.
- The sequence number advances only on delivered records.
- `AddLogs`, `AddMetrics`, `AddTraces` keep their signatures and observable behavior.
- The processor module resolves `pkg/measurements` through a `replace` directive to `../../pkg/measurements`, so no version bump is needed between the branches.
- Run tests per module: `cd pkg/measurements && go test ./...` and `cd processor/throughputmeasurementprocessor && go test ./...`. Run `gofmt -l .` in each module before every commit.
- Benchmarks: the spec branch holds `processor_benchmark_test.go` and the before numbers. The benchmark uses only the public factory API, so it must compile unchanged on every branch of the stack. Run it with `go test -run '^$' -bench BenchmarkProcessor -benchmem -count=10 ./...` from the processor module and compare sides with `go run golang.org/x/perf/cmd/benchstat@latest before.txt after.txt`. Raw output files live in the scratchpad, not the repo.

---

## File Structure

| Path | Responsibility |
| --- | --- |
| `pkg/measurements/measurement.go` (create) | `Measurement` value type and the pure `MeasureLogs`, `MeasureMetrics`, `MeasureTraces` functions. No counters. |
| `pkg/measurements/measurement_test.go` (create) | Tests for the measure functions. |
| `pkg/measurements/throughput.go` (modify) | Rejected counters, `Record*` and `RecordRejected*` methods, `Add*` re-expressed as measure then record, updated descriptions, counter construction helper. |
| `pkg/measurements/throughput_test.go` (modify) | Tests for record paths, sequence number rule, registry exclusion of rejected traffic. |
| `processor/throughputmeasurementprocessor/consumer.go` (create) | Per-signal consumer wrappers: sample, measure, forward, record by outcome. |
| `processor/throughputmeasurementprocessor/consumer_test.go` (create) | Tests for the wrappers: rejected, moved payload, disabled, zero sampling. |
| `processor/throughputmeasurementprocessor/processor.go` (modify) | Remove `processLogs`/`processMetrics`/`processTraces`; add `sample()`. |
| `processor/throughputmeasurementprocessor/factory.go` (modify) | Pass-through process functions; wrap `nextConsumer` before handing it to processorhelper. |
| `processor/throughputmeasurementprocessor/processor_test.go` (modify) | Route existing tests through the wrappers; add rejected-only OpAMP report test. |
| `processor/throughputmeasurementprocessor/README.md` (modify) | Document new semantics and rejected counters. |
| `processor/throughputmeasurementprocessor/processor_benchmark_test.go` (create, spec branch) | Factory-driven benchmark: 3 signals, golden and synthetic payloads, accepting and rejecting consumer. Compiles before and after the change. |
| `docs/superpowers/specs/2026-09-15-throughput-count-on-success-benchmarks.md` (create on spec branch, modify on processor branch) | Environment, commands, before table, after table, benchstat delta. |

---

## Branch 0: `briangardner/bpop-5831-count-throughput-bytes-only-after-successful-exporter` (spec branch)

This branch is `main` plus the spec, the plan, the benchmark, and the before numbers. It has no code change, so it is the before side of the benchmark.

### Task 0: Benchmark harness and before run

**Files:**
- Create: `processor/throughputmeasurementprocessor/processor_benchmark_test.go`
- Create: `docs/superpowers/specs/2026-09-15-throughput-count-on-success-benchmarks.md`

- [ ] **Step 1: Check out the spec branch**

```bash
cd ~/git/bindplane-otel-contrib.worktrees/briangardner/bpop-5831-throughput-count-on-success
git checkout briangardner/bpop-5831-count-throughput-bytes-only-after-successful-exporter
```

- [ ] **Step 2: Write the benchmark**

The benchmark builds the processor with `NewFactory()` and nop settings, swaps in a real SDK meter provider, and consumes one payload per iteration. It uses no unexported symbol that changes between branches. Write `processor/throughputmeasurementprocessor/processor_benchmark_test.go`:

```go
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
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processortest"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// The benchmark drives the processor through the public factory only. The same
// file compiles before and after the count-on-success change, so one benchmark
// measures both sides. See docs/superpowers/specs/2026-09-15-throughput-count-on-success-design.md.

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
func benchConfig() *Config {
	return &Config{Enabled: true, SamplingRatio: 1}
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
// payload, and next-consumer outcome. Sub-benchmark names are stable so that
// benchstat pairs the before and after rows.
func BenchmarkProcessor(b *testing.B) {
	b.Run("logs", benchmarkLogs)
	b.Run("metrics", benchmarkMetrics)
	b.Run("traces", benchmarkTraces)
}

func benchmarkLogs(b *testing.B) {
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
				proc, err := NewFactory().CreateLogs(ctx, benchSettings(b), benchConfig(), c.next)
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

func benchmarkMetrics(b *testing.B) {
	goldenMetrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	if err != nil {
		b.Fatal(err)
	}

	for _, c := range benchConsumers {
		b.Run("golden/"+c.name, func(b *testing.B) {
			ctx := context.Background()
			proc, err := NewFactory().CreateMetrics(ctx, benchSettings(b), benchConfig(), c.next)
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

func benchmarkTraces(b *testing.B) {
	goldenTraces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	if err != nil {
		b.Fatal(err)
	}

	for _, c := range benchConsumers {
		b.Run("golden/"+c.name, func(b *testing.B) {
			ctx := context.Background()
			proc, err := NewFactory().CreateTraces(ctx, benchSettings(b), benchConfig(), c.next)
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
```

- [ ] **Step 3: Compile and smoke-run every cell once**

```bash
cd processor/throughputmeasurementprocessor
gofmt -l .
go vet ./...
go test -run '^$' -bench 'BenchmarkProcessor' -benchtime=1x -benchmem ./...
```
Expected: no gofmt output, twelve `BenchmarkProcessor/...` lines, `ok`.

- [ ] **Step 4: Confirm the file compiles on the top of the stack**

```bash
SCR=$(mktemp -d)
git worktree add "$SCR/after" briangardner/bpop-5831-processor-count-on-success
cp processor_benchmark_test.go "$SCR/after/processor/throughputmeasurementprocessor/"
(cd "$SCR/after/processor/throughputmeasurementprocessor" && go vet ./... && go test -run '^$' -bench 'BenchmarkProcessor/logs/golden' -benchtime=1x ./...)
git worktree remove --force "$SCR/after"
```
Expected: `ok`. Skip this step when the upper branches do not exist yet; Task 7b compiles the file again on the after side.

- [ ] **Step 5: Run the before benchmarks**

Run with nothing else heavy on the machine. Output goes to a scratch directory.

```bash
SCR=$(mktemp -d)
(cd processor/throughputmeasurementprocessor && go test -run '^$' -bench 'BenchmarkProcessor' -benchmem -count=10 ./... > "$SCR/before-processor.txt")
(cd pkg/measurements && go test -run '^$' -bench 'BenchmarkAddLogsMeasureLogRawBytes' -benchmem -count=10 ./... > "$SCR/before-measurements.txt")
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/before-processor.txt"
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/before-measurements.txt"
```
Expected: two benchstat tables with a `sec/op`, `B/op`, and `allocs/op` column and a confidence interval per row.

- [ ] **Step 6: Write the results document**

Write `docs/superpowers/specs/2026-09-15-throughput-count-on-success-benchmarks.md` with these sections:

- Purpose: one paragraph linking to the spec Performance section.
- Environment: Go version, CPU, core count, OS, and the commit SHA of each side. Leave the after SHA to Task 7b.
- Commands: the exact commands from Step 5 and the benchstat compare command.
- Before: the two benchstat tables from Step 5, pasted as fenced text.
- After: one sentence that states the processor branch adds this section.

- [ ] **Step 7: Commit in two parts**

```bash
gofmt -l processor/throughputmeasurementprocessor
git add processor/throughputmeasurementprocessor/processor_benchmark_test.go
git commit -m "test(throughputmeasurement): benchmark the processor through the factory

Assisted-by: Claude Fable 5.1"
git add docs/superpowers/specs/2026-09-15-throughput-count-on-success-benchmarks.md
git commit -m "docs(throughputmeasurement): record before benchmarks for BPOP-5831

Assisted-by: Claude Fable 5.1"
```

- [ ] **Step 8: Restack the upper branches**

```bash
gh stack rebase --no-trunk
gh stack view
```
Expected: each upper branch has the new spec-branch commits in its history. Resolve conflicts if any, then `gh stack rebase --continue`.

---

## Branch 1: `briangardner/bpop-5831-measurements-record-split`

- [ ] **Step 0: Create the branch**

```bash
cd ~/git/bindplane-otel-contrib.worktrees/briangardner/bpop-5831-throughput-count-on-success
gh stack add briangardner/bpop-5831-measurements-record-split
git branch --show-current
```
Expected: prints `briangardner/bpop-5831-measurements-record-split`.

### Task 1: `Measurement` type and measure functions

**Files:**
- Create: `pkg/measurements/measurement.go`
- Create: `pkg/measurements/measurement_test.go`

**Interfaces:**
- Produces:
  - `type Measurement struct { Size, Count, RawBytes int64; HasRawBytes bool }`
  - `func MeasureLogs(l plog.Logs, measureLogRawBytes bool) Measurement`
  - `func MeasureMetrics(m pmetric.Metrics) Measurement`
  - `func MeasureTraces(t ptrace.Traces) Measurement`

- [ ] **Step 1: Write the failing tests**

Create `pkg/measurements/measurement_test.go`:

```go
// (license header)

package measurements

import (
	"path/filepath"
	"testing"

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"github.com/stretchr/testify/require"
)

func TestMeasureLogs(t *testing.T) {
	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	t.Run("without raw bytes", func(t *testing.T) {
		m := MeasureLogs(logs, false)
		require.Equal(t, Measurement{Size: 3974, Count: 16}, m)
	})

	t.Run("with raw bytes", func(t *testing.T) {
		m := MeasureLogs(logs, true)
		require.Equal(t, Measurement{Size: 3974, Count: 16, RawBytes: 2373, HasRawBytes: true}, m)
	})

	t.Run("raw bytes prefer log.record.original", func(t *testing.T) {
		withOriginal := logs
		rls := withOriginal.ResourceLogs()
		for i := 0; i < rls.Len(); i++ {
			sls := rls.At(i).ScopeLogs()
			for j := 0; j < sls.Len(); j++ {
				lrs := sls.At(j).LogRecords()
				for k := 0; k < lrs.Len(); k++ {
					lrs.At(k).Attributes().PutStr("log.record.original", "12345678901234567890")
				}
			}
		}
		m := MeasureLogs(withOriginal, true)
		require.Equal(t, int64(16*20), m.RawBytes)
		require.True(t, m.HasRawBytes)
	})
}

func TestMeasureMetrics(t *testing.T) {
	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	require.Equal(t, Measurement{Size: 5675, Count: 37}, MeasureMetrics(metrics))
}

func TestMeasureTraces(t *testing.T) {
	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	require.Equal(t, Measurement{Size: 16767, Count: 178}, MeasureTraces(traces))
}
```

Note: the third logs subtest mutates `logs`, so it runs last. The 2373 raw byte value is half of the 4746 that `TestProcessor_Logs_TwoInstancesSameID` asserts for two payloads.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/measurements && go test ./... -run 'TestMeasure' -v`
Expected: build failure, `undefined: MeasureLogs`.

- [ ] **Step 3: Write the implementation**

Create `pkg/measurements/measurement.go`:

```go
// (license header)

package measurements

import (
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Measurement holds the size and count of one payload.
// Take it before the payload is forwarded and record it after the forward
// returns. Downstream components can move data out of the payload, so a
// measurement taken after the forward can be empty.
type Measurement struct {
	// Size is the protobuf-encoded size of the payload in bytes.
	Size int64
	// Count is the number of log records, data points, or spans in the payload.
	Count int64
	// RawBytes is the size of the original log content in bytes.
	// It is meaningful only when HasRawBytes is true.
	RawBytes int64
	// HasRawBytes is true when raw bytes were measured.
	HasRawBytes bool
}

// MeasureLogs measures a logs payload. It records nothing.
func MeasureLogs(l plog.Logs, measureLogRawBytes bool) Measurement {
	sizer := plog.ProtoMarshaler{}
	m := Measurement{
		Size:  int64(sizer.LogsSize(l)),
		Count: int64(l.LogRecordCount()),
	}

	if measureLogRawBytes {
		m.HasRawBytes = true
		m.RawBytes = logRawBytes(l)
	}

	return m
}

// MeasureMetrics measures a metrics payload. It records nothing.
func MeasureMetrics(m pmetric.Metrics) Measurement {
	sizer := pmetric.ProtoMarshaler{}
	return Measurement{
		Size:  int64(sizer.MetricsSize(m)),
		Count: int64(m.DataPointCount()),
	}
}

// MeasureTraces measures a traces payload. It records nothing.
func MeasureTraces(t ptrace.Traces) Measurement {
	sizer := ptrace.ProtoMarshaler{}
	return Measurement{
		Size:  int64(sizer.TracesSize(t)),
		Count: int64(t.SpanCount()),
	}
}

// logRawBytes sums the original content size of every log record.
// It uses the log.record.original attribute when present and the body otherwise.
func logRawBytes(l plog.Logs) int64 {
	total := int64(0)
	resourceLogs := l.ResourceLogs()
	for i := 0; i < resourceLogs.Len(); i++ {
		scopeLogs := resourceLogs.At(i).ScopeLogs()
		for j := 0; j < scopeLogs.Len(); j++ {
			logRecords := scopeLogs.At(j).LogRecords()
			for k := 0; k < logRecords.Len(); k++ {
				logRecord := logRecords.At(k)
				if original, ok := logRecord.Attributes().Get("log.record.original"); ok {
					total += int64(len(original.Str()))
				} else {
					total += int64(len(logRecord.Body().AsString()))
				}
			}
		}
	}
	return total
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd pkg/measurements && go test ./... -run 'TestMeasure' -v`
Expected: PASS for all three tests and subtests.

- [ ] **Step 5: Commit**

```bash
cd pkg/measurements && gofmt -l . && cd -
git add pkg/measurements/measurement.go pkg/measurements/measurement_test.go
git commit -m "feat(measurements): add Measurement type and measure functions

Assisted-by: Claude Fable 5.1"
```

### Task 2: Record paths and rejected counters

**Files:**
- Modify: `pkg/measurements/throughput.go`
- Modify: `pkg/measurements/throughput_test.go`

**Interfaces:**
- Consumes: `Measurement`, `MeasureLogs`, `MeasureMetrics`, `MeasureTraces` from Task 1.
- Produces:
  - `func (tm *ThroughputMeasurements) RecordLogs(ctx context.Context, m Measurement)`
  - `func (tm *ThroughputMeasurements) RecordRejectedLogs(ctx context.Context, m Measurement)`
  - `func (tm *ThroughputMeasurements) RecordMetrics(ctx context.Context, m Measurement)`
  - `func (tm *ThroughputMeasurements) RecordRejectedMetrics(ctx context.Context, m Measurement)`
  - `func (tm *ThroughputMeasurements) RecordTraces(ctx context.Context, m Measurement)`
  - `func (tm *ThroughputMeasurements) RecordRejectedTraces(ctx context.Context, m Measurement)`
  - New counters named `otelcol_processor_throughputmeasurement_<name>_rejected` for `log_data_size`, `log_count`, `log_raw_bytes`, `metric_data_size`, `metric_count`, `trace_data_size`, `trace_count`.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/measurements/throughput_test.go`. Add a helper first, then the tests. Add `"context"` and the metric SDK imports if not already present (they are).

```go
// collectSums reads every Int64 sum from the reader and returns name to value.
// It fails the test if a metric has more than one data point.
func collectSums(t *testing.T, reader *metric.ManualReader) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	sums := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			require.Len(t, sum.DataPoints, 1, m.Name)
			sums[m.Name] = sum.DataPoints[0].Value
		}
	}
	return sums
}

func newTestMeasurements(t *testing.T) (*ThroughputMeasurements, *metric.ManualReader) {
	t.Helper()

	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	tmp, err := NewThroughputMeasurements(mp, "throughputmeasurement/1", map[string]string{})
	require.NoError(t, err)
	return tmp, reader
}

func TestRecordRejectedLogs(t *testing.T) {
	tmp, reader := newTestMeasurements(t)

	tmp.RecordRejectedLogs(context.Background(), Measurement{Size: 3974, Count: 16, RawBytes: 2373, HasRawBytes: true})

	sums := collectSums(t, reader)
	require.Equal(t, int64(3974), sums["otelcol_processor_throughputmeasurement_log_data_size_rejected"])
	require.Equal(t, int64(16), sums["otelcol_processor_throughputmeasurement_log_count_rejected"])
	require.Equal(t, int64(2373), sums["otelcol_processor_throughputmeasurement_log_raw_bytes_rejected"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_data_size")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_count")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_raw_bytes")

	require.Equal(t, int64(0), tmp.LogSize())
	require.Equal(t, int64(0), tmp.LogCount())
	require.Equal(t, int64(0), tmp.SequenceNumber())
}

func TestRecordRejectedMetrics(t *testing.T) {
	tmp, reader := newTestMeasurements(t)

	tmp.RecordRejectedMetrics(context.Background(), Measurement{Size: 5675, Count: 37})

	sums := collectSums(t, reader)
	require.Equal(t, int64(5675), sums["otelcol_processor_throughputmeasurement_metric_data_size_rejected"])
	require.Equal(t, int64(37), sums["otelcol_processor_throughputmeasurement_metric_count_rejected"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_data_size")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_count")
	require.Equal(t, int64(0), tmp.SequenceNumber())
}

func TestRecordRejectedTraces(t *testing.T) {
	tmp, reader := newTestMeasurements(t)

	tmp.RecordRejectedTraces(context.Background(), Measurement{Size: 16767, Count: 178})

	sums := collectSums(t, reader)
	require.Equal(t, int64(16767), sums["otelcol_processor_throughputmeasurement_trace_data_size_rejected"])
	require.Equal(t, int64(178), sums["otelcol_processor_throughputmeasurement_trace_count_rejected"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_data_size")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_count")
	require.Equal(t, int64(0), tmp.SequenceNumber())
}

func TestRecordLogs_SequenceNumber(t *testing.T) {
	tmp, _ := newTestMeasurements(t)

	tmp.RecordLogs(context.Background(), Measurement{Size: 1, Count: 1})
	require.Equal(t, int64(1), tmp.SequenceNumber())

	tmp.RecordRejectedLogs(context.Background(), Measurement{Size: 1, Count: 1})
	require.Equal(t, int64(1), tmp.SequenceNumber(), "rejected records must not advance the sequence number")

	tmp.RecordMetrics(context.Background(), Measurement{Size: 1, Count: 1})
	tmp.RecordTraces(context.Background(), Measurement{Size: 1, Count: 1})
	require.Equal(t, int64(3), tmp.SequenceNumber())
}

func TestRecordLogs_RawBytesFlag(t *testing.T) {
	t.Run("not measured emits no series", func(t *testing.T) {
		tmp, reader := newTestMeasurements(t)
		tmp.RecordLogs(context.Background(), Measurement{Size: 10, Count: 1})
		sums := collectSums(t, reader)
		require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_raw_bytes")
	})

	t.Run("measured zero emits series with zero", func(t *testing.T) {
		tmp, reader := newTestMeasurements(t)
		tmp.RecordLogs(context.Background(), Measurement{Size: 10, Count: 1, HasRawBytes: true})
		sums := collectSums(t, reader)
		require.Contains(t, sums, "otelcol_processor_throughputmeasurement_log_raw_bytes")
		require.Equal(t, int64(0), sums["otelcol_processor_throughputmeasurement_log_raw_bytes"])
	})
}

func TestRecordLogs_MatchesAddLogs(t *testing.T) {
	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	viaAdd, addReader := newTestMeasurements(t)
	viaAdd.AddLogs(context.Background(), logs, true)

	viaRecord, recordReader := newTestMeasurements(t)
	viaRecord.RecordLogs(context.Background(), MeasureLogs(logs, true))

	require.Equal(t, collectSums(t, addReader), collectSums(t, recordReader))
	require.Equal(t, viaAdd.SequenceNumber(), viaRecord.SequenceNumber())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/measurements && go test ./... -run 'TestRecord' -v`
Expected: build failure, `tmp.RecordRejectedLogs undefined`.

- [ ] **Step 3: Write the implementation**

In `pkg/measurements/throughput.go`:

Replace the `ThroughputMeasurements` struct:

```go
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
```

Replace the body of `NewThroughputMeasurements` with a table-driven build. Keep the function signature.

```go
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
```

Replace `AddLogs`, `AddMetrics`, `AddTraces` and add the record methods:

```go
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
```

Delete the old bodies of `AddLogs` (the raw byte loop moved to `logRawBytes` in Task 1), `AddMetrics`, and `AddTraces`. Leave all getters, `int64Counter`, `metricName`, `createMeasurementsAttributeSet`, and the registry code unchanged.

- [ ] **Step 4: Run the whole module**

Run: `cd pkg/measurements && go test ./... -v 2>&1 | grep -E '^(--- |FAIL|ok|PASS)'`
Expected: every test PASS, including the existing golden tests, since the delivered series and their values are unchanged.

- [ ] **Step 5: Commit**

```bash
cd pkg/measurements && gofmt -l . && cd -
git add pkg/measurements/throughput.go pkg/measurements/throughput_test.go
git commit -m "feat(measurements): split Add into measure and record, add rejected counters

Assisted-by: Claude Fable 5.1"
```

### Task 3: Registry ignores rejected traffic

**Files:**
- Modify: `pkg/measurements/throughput_test.go`

**Interfaces:**
- Consumes: `RecordLogs`, `RecordRejectedLogs`, `ResettableThroughputMeasurementsRegistry.OTLPMeasurements`.

- [ ] **Step 1: Write the test**

Append to `pkg/measurements/throughput_test.go`:

```go
func TestRegistry_IgnoresRejectedRecords(t *testing.T) {
	reg := NewResettableThroughputMeasurementsRegistry(true)
	tmp, _ := newTestMeasurements(t)
	require.NoError(t, reg.RegisterThroughputMeasurements("throughputmeasurement/1", tmp))

	// Rejected-only traffic produces no report.
	tmp.RecordRejectedLogs(context.Background(), Measurement{Size: 3974, Count: 16})
	require.Equal(t, 0, reg.OTLPMeasurements(nil).DataPointCount())

	// Delivered traffic produces a report that carries no rejected series.
	tmp.RecordLogs(context.Background(), Measurement{Size: 3974, Count: 16})
	reported := reg.OTLPMeasurements(nil)
	require.NotEqual(t, 0, reported.DataPointCount())

	metrics := reported.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
	for i := 0; i < metrics.Len(); i++ {
		require.NotContains(t, metrics.At(i).Name(), "_rejected")
	}

	// More rejected traffic after a report does not trigger another one.
	tmp.RecordRejectedLogs(context.Background(), Measurement{Size: 3974, Count: 16})
	require.Equal(t, 0, reg.OTLPMeasurements(nil).DataPointCount())
}
```

- [ ] **Step 2: Run the test**

Run: `cd pkg/measurements && go test ./... -run TestRegistry_IgnoresRejectedRecords -v`
Expected: PASS on the first run. This test locks in behavior that Task 2 already provides. If it fails, the sequence number rule in Task 2 is wrong; fix Task 2, not the test.

- [ ] **Step 3: Commit**

```bash
git add pkg/measurements/throughput_test.go
git commit -m "test(measurements): registry ignores rejected records

Assisted-by: Claude Fable 5.1"
```

---

## Branch 2: `briangardner/bpop-5831-processor-count-on-success`

- [ ] **Step 0: Create the branch**

```bash
cd ~/git/bindplane-otel-contrib.worktrees/briangardner/bpop-5831-throughput-count-on-success
gh stack add briangardner/bpop-5831-processor-count-on-success
git branch --show-current
```
Expected: prints `briangardner/bpop-5831-processor-count-on-success`.

### Task 4: Logs consumer wrapper and factory wiring

**Files:**
- Create: `processor/throughputmeasurementprocessor/consumer.go`
- Create: `processor/throughputmeasurementprocessor/consumer_test.go`
- Modify: `processor/throughputmeasurementprocessor/processor.go`
- Modify: `processor/throughputmeasurementprocessor/factory.go`
- Modify: `processor/throughputmeasurementprocessor/processor_test.go`

**Interfaces:**
- Consumes: `measurements.MeasureLogs`, `RecordLogs`, `RecordRejectedLogs` from Branch 1.
- Produces:
  - `func newLogsConsumer(tmp *throughputMeasurementProcessor, next consumer.Logs) consumer.Logs`
  - `func (tmp *throughputMeasurementProcessor) sample() bool`
  - `func passLogs(_ context.Context, ld plog.Logs) (plog.Logs, error)`

- [ ] **Step 1: Write the failing tests**

Create `processor/throughputmeasurementprocessor/consumer_test.go`:

```go
// (license header)

package throughputmeasurementprocessor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
)

var errDownstream = errors.New("downstream refused")

// newTestProcessor builds a processor with the given config and returns it with a
// manual reader for its counters.
func newTestProcessor(t *testing.T, cfg *Config) (*throughputMeasurementProcessor, *metric.ManualReader) {
	t.Helper()

	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, cfg, component.MustNewIDWithName("throughputmeasurement", t.Name()))
	require.NoError(t, err)
	return tmp, reader
}

// collectSums reads every Int64 sum from the reader and returns name to value.
func collectSums(t *testing.T, reader *metric.ManualReader) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	sums := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			require.Len(t, sum.DataPoints, 1, m.Name)
			sums[m.Name] = sum.DataPoints[0].Value
		}
	}
	return sums
}

func TestLogsConsumer_Delivered(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1, MeasureLogRawBytes: true})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	sink := new(consumertest.LogsSink)
	require.NoError(t, newLogsConsumer(tmp, sink).ConsumeLogs(context.Background(), logs))
	require.Equal(t, 16, sink.LogRecordCount())

	sums := collectSums(t, reader)
	require.Equal(t, int64(3974), sums["otelcol_processor_throughputmeasurement_log_data_size"])
	require.Equal(t, int64(16), sums["otelcol_processor_throughputmeasurement_log_count"])
	require.Equal(t, int64(2373), sums["otelcol_processor_throughputmeasurement_log_raw_bytes"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_data_size_rejected")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_count_rejected")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_raw_bytes_rejected")
}

func TestLogsConsumer_Rejected(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1, MeasureLogRawBytes: true})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	err = newLogsConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeLogs(context.Background(), logs)
	require.ErrorIs(t, err, errDownstream)

	sums := collectSums(t, reader)
	require.Equal(t, int64(3974), sums["otelcol_processor_throughputmeasurement_log_data_size_rejected"])
	require.Equal(t, int64(16), sums["otelcol_processor_throughputmeasurement_log_count_rejected"])
	require.Equal(t, int64(2373), sums["otelcol_processor_throughputmeasurement_log_raw_bytes_rejected"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_data_size")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_count")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_log_raw_bytes")
	require.Equal(t, int64(0), tmp.measurements.SequenceNumber())
}

// A downstream batch processor moves the records out of the payload before it returns.
// The measurement must be taken before the forward, so the full size is recorded.
func TestLogsConsumer_MeasuresBeforeForward(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	draining, err := consumer.NewLogs(func(_ context.Context, ld plog.Logs) error {
		ld.ResourceLogs().MoveAndAppendTo(plog.NewResourceLogsSlice())
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, newLogsConsumer(tmp, draining).ConsumeLogs(context.Background(), logs))
	require.Equal(t, 0, logs.LogRecordCount(), "precondition: downstream emptied the payload")

	sums := collectSums(t, reader)
	require.Equal(t, int64(3974), sums["otelcol_processor_throughputmeasurement_log_data_size"])
	require.Equal(t, int64(16), sums["otelcol_processor_throughputmeasurement_log_count"])
}

func TestLogsConsumer_DisabledRecordsNothing(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: false, SamplingRatio: 1})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	err = newLogsConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeLogs(context.Background(), logs)
	require.ErrorIs(t, err, errDownstream)

	require.Empty(t, collectSums(t, reader))
}

func TestLogsConsumer_ZeroSamplingRecordsNothing(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 0})

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	err = newLogsConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeLogs(context.Background(), logs)
	require.ErrorIs(t, err, errDownstream)

	require.Empty(t, collectSums(t, reader))
}
```

Note on zero sampling: `rand.Float64()` returns a value in `[0, 1)`, so `rand.Float64() <= 0` is true only when it returns exactly 0. That is the existing behavior and the test tolerates it only in theory; the probability is negligible.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd processor/throughputmeasurementprocessor && go test ./... -run 'TestLogsConsumer' -v`
Expected: build failure, `undefined: newLogsConsumer`.

- [ ] **Step 3: Write the wrapper**

Create `processor/throughputmeasurementprocessor/consumer.go`:

```go
// (license header)

package throughputmeasurementprocessor

import (
	"context"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/observiq/bindplane-otel-contrib/pkg/measurements"
)

// logsConsumer wraps the next consumer in the pipeline.
// It measures a sampled payload before it forwards it, then records the
// measurement as delivered when the next consumer returns nil and as rejected
// when it returns an error. The measurement is taken before the forward
// because downstream components can move data out of the payload.
type logsConsumer struct {
	tmp  *throughputMeasurementProcessor
	next consumer.Logs
}

func newLogsConsumer(tmp *throughputMeasurementProcessor, next consumer.Logs) consumer.Logs {
	return &logsConsumer{tmp: tmp, next: next}
}

// Capabilities reports the capabilities of the wrapped consumer.
func (c *logsConsumer) Capabilities() consumer.Capabilities {
	return c.next.Capabilities()
}

// ConsumeLogs forwards the payload and records the outcome.
func (c *logsConsumer) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	if !c.tmp.sample() {
		return c.next.ConsumeLogs(ctx, ld)
	}

	m := measurements.MeasureLogs(ld, c.tmp.measureLogRawBytes)

	if err := c.next.ConsumeLogs(ctx, ld); err != nil {
		c.tmp.measurements.RecordRejectedLogs(ctx, m)
		return err
	}

	c.tmp.measurements.RecordLogs(ctx, m)
	return nil
}
```

- [ ] **Step 4: Update the processor**

In `processor/throughputmeasurementprocessor/processor.go`:

Delete `processTraces`, `processLogs`, and `processMetrics`. Add:

```go
// sample reports whether the current payload is measured.
// A disabled processor never samples. The decision is taken before the payload
// is forwarded, so delivered and rejected payloads are sampled at the same ratio.
func (tmp *throughputMeasurementProcessor) sample() bool {
	if !tmp.enabled {
		return false
	}
	//#nosec G404 -- randomly generated number is not used for security purposes. It's ok if it's weak
	return rand.Float64() <= tmp.samplingCutOffRatio
}
```

Remove the now-unused imports `plog`, `pmetric`, `ptrace` from `processor.go`. Keep `math/rand`.

- [ ] **Step 5: Update the factory for logs**

In `processor/throughputmeasurementprocessor/factory.go`, add the pass-through and use the wrapper. Metrics and traces keep calling the old process functions for now; they are removed in Task 5, so the package will not build until Task 5 unless you leave temporary stubs. To keep each step green, do Task 4 and Task 5 code changes together before running the build, but commit them as described.

Add to `factory.go`:

```go
// passLogs is the processorhelper process function. Measurement happens in the
// consumer wrapper so that the outcome of the forward is known.
func passLogs(_ context.Context, ld plog.Logs) (plog.Logs, error) {
	return ld, nil
}
```

Change `createLogsProcessor`:

```go
	return processorhelper.NewLogs(
		ctx, set, cfg, newLogsConsumer(tmp, nextConsumer), passLogs,
		processorhelper.WithCapabilities(consumerCapabilities),
		processorhelper.WithStart(tmp.start),
		processorhelper.WithShutdown(tmp.shutdown),
	)
```

Add `"go.opentelemetry.io/collector/pdata/plog"` to the factory imports.

- [ ] **Step 6: Route existing logs tests through the wrapper**

In `processor/throughputmeasurementprocessor/processor_test.go`, add `"go.opentelemetry.io/collector/consumer/consumertest"` to the imports, then:

Replace in `TestProcessor_Logs` (around line 62):

```go
	processedLogs, err := tmp.processLogs(context.Background(), logs)
	require.NoError(t, err)

	// Output logs should be the same as input logs (passthrough check)
	require.NoError(t, plogtest.CompareLogs(logs, processedLogs))
```
with
```go
	sink := new(consumertest.LogsSink)
	require.NoError(t, newLogsConsumer(tmp, sink).ConsumeLogs(context.Background(), logs))

	// Output logs should be the same as input logs (passthrough check)
	require.Len(t, sink.AllLogs(), 1)
	require.NoError(t, plogtest.CompareLogs(logs, sink.AllLogs()[0]))
```

Replace every remaining `_, err = tmpX.processLogs(context.Background(), logs)` (where `tmpX` is `tmp`, `tmp1`, `tmp2`, or `tmp3`) with `err = newLogsConsumer(tmpX, consumertest.NewNop()).ConsumeLogs(context.Background(), logs)`. This sed does it:

```bash
cd processor/throughputmeasurementprocessor
sed -i '' -E 's/_, err = (tmp[0-9]*)\.processLogs\(context\.Background\(\), logs\)/err = newLogsConsumer(\1, consumertest.NewNop()).ConsumeLogs(context.Background(), logs)/' processor_test.go
grep -n 'processLogs' processor_test.go
```
Expected: grep prints nothing.

- [ ] **Step 7: Continue with Task 5 before building**

The package does not compile until metrics and traces are converted. Proceed to Task 5 Step 3 onward, then return here for the commit.

- [ ] **Step 8: Run the logs tests**

Run: `cd processor/throughputmeasurementprocessor && go test ./... -run 'TestLogsConsumer|TestProcessor_Logs' -v 2>&1 | grep -E '^(--- |FAIL|ok|PASS)'`
Expected: all PASS.

- [ ] **Step 9: Commit (logs)**

```bash
cd processor/throughputmeasurementprocessor && gofmt -l . && cd -
git add processor/throughputmeasurementprocessor/consumer.go \
        processor/throughputmeasurementprocessor/consumer_test.go \
        processor/throughputmeasurementprocessor/processor.go \
        processor/throughputmeasurementprocessor/factory.go \
        processor/throughputmeasurementprocessor/processor_test.go
git commit -m "feat(throughputmeasurement): record logs only after the next consumer accepts them

Assisted-by: Claude Fable 5.1"
```

This commit includes the Task 5 code as well, because the package only builds with all three signals converted. That is acceptable; Task 5 has no separate commit.

### Task 5: Metrics and traces consumer wrappers

**Files:**
- Modify: `processor/throughputmeasurementprocessor/consumer.go`
- Modify: `processor/throughputmeasurementprocessor/consumer_test.go`
- Modify: `processor/throughputmeasurementprocessor/factory.go`
- Modify: `processor/throughputmeasurementprocessor/processor_test.go`

**Interfaces:**
- Consumes: `measurements.MeasureMetrics`, `MeasureTraces`, `RecordMetrics`, `RecordRejectedMetrics`, `RecordTraces`, `RecordRejectedTraces`.
- Produces:
  - `func newMetricsConsumer(tmp *throughputMeasurementProcessor, next consumer.Metrics) consumer.Metrics`
  - `func newTracesConsumer(tmp *throughputMeasurementProcessor, next consumer.Traces) consumer.Traces`
  - `func passMetrics(_ context.Context, md pmetric.Metrics) (pmetric.Metrics, error)`
  - `func passTraces(_ context.Context, td ptrace.Traces) (ptrace.Traces, error)`

- [ ] **Step 1: Write the failing tests**

Append to `consumer_test.go`. Add `"go.opentelemetry.io/collector/pdata/pmetric"` and `"go.opentelemetry.io/collector/pdata/ptrace"` to its imports.

```go
func TestMetricsConsumer_Delivered(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	sink := new(consumertest.MetricsSink)
	require.NoError(t, newMetricsConsumer(tmp, sink).ConsumeMetrics(context.Background(), metrics))
	require.Equal(t, 37, sink.DataPointCount())

	sums := collectSums(t, reader)
	require.Equal(t, int64(5675), sums["otelcol_processor_throughputmeasurement_metric_data_size"])
	require.Equal(t, int64(37), sums["otelcol_processor_throughputmeasurement_metric_count"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_data_size_rejected")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_count_rejected")
}

func TestMetricsConsumer_Rejected(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	err = newMetricsConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeMetrics(context.Background(), metrics)
	require.ErrorIs(t, err, errDownstream)

	sums := collectSums(t, reader)
	require.Equal(t, int64(5675), sums["otelcol_processor_throughputmeasurement_metric_data_size_rejected"])
	require.Equal(t, int64(37), sums["otelcol_processor_throughputmeasurement_metric_count_rejected"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_data_size")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_metric_count")
	require.Equal(t, int64(0), tmp.measurements.SequenceNumber())
}

func TestMetricsConsumer_MeasuresBeforeForward(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	draining, err := consumer.NewMetrics(func(_ context.Context, md pmetric.Metrics) error {
		md.ResourceMetrics().MoveAndAppendTo(pmetric.NewResourceMetricsSlice())
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, newMetricsConsumer(tmp, draining).ConsumeMetrics(context.Background(), metrics))
	require.Equal(t, 0, metrics.DataPointCount(), "precondition: downstream emptied the payload")

	sums := collectSums(t, reader)
	require.Equal(t, int64(5675), sums["otelcol_processor_throughputmeasurement_metric_data_size"])
	require.Equal(t, int64(37), sums["otelcol_processor_throughputmeasurement_metric_count"])
}

func TestTracesConsumer_Delivered(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	sink := new(consumertest.TracesSink)
	require.NoError(t, newTracesConsumer(tmp, sink).ConsumeTraces(context.Background(), traces))
	require.Equal(t, 178, sink.SpanCount())

	sums := collectSums(t, reader)
	require.Equal(t, int64(16767), sums["otelcol_processor_throughputmeasurement_trace_data_size"])
	require.Equal(t, int64(178), sums["otelcol_processor_throughputmeasurement_trace_count"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_data_size_rejected")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_count_rejected")
}

func TestTracesConsumer_Rejected(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	err = newTracesConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeTraces(context.Background(), traces)
	require.ErrorIs(t, err, errDownstream)

	sums := collectSums(t, reader)
	require.Equal(t, int64(16767), sums["otelcol_processor_throughputmeasurement_trace_data_size_rejected"])
	require.Equal(t, int64(178), sums["otelcol_processor_throughputmeasurement_trace_count_rejected"])
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_data_size")
	require.NotContains(t, sums, "otelcol_processor_throughputmeasurement_trace_count")
	require.Equal(t, int64(0), tmp.measurements.SequenceNumber())
}

func TestTracesConsumer_MeasuresBeforeForward(t *testing.T) {
	tmp, reader := newTestProcessor(t, &Config{Enabled: true, SamplingRatio: 1})

	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	draining, err := consumer.NewTraces(func(_ context.Context, td ptrace.Traces) error {
		td.ResourceSpans().MoveAndAppendTo(ptrace.NewResourceSpansSlice())
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, newTracesConsumer(tmp, draining).ConsumeTraces(context.Background(), traces))
	require.Equal(t, 0, traces.SpanCount(), "precondition: downstream emptied the payload")

	sums := collectSums(t, reader)
	require.Equal(t, int64(16767), sums["otelcol_processor_throughputmeasurement_trace_data_size"])
	require.Equal(t, int64(178), sums["otelcol_processor_throughputmeasurement_trace_count"])
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd processor/throughputmeasurementprocessor && go test ./... -run 'TestMetricsConsumer|TestTracesConsumer' -v`
Expected: build failure, `undefined: newMetricsConsumer`.

- [ ] **Step 3: Write the wrappers**

Append to `consumer.go`. Add `"go.opentelemetry.io/collector/pdata/pmetric"` and `"go.opentelemetry.io/collector/pdata/ptrace"` to its imports.

```go
// metricsConsumer is the metrics counterpart of logsConsumer.
type metricsConsumer struct {
	tmp  *throughputMeasurementProcessor
	next consumer.Metrics
}

func newMetricsConsumer(tmp *throughputMeasurementProcessor, next consumer.Metrics) consumer.Metrics {
	return &metricsConsumer{tmp: tmp, next: next}
}

// Capabilities reports the capabilities of the wrapped consumer.
func (c *metricsConsumer) Capabilities() consumer.Capabilities {
	return c.next.Capabilities()
}

// ConsumeMetrics forwards the payload and records the outcome.
func (c *metricsConsumer) ConsumeMetrics(ctx context.Context, md pmetric.Metrics) error {
	if !c.tmp.sample() {
		return c.next.ConsumeMetrics(ctx, md)
	}

	m := measurements.MeasureMetrics(md)

	if err := c.next.ConsumeMetrics(ctx, md); err != nil {
		c.tmp.measurements.RecordRejectedMetrics(ctx, m)
		return err
	}

	c.tmp.measurements.RecordMetrics(ctx, m)
	return nil
}

// tracesConsumer is the traces counterpart of logsConsumer.
type tracesConsumer struct {
	tmp  *throughputMeasurementProcessor
	next consumer.Traces
}

func newTracesConsumer(tmp *throughputMeasurementProcessor, next consumer.Traces) consumer.Traces {
	return &tracesConsumer{tmp: tmp, next: next}
}

// Capabilities reports the capabilities of the wrapped consumer.
func (c *tracesConsumer) Capabilities() consumer.Capabilities {
	return c.next.Capabilities()
}

// ConsumeTraces forwards the payload and records the outcome.
func (c *tracesConsumer) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	if !c.tmp.sample() {
		return c.next.ConsumeTraces(ctx, td)
	}

	m := measurements.MeasureTraces(td)

	if err := c.next.ConsumeTraces(ctx, td); err != nil {
		c.tmp.measurements.RecordRejectedTraces(ctx, m)
		return err
	}

	c.tmp.measurements.RecordTraces(ctx, m)
	return nil
}
```

- [ ] **Step 4: Update the factory for metrics and traces**

In `factory.go`, add `"go.opentelemetry.io/collector/pdata/pmetric"` and `"go.opentelemetry.io/collector/pdata/ptrace"` to the imports, add:

```go
// passMetrics is the processorhelper process function for metrics. See passLogs.
func passMetrics(_ context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	return md, nil
}

// passTraces is the processorhelper process function for traces. See passLogs.
func passTraces(_ context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	return td, nil
}
```

Change `createTracesProcessor`:

```go
	return processorhelper.NewTraces(
		ctx, set, cfg, newTracesConsumer(tmp, nextConsumer), passTraces,
		processorhelper.WithCapabilities(consumerCapabilities),
		processorhelper.WithStart(tmp.start),
		processorhelper.WithShutdown(tmp.shutdown),
	)
```

Change `createMetricsProcessor`:

```go
	return processorhelper.NewMetrics(
		ctx, set, cfg, newMetricsConsumer(tmp, nextConsumer), passMetrics,
		processorhelper.WithCapabilities(consumerCapabilities),
		processorhelper.WithStart(tmp.start),
		processorhelper.WithShutdown(tmp.shutdown),
	)
```

- [ ] **Step 5: Route existing metrics and traces tests through the wrappers**

In `processor_test.go`, `TestProcessor_Metrics` (around line 125), replace:

```go
	processedMetrics, err := tmp.processMetrics(context.Background(), metrics)
	require.NoError(t, err)

	// Output metrics should be the same as input logs (passthrough check)
	require.NoError(t, pmetrictest.CompareMetrics(metrics, processedMetrics))
```
with
```go
	sink := new(consumertest.MetricsSink)
	require.NoError(t, newMetricsConsumer(tmp, sink).ConsumeMetrics(context.Background(), metrics))

	// Output metrics should be the same as input metrics (passthrough check)
	require.Len(t, sink.AllMetrics(), 1)
	require.NoError(t, pmetrictest.CompareMetrics(metrics, sink.AllMetrics()[0]))
```

In `TestProcessor_Traces` (around line 188), replace:

```go
	processedTraces, err := tmp.processTraces(context.Background(), traces)
	require.NoError(t, err)

	// Output traces should be the same as input traces (passthrough check)
	require.NoError(t, ptracetest.CompareTraces(traces, processedTraces))
```
with
```go
	sink := new(consumertest.TracesSink)
	require.NoError(t, newTracesConsumer(tmp, sink).ConsumeTraces(context.Background(), traces))

	// Output traces should be the same as input traces (passthrough check)
	require.Len(t, sink.AllTraces(), 1)
	require.NoError(t, ptracetest.CompareTraces(traces, sink.AllTraces()[0]))
```

If the exact comment text differs, match on the `processMetrics` and `processTraces` calls. Verify:

```bash
grep -n 'processMetrics\|processTraces\|processLogs' processor/throughputmeasurementprocessor/*.go
```
Expected: no output.

- [ ] **Step 6: Build and run the whole module**

Run: `cd processor/throughputmeasurementprocessor && go vet ./... && go test ./... -v 2>&1 | grep -E '^(--- |FAIL|ok|PASS)'`
Expected: `go vet` clean and every test PASS. Then return to Task 4 Step 8 and Step 9 to commit.

### Task 6: Rejected-only traffic reports no measurements over OpAMP

**Files:**
- Modify: `processor/throughputmeasurementprocessor/processor_test.go`

**Interfaces:**
- Consumes: `newLogsConsumer`, `mockOpAMPExtension`, `mockHost` from the existing test files.

- [ ] **Step 1: Write the test**

Append to `processor_test.go`, next to `TestProcessor_ReportsMeasurementsOverOpAMP`:

```go
// Rejected payloads do not advance the sequence number, so the reporter has
// nothing to send when every payload was refused downstream.
func TestProcessor_RejectedOnlyDoesNotReportOverOpAMP(t *testing.T) {
	mp := metric.NewMeterProvider()
	defer mp.Shutdown(context.Background())

	processorID := component.MustNewIDWithName("throughputmeasurement", "rejected")
	opampID := component.MustNewID("opamp")

	tmp, err := newThroughputMeasurementProcessor(zap.NewNop(), mp, &Config{
		Enabled:       true,
		SamplingRatio: 1,
		OpAMP:         opampID,
		Global:        &GlobalConfig{Interval: 50 * time.Millisecond},
	}, processorID)
	require.NoError(t, err)

	mockOpamp := &mockOpAMPExtension{msgChan: make(chan *protobufs.CustomMessage, 1)}
	mh := mockHost{
		extMap: map[component.ID]component.Component{
			opampID: mockOpamp,
		},
	}

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	err = newLogsConsumer(tmp, consumertest.NewErr(errDownstream)).ConsumeLogs(context.Background(), logs)
	require.ErrorIs(t, err, errDownstream)

	require.NoError(t, tmp.start(context.Background(), mh))
	require.Never(t, func() bool {
		return mockOpamp.GotMessage()
	}, 500*time.Millisecond, 10*time.Millisecond)

	require.NoError(t, tmp.shutdown(context.Background()))
}
```

- [ ] **Step 2: Run the test**

Run: `cd processor/throughputmeasurementprocessor && go test ./... -run 'TestProcessor_RejectedOnlyDoesNotReportOverOpAMP|TestProcessor_ReportsMeasurementsOverOpAMP' -v -count=1`
Expected: both PASS. The rejected-only test passes on the first run; it locks in the sequence number rule end to end.

- [ ] **Step 3: Commit**

```bash
git add processor/throughputmeasurementprocessor/processor_test.go
git commit -m "test(throughputmeasurement): rejected-only traffic does not report over opamp

Assisted-by: Claude Fable 5.1"
```

### Task 7: README

**Files:**
- Modify: `processor/throughputmeasurementprocessor/README.md`

- [ ] **Step 1: Update the intro and counter list**

Replace the first paragraph and the Counters list with:

```markdown
This processor samples OTLP payloads and measures the protobuf size as well as number of OTLP objects in that payload. A payload is recorded only after the next component in the pipeline accepts it. When the pipeline applies backpressure and the next component refuses the payload, the payload is recorded into the `_rejected` counters instead. These measurements are added to the following counter metrics that can be accessed via the collectors internal telemetry service. Units for each `data_size` counter are in Bytes.

Delivered counters (payloads the next component accepted):

- `log_data_size` - The size of the log payload, including all attributes, headers, and metadata
- `log_raw_bytes` - The raw byte size of the log body payload
- `metric_data_size` - The size of the metric payload, including all attributes, headers, and metadata
- `trace_data_size` - The size of the trace payload, including all attributes, headers, and metadata
- `log_count` - The number of log records in the payload
- `metric_count` - The number of metric data points in the payload
- `trace_count` - The number of trace spans in the payload

Rejected counters (payloads the next component refused):

- `log_data_size_rejected`, `log_raw_bytes_rejected`, `log_count_rejected`
- `metric_data_size_rejected`, `metric_count_rejected`
- `trace_data_size_rejected`, `trace_count_rejected`

Each rejected counter has the same meaning and attributes as its delivered counterpart.

## What counts as delivered

The processor forwards the payload and waits for the result. A nil result means delivered. An error means rejected. The whole payload goes to one side; there is no partial credit.

- When the exporter has a sending queue, memory or persistent, the exporter returns nil as soon as it enqueues the request. The processor counts at enqueue time.
- When the exporter has no sending queue, the exporter blocks through its retries and returns the final result. The processor counts only on final success.
- A full sending queue, a permanent error, or exhausted retries return an error. The processor counts the payload as rejected.
- When a pipeline fans out to several exporters, an error from any of them counts the payload as rejected.

Every throughput processor in a pipeline behaves this way, including ones placed early in the processor chain. Under backpressure, all of them report a drop in delivered counters and a rise in rejected counters. Only the delivered counters are reported to Bindplane over OpAMP.
```

- [ ] **Step 2: Commit**

```bash
git add processor/throughputmeasurementprocessor/README.md
git commit -m "docs(throughputmeasurement): document delivered and rejected counters

Assisted-by: Claude Fable 5.1"
```

### Task 7b: After benchmark run and results update

**Files:**
- Modify: `docs/superpowers/specs/2026-09-15-throughput-count-on-success-benchmarks.md`

- [ ] **Step 1: Run the after benchmarks on the processor branch**

```bash
git checkout briangardner/bpop-5831-processor-count-on-success
SCR=$(mktemp -d)
(cd processor/throughputmeasurementprocessor && go test -run '^$' -bench 'BenchmarkProcessor' -benchmem -count=10 ./... > "$SCR/after-processor.txt")
(cd pkg/measurements && go test -run '^$' -bench 'BenchmarkAddLogsMeasureLogRawBytes' -benchmem -count=10 ./... > "$SCR/after-measurements.txt")
```

- [ ] **Step 2: Compare sides**

Use the before files from Task 0 Step 5. If they are gone, re-run Task 0 Step 5 on the spec branch first.

```bash
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/before-processor.txt" "$SCR/after-processor.txt"
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/before-measurements.txt" "$SCR/after-measurements.txt"
```
Expected: rows for every cell with a delta column. The accepting cells show `~` (no significant change) or a delta inside noise. Any significant change on a rejecting cell gets an explanation in the results document.

- [ ] **Step 3: Update the results document**

In `docs/superpowers/specs/2026-09-15-throughput-count-on-success-benchmarks.md`:

- Fill the after commit SHA in Environment.
- Replace the one-sentence After section with the two benchstat comparison tables as fenced text.
- Add a Reading section: two to five sentences that state whether the acceptance criteria in the spec Performance section hold, and explain any significant row.

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/specs/2026-09-15-throughput-count-on-success-benchmarks.md
git commit -m "docs(throughputmeasurement): record after benchmarks for BPOP-5831

Assisted-by: Claude Fable 5.1"
```

### Task 8: Full verification and stack push

- [ ] **Step 1: Run both modules with the race detector**

```bash
cd ~/git/bindplane-otel-contrib.worktrees/briangardner/bpop-5831-throughput-count-on-success
(cd pkg/measurements && go vet ./... && go test -race -count=1 ./...)
(cd processor/throughputmeasurementprocessor && go vet ./... && go test -race -count=1 ./...)
(cd processor/throughputmeasurementprocessor && go test -run '^$' -bench 'BenchmarkProcessor' -benchtime=1x ./...)
```
Expected: `ok` for both modules and twelve benchmark lines.

- [ ] **Step 2: Lint the two modules the way CI does, if the tools are installed**

```bash
which golangci-lint && (cd pkg/measurements && golangci-lint run ./...) && (cd processor/throughputmeasurementprocessor && golangci-lint run ./...)
which addlicense && addlicense -check pkg/measurements/measurement.go pkg/measurements/measurement_test.go processor/throughputmeasurementprocessor/consumer.go processor/throughputmeasurementprocessor/consumer_test.go
```
Expected: no findings. If a tool is missing, note it in the final report rather than installing it.

- [ ] **Step 3: View and push the stack**

```bash
gh stack view
gh stack push
```

Do not run `gh stack submit`. Opening the draft PRs needs the PR template sections from the user, per repo policy.

---

## Self-Review Notes

- Spec coverage: measure/record split (Task 1, 2), rejected counters (Task 2), sequence rule (Task 2, 3, 6), OpAMP exclusion (Task 3), wrapper with sample before measure (Task 4, 5), MoveAndAppendTo case (Task 4, 5), disabled and zero sampling (Task 4), reporter rejected-only (Task 6), README and descriptions (Task 2, 7), Performance benchmark and results (Task 0, 7b).
- Type consistency: `Measurement` fields `Size`, `Count`, `RawBytes`, `HasRawBytes` are used with those exact names in every task. Wrapper constructors are `newLogsConsumer`, `newMetricsConsumer`, `newTracesConsumer` throughout.
- Known compile gap: Task 4 and Task 5 must land together for the package to build. The plan states this in Task 4 Step 7 and folds both into one commit.
