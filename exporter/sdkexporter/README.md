# SDK Exporter

The `sdk` exporter republishes incoming pipeline metrics as observations on the
collector's OpenTelemetry SDK `MeterProvider` — the same `MeterProvider` the
collector uses for its own self-telemetry. Anything wired to that provider
(Prometheus on `:8888/metrics`, OTLP self-telemetry, etc.) sees the
republished metrics alongside `otelcol_*` metrics.

## Supported Pipelines

- Metrics

## Use Case

Paired with the [`signaltometricsconnector`](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/connector/signaltometricsconnector),
this exporter lets users define new self-telemetry metrics **at runtime via
collector configuration** rather than at compile time. "Telemetry for my
telemetry."

For example, you can write an OTTL condition that counts a certain class of
spans flowing through your pipeline, and then surface that count as a
self-telemetry metric viewable in Prometheus alongside the collector's
built-in instrumentation — no code change required.

```
       pipeline telemetry
              |
              v
   signaltometricsconnector  ──(emits derived pdata metrics)──>  sdkexporter  ──>  SDK MeterProvider  ──>  Prometheus :8888/metrics
                                                                                              ^
                                                                                              |
                                                                            collector's own otelcol_* metrics
```

## Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `include_resource_attributes` | bool | `false` | Fold pdata Resource attributes into each data point's attribute set. Off by default to avoid cardinality bloat. |
| `resource_attribute_keys` | []string | `[]` | When non-empty, restricts which resource attributes are folded. Only meaningful when `include_resource_attributes` is `true`. |

### Example

```yaml
receivers:
  otlp:
    protocols:
      grpc: {}

connectors:
  signaltometrics:
    spans:
      - name: span_count
        sum:
          value: "1"

exporters:
  sdk: {}

service:
  telemetry:
    metrics:
      readers:
        - pull:
            exporter:
              prometheus:
                host: 0.0.0.0
                port: 8888
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [signaltometrics]
    metrics/derived:
      receivers: [signaltometrics]
      exporters: [sdk]
```

After running, `curl localhost:8888/metrics | grep span_count` shows the
derived metric alongside `otelcol_*` self-telemetry.

---

## How It Works (and Why It's Harder Than It Looks)

This section is unusually long because the design is unusual. Read it before
filing bugs about "why does my histogram disappear."

### The OpenTelemetry metrics architecture in two layers

OpenTelemetry metrics has two layers that are easy to conflate:

1. **The instrument API** — what *application code* calls. `Counter`,
   `UpDownCounter`, `Gauge`, `Histogram`, and their observable variants. You
   record individual observations as they happen: `counter.Add(1)`,
   `histogram.Record(latencyMs)`, etc.

2. **The data model (pdata / OTLP)** — what flows over the wire and through
   the collector. Already-aggregated points: `Sum`, `Gauge`, `Histogram`,
   `ExponentialHistogram`, `Summary`, each carrying a *temporality*
   (delta vs cumulative) and pre-computed values, attributes, and timestamps.

The OpenTelemetry **SDK** is the bridge between these two layers. Application
code makes instrument calls; the SDK aggregates them into per-attribute-set
state; a `MetricReader` periodically snapshots the state, formats it as
pdata, and hands it to whatever exporter is wired in.

```
   instrumentation calls          SDK aggregator           pdata / OTLP
   counter.Add(5)         ───>    [running totals,    ───> emitted on
   histogram.Record(...)          buckets, ...]            reader cadence
```

**The data model is the *output* of the SDK.** That's the asymmetry that
shapes everything in this exporter.

### What this exporter does — and why that is not free

