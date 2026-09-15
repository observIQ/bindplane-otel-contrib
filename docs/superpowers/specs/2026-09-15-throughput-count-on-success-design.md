# Throughput processor: count only delivered payloads

Linear: [BPOP-5831](https://linear.app/bindplane/issue/BPOP-5831/count-throughput-bytes-only-after-successful-exporter-delivery)

## Problem

The throughput measurement processor records bytes and item counts before it
hands the payload to the next consumer. When the pipeline applies backpressure,
the next consumer returns an error at once. The processor has already counted
the payload. Throughput stays high while no data leaves the collector.

Bindplane uses these measurements for the Data Summary graph and for billing.
Under backpressure the graph must drop.

## Goal

The processor records a payload only when the next consumer accepts it. A nil
return from the next consumer is acceptance. This holds in every configuration:

- With a sending queue on the exporter, memory or persistent, the exporter
  returns nil when it enqueues the request. The processor counts at enqueue.
- Without a sending queue, the exporter blocks through retries and returns the
  final result. The processor counts only on final success.
- A full queue, a permanent error, or exhausted retries return an error. The
  processor does not count the payload as throughput.

The behavior is unconditional. Every throughput processor in a collector gates
on acceptance. This includes the source-side processors Bindplane renders, so
ingress and egress both drop under backpressure.

Payloads the processor measured but the next consumer rejected go to a new set
of rejected counters. Throughput plus rejected equals what arrived. A user can
detect backpressure from the processor's own metrics.

## Non-goals

- No new configuration field. The behavior is always on.
- The rejected counters do not go into the OpAMP report to Bindplane. They are
  internal collector telemetry only. A later change can add them to the report
  together with a server change.
- No per-exporter attribution when a pipeline fans out to more than one
  exporter. Any non-nil error counts the whole payload as rejected. Bindplane
  renders one exporter per pipeline, so this only affects hand-written
  configurations.
- No change to the processorhelper telemetry. `otelcol_processor_incoming_items`
  and `otelcol_processor_outgoing_items` keep their current meaning.

## Design

### Why measure before the call and record after

Downstream components move data out of the payload. The batch processor and the
exporter queue batcher call `MoveAndAppendTo`, which empties the source. When
the next consumer returns, the payload can be empty. The processor must compute
the size and counts before the call, hold them, and record them after the call
returns.

### Measurements package (`pkg/measurements`)

Add a value type that holds one measured payload:

```go
// Measurement holds the size and count of one payload. The processor takes
// it before it forwards the payload and records it after the forward returns.
type Measurement struct {
    Size        int64 // protobuf-encoded size in bytes
    Count       int64 // log records, data points, or spans
    RawBytes    int64 // log body bytes; meaningful only when HasRawBytes is set
    HasRawBytes bool  // true when MeasureLogs measured raw bytes
}
```

Add measure functions that compute a `Measurement` and record nothing:

```go
func MeasureLogs(l plog.Logs, measureLogRawBytes bool) Measurement
func MeasureMetrics(m pmetric.Metrics) Measurement
func MeasureTraces(t ptrace.Traces) Measurement
```

Add record methods on `ThroughputMeasurements`, one pair per signal:

```go
func (tm *ThroughputMeasurements) RecordLogs(ctx context.Context, m Measurement)
func (tm *ThroughputMeasurements) RecordRejectedLogs(ctx context.Context, m Measurement)
// and the Metrics and Traces equivalents
```

`RecordLogs` adds to the existing counters and advances the sequence number.
It records `log_raw_bytes` only when `m.HasRawBytes` is true. This keeps the
current behavior: the series appears only when raw byte measurement is on, and
it appears even when the measured value is zero.
`RecordRejectedLogs` adds to the new rejected counters and does not advance the
sequence number. The sequence number tells the OpAMP reporter that delivered
throughput changed. Rejected traffic must not trigger a report.

The existing `AddLogs`, `AddMetrics`, and `AddTraces` keep their signatures and
behavior. Each becomes measure followed by record. Other callers see no change.

New counters, all with the same attribute set as the existing ones:

| Counter                    | Unit          |
| -------------------------- | ------------- |
| `log_data_size_rejected`   | By            |
| `log_count_rejected`       | {logs}        |
| `log_raw_bytes_rejected`   | By            |
| `metric_data_size_rejected`| By            |
| `metric_count_rejected`    | {datapoints}  |
| `trace_data_size_rejected` | By            |
| `trace_count_rejected`     | {spans}       |

`OTLPMeasurements` and the registry code that builds the OpAMP payload do not
read the rejected counters.

### Processor (`processor/throughputmeasurementprocessor`)

The factory keeps `processorhelper.NewLogs`, `NewMetrics`, and `NewTraces` so
start, shutdown, capabilities, and processorhelper telemetry stay as they are.
The process functions become pass-through and return the payload unchanged.

The factory wraps the real next consumer before it passes it to processorhelper.
The wrapper does the work, once per payload:

1. If the processor is disabled, forward and return. Record nothing.
2. Take one sampling decision with `rand.Float64() <= samplingCutOffRatio`.
   If not sampled, forward and return. Record nothing.
3. Measure the payload.
4. Forward to the real next consumer.
5. On nil, record as delivered. On error, record as rejected. Return the error
   unchanged.

The sampling decision happens before the outcome is known, so delivered and
rejected payloads are sampled at the same ratio.

One wrapper type per signal, each implementing the matching `consumer`
interface, with `Capabilities` delegating to the wrapped consumer. The wrapper
holds a pointer to the shared `throughputMeasurementProcessor` so that the
existing one-instance-per-ID model is unchanged.

The context passed to record is the same context the forward used. The OTel
metric API ignores context cancellation on `Add`, so a canceled context after
a failed forward does not lose the rejected record.

### README

- Add the rejected counters to the counter list.
- Add a short section that states: counters reflect payloads the next consumer
  accepted; with a sending queue that means enqueued; the rejected counters
  hold payloads the next consumer refused; a drop in throughput with a rise in
  rejected means backpressure.
- Update the counter descriptions from "passed to the processor" to "accepted
  by the next consumer".

### Metric descriptions

Update the `WithDescription` strings on the existing counters to match the new
meaning, for example "Size of the log payloads accepted downstream of the
processor".

## Testing

### `pkg/measurements`

- `MeasureLogs` returns the same size and count that `AddLogs` records today,
  with and without raw bytes.
- `RecordLogs` advances the sequence number. `RecordRejectedLogs` does not.
- `RecordRejectedLogs` adds to the rejected counters and leaves the delivered
  counters at zero. Same for metrics and traces.
- `RecordLogs` with `HasRawBytes` false does not emit `log_raw_bytes`. With
  `HasRawBytes` true and `RawBytes` zero, it emits the series with value zero.
- `OTLPMeasurements` output does not include rejected series. Extend the
  existing golden tests if needed so a rejected record does not change them.

### `processor/throughputmeasurementprocessor`

- Existing success tests keep passing with the same expected values, and gain
  an assertion that every rejected counter is absent or zero.
- For each signal: a next consumer that returns an error. Delivered counters
  are absent or zero. Rejected counters hold the payload size and count. The
  error returns to the caller unchanged.
- A next consumer that empties the payload with `MoveAndAppendTo` and returns
  nil. Delivered counters hold the full original size and count.
- Disabled processor with a failing consumer records nothing.
- Sampling ratio zero with a failing consumer records nothing.
- The OpAMP reporter test that asserts a report happens after delivered traffic
  gains a companion: rejected-only traffic does not produce a report.

The processor tests build the consumer through the factory with a
`consumertest` sink or a custom failing consumer, so the wrapper path is the
path under test.

## Rollout

The change ships in the next `pkg/measurements` and processor module versions
and reaches agents through the normal bindplane-otel-collector dependency bump.
No server change is required. Bindplane dashboards built on the existing
counters keep working and start to show drops under backpressure.
