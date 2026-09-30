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
| After code       | processor branch at `32818c85` (spec branch `328ccb98` plus the two code branches) |
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

Each table shows the before column, the after column, and the change. `~`
means benchstat found no significant difference.

### Processor

```text
goos: darwin
goarch: arm64
pkg: github.com/observiq/bindplane-otel-contrib/processor/throughputmeasurementprocessor
cpu: Apple M4 Pro
                                     │ before-processor.txt │        after-processor.txt         │
                                     │        sec/op        │   sec/op     vs base               │
Processor/logs/golden/accepted-14               1.139µ ± 1%   1.220µ ± 1%  +7.07% (p=0.000 n=10)
Processor/logs/golden/rejected-14               1.146µ ± 5%   1.223µ ± 2%  +6.77% (p=0.000 n=10)
Processor/logs/100/accepted-14                  1.412µ ± 1%   1.521µ ± 3%  +7.68% (p=0.000 n=10)
Processor/logs/100/rejected-14                  1.424µ ± 1%   1.518µ ± 1%  +6.60% (p=0.000 n=10)
Processor/logs/1000/accepted-14                 9.184µ ± 1%   9.465µ ± 1%  +3.06% (p=0.000 n=10)
Processor/logs/1000/rejected-14                 9.231µ ± 2%   9.569µ ± 2%  +3.66% (p=0.000 n=10)
Processor/logs/10000/accepted-14                82.28µ ± 4%   86.29µ ± 1%  +4.88% (p=0.000 n=10)
Processor/logs/10000/rejected-14                81.58µ ± 2%   86.43µ ± 1%  +5.94% (p=0.000 n=10)
Processor/metrics/golden/accepted-14            1.356µ ± 1%   1.431µ ± 0%  +5.49% (p=0.000 n=10)
Processor/metrics/golden/rejected-14            1.357µ ± 3%   1.428µ ± 0%  +5.27% (p=0.000 n=10)
Processor/traces/golden/accepted-14             1.732µ ± 2%   1.865µ ± 1%  +7.65% (p=0.000 n=10)
Processor/traces/golden/rejected-14             1.728µ ± 2%   1.850µ ± 2%  +7.03% (p=0.000 n=10)
geomean                                         3.769µ        3.992µ       +5.92%

                                     │ before-processor.txt │         after-processor.txt         │
                                     │         B/op         │    B/op     vs base                 │
Processor/logs/golden/accepted-14                160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/golden/rejected-14                160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/100/accepted-14                   160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/100/rejected-14                   160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/1000/accepted-14                  160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/1000/rejected-14                  160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/10000/accepted-14                 160.0 ± 0%   160.5 ± 0%  +0.31% (p=0.033 n=10)
Processor/logs/10000/rejected-14                 160.0 ± 0%   160.0 ± 1%       ~ (p=0.474 n=10)
Processor/metrics/golden/accepted-14             160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
Processor/metrics/golden/rejected-14             160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
Processor/traces/golden/accepted-14              160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
Processor/traces/golden/rejected-14              160.0 ± 0%   160.0 ± 0%       ~ (p=1.000 n=10) ¹
geomean                                          160.0        160.0       +0.03%
¹ all samples are equal

                                     │ before-processor.txt │         after-processor.txt         │
                                     │      allocs/op       │ allocs/op   vs base                 │
Processor/logs/golden/accepted-14                9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/golden/rejected-14                9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/100/accepted-14                   9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/100/rejected-14                   9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/1000/accepted-14                  9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/1000/rejected-14                  9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/10000/accepted-14                 9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/logs/10000/rejected-14                 9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/metrics/golden/accepted-14             9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/metrics/golden/rejected-14             9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/traces/golden/accepted-14              9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
Processor/traces/golden/rejected-14              9.000 ± 0%   9.000 ± 0%       ~ (p=1.000 n=10) ¹
geomean                                          9.000        9.000       +0.00%
¹ all samples are equal
```

### Measurements package

