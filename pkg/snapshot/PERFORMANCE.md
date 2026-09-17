# Snapshot buffer performance

This document records how the snapshot buffer's cost was measured, what was
found, what changed, and how to reproduce every number. It exists because the
buffer sits on the hot path of every Bindplane pipeline three times (source,
destination and "View Recent Telemetry" snapshot processors) and was not going
to be revisited for a while.

## 1. What the buffer does

Each `snapshotprocessor` instance keeps the most recent ~100 log records,
metric data points or spans per signal so that Bindplane can show a snapshot on
request. The buffers live in this module (`LogBuffer`, `MetricBuffer`,
`TraceBuffer`) and are shared by the collector's snapshot processor (via its
report manager) and the contrib snapshot processor.

## 2. Baseline: what it cost before

Measured on the shipped code (`pkg/snapshot` v1.14.0, collector 1.107.0).

Hot path, per batch, per processor instance (Go benchmark, Apple M-series):

| Batch                     | ns/op     | allocs/op | B/op      |
|---------------------------|-----------|-----------|-----------|
| 1,000 log records         | 226,983   | 13,012    | 680,685   |
| 10,000 log records        | 2,250,389 | 130,012   | 6,802,409 |
| 1,000 metric data points  | 174,557   | 8,411     | 414,514   |
| 1,000 spans               | 270,421   | 12,011    | 792,609   |

The processor deep-copied every batch (`plog.NewLogs(); ld.CopyTo(...)`) and
the buffer then kept 100 records of it, so the copy was garbage one batch
later. `LogBuffer.Add` itself cost 27 ns; the whole cost was the copy.
Secondary costs: `Len()` re-walked every buffered entry on every `Add`
(dominant at 1-record batches), zero-record batches grew the entry list
without bound, a snapshot request held the hot-path mutex through five
marshal + gzip passes (2.04 ms, 6.0 MB, 2.75 GC cycles per request), and
eviction by re-slicing kept evicted payloads reachable.

Whole-collector impact (Docker, stock `observiq/bindplane-agent:1.107.0`,
three snapshot processors vs none, ~13k log records/s, 90 s, 2 CPUs, see §6):

| Batch shape        | CPU µs/record (with · without) | Overhead | Alloc KB/record | Overhead |
|--------------------|-------------------------------|----------|-----------------|----------|
| 1 record/batch     | 61.5 · 54.0                   | +13.9 %  | 11.7 · 9.4      | +25 %    |
| 1,000 records/batch| 54.9 · 52.3                   | +4.9 %   | 11.3 · 9.4      | +20 %    |

pprof attributed 9.4 % of CPU to `processLogs` at 1-record batches (5.7 % of it
the `Len()` walk) and 17–18 % of all allocated bytes to the copy.

## 3. What changed

Three layers, all inside this module so every consumer gets them with no API
change.

### 3.1 Bounded record store (continuous mode)

- `Add` copies only the newest `min(n, idealSize)` records into a single
  `plog.Logs`/`pmetric.Metrics`/`ptrace.Traces` store and evicts the oldest in
  place (`copy.go`). Resource, scope and metric shells are copied once per
  admitted group, never per record; groups that become empty are removed.
- The count is cached; `Len()` is O(1). Zero-record payloads are dropped.
- `ConstructPayload` copies the ≤100-record store under the lock and does
  filtering, sampling, marshaling and compression unlocked, in one pass
  (largest retention that fits) instead of five; gzip writers are pooled.

### 3.2 Admission budget (continuous mode)

Once a buffer is full it admits about `idealSize` items per
`DefaultRefreshInterval` (1 s), budgeted by records so batch size does not
matter (`admission.go`). Below capacity everything is admitted, so a quiet
pipeline fills promptly. A rejected payload is only counted, never copied:
the check is one atomic load, two atomic adds, one clock read and one more
load.

### 3.3 On-demand mode (high-throughput pipelines)

At every window rollover the buffer takes the pipeline's record and batch
rate from what was offered during the window. After three
consecutive windows above `highThroughputMultiple` (10) buffers' worth of
records per second with at least `minBatchesPerSecond` (10) batches, the
buffer switches to on-demand mode: the store is dropped and `Add` returns
after one atomic load. A snapshot request then arms the buffer, waits until
the store is full or `fillWait` (1 s) has passed, answers, and disarms
(dropping the store again). If a request's wait times out the pipeline is no
longer fast and the buffer returns to continuous mode. Requests that arrive
while another is in flight share the same fill.

Why count offered records instead of timing the fill: a single 1,000-record
batch every 10 s "fills" the budget instantly but is 100 records/s (the first
version of the detector got this wrong and the freshness benchmark at
200 records/s caught it). Every offered batch, admitted or not, adds its record
count to the window (the count is an allocation-free walk of the payload's
resource and scope groups) so the rate is exact rather than extrapolated from
the admitted batches. The batch-rate condition keeps a request from waiting a
whole batch interval on large, infrequent batches.