`sdkexporter` runs the bridge backwards. It takes pdata (the SDK's *output*)
and tries to push it back through the instrument API (the SDK's *input*).
Conceptually, it's asking the SDK to swallow its own output.

The instrument API was never designed for this. Every place pdata carries
something the instrument API can't naturally express, we either:

- find a back channel that achieves the same effect, or
- accept lossiness, or
- give up and drop the metric with a warning.

The remainder of this section is the type-by-type catalog of which bucket
each pdata kind falls into and why.

### Two losses that apply to *every* supported type

These don't go away no matter what we do with the public SDK API:

1. **Original timestamps are not preserved.** The instrument API has no
   `Add(value, atTime)` form — observations are timestamped at call time. The
   pdata point's `StartTimestamp` and `Timestamp` are dropped; the SDK
   timestamps observations as "now."

2. **pdata `Resource` attributes have no SDK Meter equivalent.** The SDK
   models scope (instrumentation library identity) on the Meter and
   attributes on the data point — there is no per-record resource concept.
   The exporter drops `Resource` attributes by default. Set
   `include_resource_attributes: true` (optionally with
   `resource_attribute_keys`) to fold them into per-data-point attributes,
   at the cost of cardinality.

### Why we don't fail data points back to the pipeline

When this exporter can't represent a data point, it logs a sampled warning
and drops it. It never returns an error to the pipeline. A self-telemetry
republisher that fails its caller would invert the dependency direction:
pipeline behavior would depend on whether self-telemetry could republish a
particular metric type. That's the opposite of what callers want.

---

## Metric Type Support

| pdata Type | Status | SDK Target | Lossy? |
|------------|--------|------------|--------|
| Sum, **delta** temporality, monotonic | Supported | `Int64Counter` / `Float64Counter` | No (modulo timestamps) |
| Sum, **delta** temporality, non-monotonic | Supported | `Int64UpDownCounter` / `Float64UpDownCounter` | No (modulo timestamps) |
| Gauge | Supported | `Int64Gauge` / `Float64Gauge` | No (modulo timestamps) |
| Sum, **cumulative** temporality | **Dropped** | n/a in MVP | — |
| Histogram | **Dropped** | n/a in MVP | — |
| Exponential Histogram | **Dropped** | n/a | — |
| Summary | **Dropped** | n/a | — |

The sections below explain each row.

---

### Sum, delta temporality (supported)

**Mapping.**
- Monotonic → `Int64Counter` / `Float64Counter`. Each pdata data point's
  value is passed to `counter.Add(ctx, value, attrs)`.
- Non-monotonic → `Int64UpDownCounter` / `Float64UpDownCounter`, same
  pattern.

**Why this works cleanly.** A delta sum's value *is* an increment over the
window `[StartTimestamp, Timestamp)`. The synchronous instrument API is
*literally designed* to receive deltas — `counter.Add(delta)` is the canonical
use of the API. The SDK accumulates deltas internally and emits whatever
temporality the configured `MetricReader` selects.

**No state is required on the exporter side.** Push, forget.

---

### Gauge (supported)

**Mapping.** Synchronous `Int64Gauge` / `Float64Gauge`. Each pdata data
point's value is passed to `gauge.Record(ctx, value, attrs)`.

**Why this works cleanly.** Gauge instruments have last-value-wins semantics
per attribute set. The pdata `NumberDataPoint` is also a last-value
representation. One-to-one mapping.

---

### Sum, cumulative temporality (currently dropped)

**The challenge.** A pdata cumulative sum's value is a *running total* since
the series' `StartTimestamp`, not an increment. Pushing it through a
synchronous instrument is *categorically* wrong:

```
pdata point at t=1:  Value = 100  (running total)
pdata point at t=2:  Value = 150  (running total, including the 100)
pdata point at t=3:  Value = 220  (running total, including 150 and 100)
```

If we did `counter.Add(value)` each time, the SDK's own internal accumulator
would receive `100 + 150 + 220 = 470` — over-counting because the SDK is
*itself* maintaining a running total of every `Add` call. We'd be feeding it
running totals it's already trying to compute.

**Possible solution.** Use observable instruments. `Int64ObservableCounter`
accepts a callback that returns "the current cumulative value per attribute
set"; the SDK then handles temporality conversion itself. The exporter would
need to:

1. Maintain a per-instrument map of `attribute.Set -> latestValue`, updated
   on each pdata batch.
2. Register an observable callback at instrument creation that snapshots that
   map and observes each entry.
3. Decide a lifecycle policy for stale series. Once an attribute set stops
   reporting, the observable callback will keep emitting its last-known
   value forever (until process restart). A configurable TTL is the obvious
   knob.

This is a substantial chunk of state-management code (mutex-guarded per-
instrument maps, lifecycle, eviction). It is the natural next milestone for
this exporter, but is deferred from MVP to keep v1 scope tight.

---

### Histogram (currently dropped)

**The challenge.** pdata histograms are *post-aggregation*: bucket counts at
specific boundaries, plus `Sum` and `Count`. The SDK histogram instrument
only accepts individual observations via `histogram.Record(value, attrs)`.
There is **no public API** to inject "47 observations into bucket [5, 10)."

**Possible solution: midpoint replay.** For each bucket, compute the midpoint
of its boundaries and call `histogram.Record(midpoint, attrs)` `count` times.
This is lossy in three distinct ways:

1. **Distribution shape within each bucket is lost.** All `count` observations
   land at the midpoint, not spread across the bucket.

2. **The SDK side decides bucket boundaries via `View` configuration**, not
   the exporter. If the SDK's view doesn't match the source's boundaries —
   and the exporter has no way to know what the SDK's view is — the buckets
   re-shuffle on the way out and `Count` per bucket diverges from the source.

3. **Cost is `O(total_count)`.** A bucket with one million observations
   means one million `Record` calls per export. A per-batch replay budget
   would be required to cap pathological cases, with affected data points
   dropped.

`Sum` and `Count` round-trip exactly under this scheme; per-bucket counts and
the underlying distribution shape do not.

This is implementable but the lossiness is real. For MVP we drop and warn so
nobody mistakes approximated histograms for accurate ones. Adding the path
later is a clean extension: a `histograms.strategy` config field with values
`drop` (current) and `midpoint_replay`, plus a `max_replay_per_batch` budget.

---

### Exponential Histogram (currently dropped)

**The challenge.** Same as regular histograms, plus the OpenTelemetry Go SDK
**has no public synchronous exponential histogram instrument at all**. There
is no `meter.Float64ExponentialHistogram(name)` to call.

**Possible solution.** None cleanly with the current SDK API. Three
imperfect routes exist:

1. **Approximate via a regular histogram + midpoint replay.** Doubly lossy:
   you lose the exponential bucket layout *and* take the regular-histogram
   replay loss on top. Results are unlikely to look like the source data.

2. **Type-assert to `*sdkmetric.MeterProvider` and use SDK internals.**
   `TelemetrySettings.MeterProvider` is the abstract `metric.MeterProvider`
   interface. Even if we type-assert to the concrete SDK type, there is no
   public "register a `Producer`" API after construction; producers are
   wired at MeterProvider construction time via
   `sdkmetric.WithReader(reader, sdkmetric.WithProducer(...))`. So this
   path requires changes to the collector itself, not just an exporter.

3. **Wait for the OTel SDK to expose an exponential histogram instrument.**
   This is the right long-term answer. There is no concrete proposal at the
   time of writing.

For MVP we drop and warn.

---

### Summary (currently dropped)

**The challenge.** `Summary` is a Prometheus-compat type the OpenTelemetry
data model carries for backward compatibility but never adopted as a
first-class instrument. There is **no SDK Summary instrument**.

**Possible solution.** Convert each quantile to an independent gauge
(`metric_p50`, `metric_p99`, etc.). This changes the metric's identity on
the way out — downstream tooling sees N gauges instead of one summary —
which is a legitimate design choice but a meaningful semantic break. Not
implemented in MVP.

---

## Future Work

In rough order of value vs effort:

1. **Cumulative sum support via observable instruments**, with a
   `series_ttl` config knob to evict stale state.
2. **Histogram support via midpoint replay**, with `histograms.strategy` and
   `histograms.max_replay_per_batch` config knobs and clear documentation
   that only `Sum` and `Count` round-trip exactly.
3. **Logs and traces signals.** Currently metrics-only. Logs would map to
   `LogRecord` emission via a `logs/v1` `LoggerProvider`; traces would
   require a trace SDK bridge that has its own asymmetries to think through.
4. **Description / unit conflict policy.** Today, first-write-wins with a
   sampled warn log. A strict mode could be useful in some deployments.