```text
goos: darwin
goarch: arm64
pkg: github.com/observiq/bindplane-otel-contrib/pkg/measurements
cpu: Apple M4 Pro
                                                            │ before-measurements.txt │       after-measurements.txt        │
                                                            │         sec/op          │    sec/op     vs base               │
AddLogsMeasureLogRawBytes/10Logs_WithOriginal-14                        193.2n ±   1%    203.9n ± 0%  +5.54% (p=0.000 n=10)
AddLogsMeasureLogRawBytes/100Logs_WithOriginal-14                       1.135µ ±   1%    1.175µ ± 1%  +3.53% (p=0.000 n=10)
AddLogsMeasureLogRawBytes/1000Logs_WithOriginal-14                      12.45µ ±   2%    12.63µ ± 1%  +1.48% (p=0.009 n=10)
AddLogsMeasureLogRawBytes/10000Logs_WithOriginal-14                     116.7µ ±   0%    122.2µ ± 1%  +4.70% (p=0.000 n=10)
AddLogsMeasureLogRawBytes/10Logs_WithoutOriginal-14                     156.7n ±   0%    170.3n ± 0%  +8.68% (p=0.000 n=10)
AddLogsMeasureLogRawBytes/100Logs_WithoutOriginal-14                    826.8n ±   1%    870.4n ± 1%  +5.27% (p=0.000 n=10)
AddLogsMeasureLogRawBytes/1000Logs_WithoutOriginal-14                   8.229µ ±   4%    8.288µ ± 1%       ~ (p=0.447 n=10)
AddLogsMeasureLogRawBytes/10000Logs_WithoutOriginal-14                  79.35µ ±   1%    80.06µ ± 1%  +0.90% (p=0.035 n=10)
AddLogsMeasureLogRawBytesFalse/10Logs_WithOriginal-14                   137.7n ±   1%    146.7n ± 0%  +6.57% (p=0.000 n=10)
AddLogsMeasureLogRawBytesFalse/100Logs_WithOriginal-14                  846.8n ±   1%    872.7n ± 1%  +3.05% (p=0.000 n=10)
AddLogsMeasureLogRawBytesFalse/1000Logs_WithOriginal-14                 8.670µ ±   1%    8.819µ ± 1%  +1.72% (p=0.000 n=10)
AddLogsMeasureLogRawBytesFalse/10000Logs_WithOriginal-14                83.02µ ±   1%    85.81µ ± 0%  +3.36% (p=0.000 n=10)
AddLogsMeasureLogRawBytesFalse/10Logs_WithoutOriginal-14                97.88n ±   1%   105.85n ± 1%  +8.15% (p=0.000 n=10)
AddLogsMeasureLogRawBytesFalse/100Logs_WithoutOriginal-14               484.7n ±   1%    503.2n ± 1%  +3.84% (p=0.000 n=10)
AddLogsMeasureLogRawBytesFalse/1000Logs_WithoutOriginal-14              4.595µ ±   1%    4.704µ ± 1%  +2.38% (p=0.001 n=10)
AddLogsMeasureLogRawBytesFalse/10000Logs_WithoutOriginal-14             45.75µ ± 114%    45.03µ ± 0%       ~ (p=1.000 n=10)
geomean                                                                 2.881µ           2.985µ       +3.61%

                                                            │ before-measurements.txt │       after-measurements.txt        │
                                                            │          B/op           │    B/op     vs base                 │
AddLogsMeasureLogRawBytes/10Logs_WithOriginal-14                           120.0 ± 0%   120.0 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/100Logs_WithOriginal-14                          120.0 ± 0%   120.0 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/1000Logs_WithOriginal-14                         120.0 ± 0%   120.0 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/10000Logs_WithOriginal-14                        120.0 ± 0%   120.0 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/10Logs_WithoutOriginal-14                        120.0 ± 0%   120.0 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/100Logs_WithoutOriginal-14                       120.0 ± 0%   120.0 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/1000Logs_WithoutOriginal-14                      120.0 ± 0%   120.0 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/10000Logs_WithoutOriginal-14                     120.0 ± 0%   120.0 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/10Logs_WithOriginal-14                      80.00 ± 0%   80.00 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/100Logs_WithOriginal-14                     80.00 ± 0%   80.00 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/1000Logs_WithOriginal-14                    80.00 ± 0%   80.00 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/10000Logs_WithOriginal-14                   80.00 ± 0%   80.00 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/10Logs_WithoutOriginal-14                   80.00 ± 0%   80.00 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/100Logs_WithoutOriginal-14                  80.00 ± 0%   80.00 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/1000Logs_WithoutOriginal-14                 80.00 ± 0%   80.00 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/10000Logs_WithoutOriginal-14                80.00 ± 0%   80.00 ± 0%       ~ (p=1.000 n=10) ¹
geomean                                                                    97.98        97.98       +0.00%
¹ all samples are equal

                                                            │ before-measurements.txt │       after-measurements.txt        │
                                                            │        allocs/op        │ allocs/op   vs base                 │
AddLogsMeasureLogRawBytes/10Logs_WithOriginal-14                           6.000 ± 0%   6.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/100Logs_WithOriginal-14                          6.000 ± 0%   6.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/1000Logs_WithOriginal-14                         6.000 ± 0%   6.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/10000Logs_WithOriginal-14                        6.000 ± 0%   6.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/10Logs_WithoutOriginal-14                        6.000 ± 0%   6.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/100Logs_WithoutOriginal-14                       6.000 ± 0%   6.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/1000Logs_WithoutOriginal-14                      6.000 ± 0%   6.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytes/10000Logs_WithoutOriginal-14                     6.000 ± 0%   6.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/10Logs_WithOriginal-14                      4.000 ± 0%   4.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/100Logs_WithOriginal-14                     4.000 ± 0%   4.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/1000Logs_WithOriginal-14                    4.000 ± 0%   4.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/10000Logs_WithOriginal-14                   4.000 ± 0%   4.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/10Logs_WithoutOriginal-14                   4.000 ± 0%   4.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/100Logs_WithoutOriginal-14                  4.000 ± 0%   4.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/1000Logs_WithoutOriginal-14                 4.000 ± 0%   4.000 ± 0%       ~ (p=1.000 n=10) ¹
AddLogsMeasureLogRawBytesFalse/10000Logs_WithoutOriginal-14                4.000 ± 0%   4.000 ± 0%       ~ (p=1.000 n=10) ¹
geomean                                                                    4.899        4.899       +0.00%
¹ all samples are equal
```