The request that arms the buffer drops the store first, so a snapshot holds
only records collected for it even if a batch slipped in as the buffer
disarmed. A request that begins in the same microsecond the buffer flips to
on-demand can copy an empty store without waiting; Bindplane's next poll a
second later arms the buffer, so at most one poll is lost. In on-demand mode
each poll returns the ~100 records collected for it, so a 30 s search sees a
sample of the stream, not a continuous tail; continuous mode admits ~100
records per second, so the volume per poll is the same.

### 3.4 Behaviour changes

- A batch larger than `idealSize` retains its newest `idealSize` records, not
  the whole batch.
- In continuous mode a full buffer refreshes ~100 records/s instead of on every
  batch, so a snapshot can be up to 1 s old.
- In on-demand mode the request returns records from the moment of the request
  (a few ms old) and may take up to `100 / rate` seconds plus marshaling to
  answer; a pipeline that just went quiet costs one 1 s wait, then the buffer
  is continuous again.
- A request no longer sees records admitted while it was marshaling.
- `ConstructPayload` may block for up to `fillWait`. The collector's OpAMP
  custom-message handler runs in its own goroutine; the legacy report-manager
  path (`report.yaml`, used by older servers) already blocked on an HTTP POST
  inside the OpAMP reload callback and now also waits for the fill there.

## 4. Go benchmarks (unit level)

Run from this directory:

```sh
go test -run '^$' -bench . -benchmem -benchtime=2000x .
go test -run '^$' -bench 'Freshness|RetainedHeap' -benchmem -benchtime=100x .
cd ../../processor/snapshotprocessor && go test -run '^$' -bench 'BenchmarkProcess|BenchmarkSnapshotRequest' -benchmem -benchtime=2000x .
```

Apple M-series, Go 1.26. "Stock" is v1.14.0 with the benchmark files added
(`chore/snapshot-bench`), "budget" is continuous mode, "on-demand" is a buffer
that has switched modes.

Hot path per batch, processor level (`BenchmarkProcessLogs`, 1,000 records):

| Build     | ns/op   | allocs/op | Notes                                      |
|-----------|---------|-----------|--------------------------------------------|
| stock     | 226,983 | 13,012    | full deep copy                             |
| budget    | 78      | 0         | steady state; one ≤100-record copy per second |
| on-demand | ~2      | 0         | idle between requests                      |

Buffer level (`BenchmarkLogBufferAdd`, 1,000 records):

| Mode                  | ns/op  | allocs/op | Notes                                     |
|-----------------------|--------|-----------|-------------------------------------------|
| stock                 | 27     | 1         | stores the pointer; the processor copied  |
| budget, steady        | 46–79  | 0         | counted, then rejected without a copy     |
| budget, admitted      | 26,798 | 1,310     | bounded copy of 100 records               |
| on-demand, idle       | 1.9    | 0         | one atomic load                           |

Snapshot request (`BenchmarkSnapshotRequest/unfiltered`, processor level):

| Build  | ns/op     | B/op      | GC cycles/op |
|--------|-----------|-----------|--------------|
| stock  | 2,043,868 | 6,007,865 | 2.75         |
| budget | 938,322   | 498,885   | 0.26         |

Freshness and request latency (`BenchmarkLogBufferFreshness`, 100-record
batches, requests back to back):

| Rate (records/s) | Mode reached | Request ns/op | Age of newest record | Records |
|------------------|--------------|---------------|----------------------|---------|
| 200              | continuous   | 272,667       | up to 1 s            | 100     |
| 2,000            | on-demand    | 49,778,265    | 1.6 ms               | 100     |
| 20,000           | on-demand    | 4,792,077     | 1.0 ms               | 100     |

The on-demand request time is the time to collect 100 records at that rate
(50 ms at 2k/s, 5 ms at 20k/s) plus ~0.3 ms to build the payload.

Retained heap per full buffer (`BenchmarkLogBufferRetainedHeap`, 256-byte
bodies, 10 attributes):

| Mode           | retained KB |
|----------------|-------------|
| continuous     | 71.9        |
| on-demand idle | 0.23        |

## 5. Docker A/B (whole collector)

Rig: two collectors from the same image on one host, 2 CPUs each, loaded
simultaneously by telemetrygen v0.139.0 (4 workers × 5,000 records/s, 90 s).
`with` = otlp → `snapshotprocessor/_s0` → `snapshotprocessor` →
`snapshotprocessor/_d0` → nop; `without` = the same pipeline with no snapshot
processors. `batch1000` adds a `batch` processor (1,000) first in both. CPU and
allocation come from the collector's own `otelcol_process_cpu_seconds` and
`otelcol_process_runtime_total_alloc_bytes` deltas, divided by
`otelcol_receiver_accepted_log_records`. Images: `observiq/bindplane-agent:1.107.0`
(stock) and drop-in images built from this branch (`docker/Dockerfile.scratch`
layout).

Apple M-series host (arm64), Docker Desktop:

| Scenario                 | Build     | CPU µs/rec (with · without) | Δ CPU  | Alloc KB/rec (with · without) | Δ alloc | processLogs cum % |
|--------------------------|-----------|-----------------------------|--------|-------------------------------|---------|-------------------|
| 1 record/batch           | stock     | 61.5 · 54.0                 | +13.9 %| 11.99 · 9.60                  | +24.9 % | 9.41 %            |
| 1 record/batch           | budget    | 57.3 · 54.3                 | +5.6 % | 9.83 · 9.58                   | +2.6 %  | 1.08 %            |
| 1 record/batch           | on-demand | 56.5 · 54.0                 | +4.5 % | 9.82 · 9.57                   | +2.6 %  | 0.21 %            |
| 1,000 records/batch      | stock     | 54.9 · 52.3                 | +4.9 % | 11.53 · 9.61                  | +20.0 % | 3.51 %            |
| 1,000 records/batch      | budget    | 52.0 · 53.1                 | −2.1 % | 9.62 · 9.61                   | +0.1 %  | <0.05 %           |
| 1,000 records/batch      | on-demand | 52.3 · 52.5                 | −0.4 % | 9.61 · 9.61                   | +0.0 %  | 0 samples         |

The with/without pair is not a paired sample (two separately loaded
containers), so differences under ~2–3 % are noise; the control sides agree
across builds within 1.4 % (54.0 / 54.3 / 54.0 µs per record at 1-record
batches). Heap bytes attributed to `processLogs` over a run: stock 1,455 MB
(17.7 % of all allocation), budget 10 MB (0.14 %).

On-demand mode engaged in both scenarios (13k batches/s of 1 record, and
~13 batches/s of 1,000, just above the 10 batches/s condition). In the
1-record profile `LogBuffer.Add` is 0.043 % flat and `admission.decide` never
appears, i.e. the hot path returns at the `collecting` gate; the remaining
0.21 % under `processLogs` is the report manager's per-batch string-keyed map
lookup of the buffer, four times the cost of `Add` itself. Caching that pointer
on the processor is a possible follow-up, but the reporter replaces its maps
when the collector restarts on a config reload, so a cache needs a generation
check. At 1,000-record batches nothing from the snapshot path appears in a
21 s profile.

Container RSS was flat at 205–218 MB in all twelve runs and the instantaneous
heap-in-use gauge swings with GC phase, so this rig cannot resolve the
retained-buffer difference (a few hundred KB); the Go benchmark in §4 is the
measurement for that.

## 6. GCP VM (Linux x86-64)

Not run at the time of writing: the author's account had only `roles/viewer`
on compute in the available project, so `gcloud compute instances create` was
denied. The rig is staged for an `n2-standard-4` (4 vCPU) Ubuntu 22.04 VM:
amd64 collector binaries for the budget and on-demand builds, an amd64
telemetrygen image, the same compose/configs/run.sh with a `CPUS` knob and
exact `_total`-tolerant metric parsing, and a step-by-step runbook. Once
`roles/compute.instanceAdmin.v1` is granted the four to six 90 s runs take
about 20 minutes; results belong in the table above with an "x86-64 VM"
label. One caveat noted in advance: two collectors at 2 CPUs each plus two
generators exceed a 4 vCPU box, so a generator-bound result should be rerun on
a larger machine or with the generators on a second VM.

## 7. End-to-end checks

- Unit: `go test -race ./...` in `pkg/snapshot` and `processor/snapshotprocessor`,
  and in the collector's `internal/report`, `internal/processor/snapshotprocessor`
  and `internal/extension/opampconnectionextension`.
- Collector build gate: `make verify-manifest`.
- Browser, continuous mode (first pass, budget build): a local Bindplane with
  the patched collector rolled out to a configuration whose Telemetry Generator
  source emits 2,000 logs/s. Source, destination and agent "View Recent
  Telemetry" snapshots each returned 100 records with the expected body; a
  matching search returned rows, a non-matching one gave the empty state after
  the server's 30 s search timeout, and re-searching recovered. The agent
  stayed connected.
- Browser, both modes (second pass, on-demand build), two agents on the same
  server:

  | Pipeline | Rate | Mode reached | Source snapshot | Newest record age | Span of the 100 records | 5 refreshes 2 s apart |
  |----------|------|--------------|-----------------|-------------------|-------------------------|-----------------------|
  | `snap-perf` | 2,006 rec/s in 502 batches/s | on-demand | 100 raw + 100 processed, 516 ms click to first row | 17–19 ms (agent page: 6 ms) | 49–51 ms, i.e. one just-in-time slice | 111–120 ms each, 100 records each, newest timestamp strictly increasing, no overlap |
  | `snap-slow` | 20 rec/s | continuous | 100 + 100, 510 ms click to first row | 211–362 ms | 4.9 s, i.e. the buffer's whole history, served without waiting | n/a |

  Destination snapshot, search hit, search miss (30 s server timeout, then a
  clean empty state) and recovery all passed on the fast pipeline; search hit
  passed on the slow one. Zero panics or error-level log lines on either
  agent; both stayed connected. The click-to-first-row figure is dominated by
  the UI and OpAMP round trip; the on-demand collection itself is the ~50 ms
  span.
