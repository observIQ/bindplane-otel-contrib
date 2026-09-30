# Throughput processor: count-on-success benchmarks

Linear: [BPOP-5831](https://linear.app/bindplane/issue/BPOP-5831/count-throughput-bytes-only-after-successful-exporter-delivery)

## Purpose

This document records the before and after benchmarks for the change described
in the Performance section of
[the design spec](2026-09-15-throughput-count-on-success-design.md). The
benchmark drives the processor through the public factory, so the same file
runs on both sides of the change. The before side is `main` with no code
change. The after side is the top of the stack, where the processor records a
payload only after the next consumer accepts it.

## Environment

| Item             | Value                                                  |
| ---------------- | ------------------------------------------------------ |
| Go               | go1.26.7 darwin/arm64                                  |
| CPU              | Apple M4 Pro, 14 cores                                 |
| OS               | macOS 26.6.2                                           |
| Before code      | `main` at `8cbbf5aa`; the spec branch adds documents and the benchmark only |
| After code       | Added by the processor branch                          |
| Runs per side    | 10                                                     |

## Commands

Run from the repository root. Raw output files stay outside the repository.

```bash
SCR=$(mktemp -d)
(cd processor/throughputmeasurementprocessor && go test -run '^$' -bench 'BenchmarkProcessor' -benchmem -count=10 ./... > "$SCR/before-processor.txt")
(cd pkg/measurements && go test -run '^$' -bench 'BenchmarkAddLogsMeasureLogRawBytes' -benchmem -count=10 ./... > "$SCR/before-measurements.txt")
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/before-processor.txt"
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/before-measurements.txt"
```

The after side uses the same commands with `after-` file names, then:

```bash
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/before-processor.txt" "$SCR/after-processor.txt"
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/before-measurements.txt" "$SCR/after-measurements.txt"
```

## Before

### Processor

Every cell allocates the same 160 bytes in 9 allocations. That is the
processorhelper and consumer path, not the measurement. Time scales with
payload size as expected. The accepting and rejecting cells are equal within
noise, which matches the before behavior: the processor measures before the
forward and does not look at the result.

```text
goos: darwin
goarch: arm64
pkg: github.com/observiq/bindplane-otel-contrib/processor/throughputmeasurementprocessor
cpu: Apple M4 Pro
                                     │ before-processor.txt │
                                     │        sec/op        │
Processor/logs/golden/accepted-14               1.139µ ± 1%
Processor/logs/golden/rejected-14               1.146µ ± 5%
Processor/logs/100/accepted-14                  1.412µ ± 1%
Processor/logs/100/rejected-14                  1.424µ ± 1%
Processor/logs/1000/accepted-14                 9.184µ ± 1%
Processor/logs/1000/rejected-14                 9.231µ ± 2%
Processor/logs/10000/accepted-14                82.28µ ± 4%
Processor/logs/10000/rejected-14                81.58µ ± 2%
Processor/metrics/golden/accepted-14            1.356µ ± 1%
Processor/metrics/golden/rejected-14            1.357µ ± 3%
Processor/traces/golden/accepted-14             1.732µ ± 2%
Processor/traces/golden/rejected-14             1.728µ ± 2%
geomean                                         3.769µ

                                     │ before-processor.txt │
                                     │         B/op         │
Processor/logs/golden/accepted-14                160.0 ± 0%
Processor/logs/golden/rejected-14                160.0 ± 0%
Processor/logs/100/accepted-14                   160.0 ± 0%
Processor/logs/100/rejected-14                   160.0 ± 0%
Processor/logs/1000/accepted-14                  160.0 ± 0%
Processor/logs/1000/rejected-14                  160.0 ± 0%
Processor/logs/10000/accepted-14                 160.0 ± 0%
Processor/logs/10000/rejected-14                 160.0 ± 0%
Processor/metrics/golden/accepted-14             160.0 ± 0%
Processor/metrics/golden/rejected-14             160.0 ± 0%
Processor/traces/golden/accepted-14              160.0 ± 0%
Processor/traces/golden/rejected-14              160.0 ± 0%
geomean                                          160.0

                                     │ before-processor.txt │
                                     │      allocs/op       │
Processor/logs/golden/accepted-14                9.000 ± 0%
Processor/logs/golden/rejected-14                9.000 ± 0%
Processor/logs/100/accepted-14                   9.000 ± 0%
Processor/logs/100/rejected-14                   9.000 ± 0%
Processor/logs/1000/accepted-14                  9.000 ± 0%
Processor/logs/1000/rejected-14                  9.000 ± 0%
Processor/logs/10000/accepted-14                 9.000 ± 0%
Processor/logs/10000/rejected-14                 9.000 ± 0%
Processor/metrics/golden/accepted-14             9.000 ± 0%
Processor/metrics/golden/rejected-14             9.000 ± 0%
Processor/traces/golden/accepted-14              9.000 ± 0%
Processor/traces/golden/rejected-14              9.000 ± 0%
geomean                                          9.000
```

### Measurements package

`AddLogs` is the function the change splits into measure and record. The last
row has a wide interval: three of ten runs took two times as long as the other
seven, which sit near 44µs. The after comparison uses the same ten runs.

```text
goos: darwin
goarch: arm64
pkg: github.com/observiq/bindplane-otel-contrib/pkg/measurements
cpu: Apple M4 Pro
                                                            │ before-measurements.txt │
                                                            │         sec/op          │
AddLogsMeasureLogRawBytes/10Logs_WithOriginal-14                        193.2n ±   1%
AddLogsMeasureLogRawBytes/100Logs_WithOriginal-14                       1.135µ ±   1%
AddLogsMeasureLogRawBytes/1000Logs_WithOriginal-14                      12.45µ ±   2%
AddLogsMeasureLogRawBytes/10000Logs_WithOriginal-14                     116.7µ ±   0%
AddLogsMeasureLogRawBytes/10Logs_WithoutOriginal-14                     156.7n ±   0%
AddLogsMeasureLogRawBytes/100Logs_WithoutOriginal-14                    826.8n ±   1%
AddLogsMeasureLogRawBytes/1000Logs_WithoutOriginal-14                   8.229µ ±   4%
AddLogsMeasureLogRawBytes/10000Logs_WithoutOriginal-14                  79.35µ ±   1%
AddLogsMeasureLogRawBytesFalse/10Logs_WithOriginal-14                   137.7n ±   1%
AddLogsMeasureLogRawBytesFalse/100Logs_WithOriginal-14                  846.8n ±   1%
AddLogsMeasureLogRawBytesFalse/1000Logs_WithOriginal-14                 8.670µ ±   1%
AddLogsMeasureLogRawBytesFalse/10000Logs_WithOriginal-14                83.02µ ±   1%
AddLogsMeasureLogRawBytesFalse/10Logs_WithoutOriginal-14                97.88n ±   1%
AddLogsMeasureLogRawBytesFalse/100Logs_WithoutOriginal-14               484.7n ±   1%
AddLogsMeasureLogRawBytesFalse/1000Logs_WithoutOriginal-14              4.595µ ±   1%
AddLogsMeasureLogRawBytesFalse/10000Logs_WithoutOriginal-14             45.75µ ± 114%
geomean                                                                 2.881µ

                                                            │ before-measurements.txt │
                                                            │          B/op           │
AddLogsMeasureLogRawBytes/10Logs_WithOriginal-14                           120.0 ± 0%
AddLogsMeasureLogRawBytes/100Logs_WithOriginal-14                          120.0 ± 0%
AddLogsMeasureLogRawBytes/1000Logs_WithOriginal-14                         120.0 ± 0%
AddLogsMeasureLogRawBytes/10000Logs_WithOriginal-14                        120.0 ± 0%
AddLogsMeasureLogRawBytes/10Logs_WithoutOriginal-14                        120.0 ± 0%
AddLogsMeasureLogRawBytes/100Logs_WithoutOriginal-14                       120.0 ± 0%
AddLogsMeasureLogRawBytes/1000Logs_WithoutOriginal-14                      120.0 ± 0%
AddLogsMeasureLogRawBytes/10000Logs_WithoutOriginal-14                     120.0 ± 0%
AddLogsMeasureLogRawBytesFalse/10Logs_WithOriginal-14                      80.00 ± 0%
AddLogsMeasureLogRawBytesFalse/100Logs_WithOriginal-14                     80.00 ± 0%
AddLogsMeasureLogRawBytesFalse/1000Logs_WithOriginal-14                    80.00 ± 0%
AddLogsMeasureLogRawBytesFalse/10000Logs_WithOriginal-14                   80.00 ± 0%
AddLogsMeasureLogRawBytesFalse/10Logs_WithoutOriginal-14                   80.00 ± 0%
AddLogsMeasureLogRawBytesFalse/100Logs_WithoutOriginal-14                  80.00 ± 0%
AddLogsMeasureLogRawBytesFalse/1000Logs_WithoutOriginal-14                 80.00 ± 0%
AddLogsMeasureLogRawBytesFalse/10000Logs_WithoutOriginal-14                80.00 ± 0%
geomean                                                                    97.98

                                                            │ before-measurements.txt │
                                                            │        allocs/op        │
AddLogsMeasureLogRawBytes/10Logs_WithOriginal-14                           6.000 ± 0%
AddLogsMeasureLogRawBytes/100Logs_WithOriginal-14                          6.000 ± 0%
AddLogsMeasureLogRawBytes/1000Logs_WithOriginal-14                         6.000 ± 0%
AddLogsMeasureLogRawBytes/10000Logs_WithOriginal-14                        6.000 ± 0%
AddLogsMeasureLogRawBytes/10Logs_WithoutOriginal-14                        6.000 ± 0%
AddLogsMeasureLogRawBytes/100Logs_WithoutOriginal-14                       6.000 ± 0%
AddLogsMeasureLogRawBytes/1000Logs_WithoutOriginal-14                      6.000 ± 0%
AddLogsMeasureLogRawBytes/10000Logs_WithoutOriginal-14                     6.000 ± 0%
AddLogsMeasureLogRawBytesFalse/10Logs_WithOriginal-14                      4.000 ± 0%
AddLogsMeasureLogRawBytesFalse/100Logs_WithOriginal-14                     4.000 ± 0%
AddLogsMeasureLogRawBytesFalse/1000Logs_WithOriginal-14                    4.000 ± 0%
AddLogsMeasureLogRawBytesFalse/10000Logs_WithOriginal-14                   4.000 ± 0%
AddLogsMeasureLogRawBytesFalse/10Logs_WithoutOriginal-14                   4.000 ± 0%
AddLogsMeasureLogRawBytesFalse/100Logs_WithoutOriginal-14                  4.000 ± 0%
AddLogsMeasureLogRawBytesFalse/1000Logs_WithoutOriginal-14                 4.000 ± 0%
AddLogsMeasureLogRawBytesFalse/10000Logs_WithoutOriginal-14                4.000 ± 0%
geomean                                                                    4.899
```

## After

The processor branch adds this section with the after tables and the benchstat
comparison against the before files.