## Reading

The acceptance line in the spec asked for no regression beyond noise on the
accepting cells. That does not hold. Every processor cell is slower after the
change, by 3% to 8%, with p below 0.001 in every row. Allocations and bytes per
operation are unchanged in every cell, so the cost is CPU time, not memory.

The absolute cost is small and mostly fixed per call. At the small payloads the
increase is 70ns to 130ns per `Consume` call. This matches the shape of the
change: one more consumer layer in front of the real next consumer, one more
method call for the sampling decision, and the measure and record halves as
separate functions that return and take a `Measurement` value. At 1000 and
10000 log records the increase is 0.3µs and 4µs. The `pkg/measurements`
comparison shows the same pattern for `AddLogs` on its own, 1% to 9% at small
payloads and near zero or within noise at 10000 records, so part of the
processor slope at large payloads is likely run-to-run drift rather than the
change.

The accepting and rejecting cells cost the same after the change, as they did
before. Recording to the rejected counters is the same number of counter adds
as recording to the delivered counters.

For a collector this is on the order of 100ns per batch that passes through
the processor. A batch of 1000 log records already costs about 9µs in this
processor, so the added share is about 3%. The change trades this cost for a
correct throughput reading under backpressure.

## Flag and fanout

This section measures the `count_on_delivery` flag and the fanout delivery
tracker. All four sides ran again on the same machine, one after the other,
with no other load: before (`328ccb98`), the processor branch (`f3d3aa1d`),
and this branch with the flag off and on.

`BenchmarkProcessor` runs with the flag off (the default).
`BenchmarkProcessorCountOnDelivery` runs the same cells with the flag on, plus a
`logs/fanout` cell: a source processor above a fanout with one rejecting branch
and one branch that has its own processor and accepts.

### Commands

```sh
(cd processor/throughputmeasurementprocessor && go test -run '^$' -benchmem -count=10 -bench 'BenchmarkProcessor$' . > "$SCR/after-legacy.txt")
(cd processor/throughputmeasurementprocessor && go test -run '^$' -benchmem -count=10 -bench 'BenchmarkProcessorCountOnDelivery' . > "$SCR/after-on.txt")
sed 's/BenchmarkProcessorCountOnDelivery/BenchmarkProcessor/' "$SCR/after-on.txt" > "$SCR/after-on-renamed.txt"
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/pr963-processor.txt" "$SCR/after-legacy.txt"
go run golang.org/x/perf/cmd/benchstat@latest "$SCR/pr963-processor.txt" "$SCR/after-on-renamed.txt"
```

### Flag off compared with the processor branch

| Cell | processor branch | flag off | delta |
| --- | --- | --- | --- |
| logs/golden/accepted | 1.224µs | 1.236µs | +1.06% (p=0.020) |
| logs/golden/rejected | 1.217µs | 1.221µs | ~ |
| logs/1000/accepted | 9.261µs | 9.423µs | +1.75% (p=0.005) |
| logs/10000/accepted | 83.90µs | 84.37µs | ~ |
| metrics/golden/accepted | 1.408µs | 1.417µs | ~ |
| traces/golden/accepted | 1.838µs | 1.785µs | -2.91% (p=0.000) |
| geomean | 3.922µs | 3.930µs | +0.21% |

Bytes and allocations per operation are the same in every cell (160 B, 9 allocs).
The flag-off path adds no cost to the processor branch.

The flag-off path is still slower than before (+3% to +8% on small log payloads).
That is the cost described in [Reading](#reading). A separate run of the
measurements branch (`24ddd215`), which still uses the old process function,
shows the same logs cost (1.141µs to 1.207µs on `logs/golden/accepted`) and no
change on metrics or traces. Thus the logs cost comes from the measure and
record split in `pkg/measurements`, not from the consumer wrapper.

### Flag on compared with the processor branch

| Cell | processor branch | flag on | delta |
| --- | --- | --- | --- |
| logs/golden/accepted | 1.224µs | 1.252µs | +2.29% (p=0.000) |
| logs/golden/rejected | 1.217µs | 1.255µs | +3.12% (p=0.000) |
| logs/1000/accepted | 9.261µs | 9.231µs | ~ |
| logs/10000/accepted | 83.90µs | 83.05µs | -1.02% (p=0.023) |
| metrics/golden/accepted | 1.408µs | 1.451µs | +3.09% (p=0.000) |
| traces/golden/accepted | 1.838µs | 1.879µs | +2.20% (p=0.000) |
| logs/fanout | | 2.594µs | |
| geomean | 3.922µs | 3.839µs | +1.14% |

Every flag-on cell has 2 more allocations and 52 more bytes per operation
(160 B to 212 B, 9 to 11 allocs): one `deliveryTracker` and one
`context.WithValue`. The `logs/fanout` cell has two processors: 464 B and 24
allocs.

The added time is 30ns to 40ns per call on small payloads and within noise on
large payloads.
