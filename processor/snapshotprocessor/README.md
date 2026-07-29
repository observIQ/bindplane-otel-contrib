# Snapshot Processor

The snapshot processor is used in custom distributions of the collector to provide snapshot functionality in Bindplane. It is not currently included in the official `bindplane-agent`.
## Supported pipelines

- Logs
- Metrics
- Traces

## How it works

1. The user configures the processor in one or more pipelines. If the same processor ID is used across multiple pipelines or signal types, a single shared instance is created.
2. Whenever telemetry passes through the processor, a bounded copy is stored in an in-memory buffer. The processor keeps a separate buffer per configured signal type, each holding the most recent `buffer_size` (default 100) log records, metric data points, or spans. Once a buffer is full, it is refreshed with a new batch at most once per `refresh_interval` (default 250ms); batches arriving inside the interval pass through with no buffering cost. Telemetry is always passed through to the next consumer unmodified.
3. On startup, the processor registers the `com.bindplane.snapshot` custom capability with the OpAMP extension named by the `opamp` option (typically the `opamp` extension used alongside the `bindplane` extension).
4. An OpAMP server (such as Bindplane) requests a snapshot by sending a `requestSnapshot` custom message. The request identifies a processor ID and pipeline type (`logs`, `metrics`, or `traces`), and may include a search query and minimum timestamp to filter the buffered telemetry, as well as a maximum payload size (default 10MiB).
5. The processor serializes the matching buffered telemetry to JSON, gzip-compresses it, and sends it back over the same OpAMP connection as a `reportSnapshot` custom message. If the payload would exceed the maximum size, telemetry is sampled to fit.

## Configuration

| Field            | Type     | Default | Required | Description                                                            |
|------------------|----------|---------|----------|------------------------------------------------------------------------|
| enabled          | bool     | `true`  | `false`  | Whether the snapshot processor is enabled or not. When disabled, telemetry passes through without being buffered. The component still reports the collector's standard processor telemetry (incoming/outgoing item counts and duration) per batch; remove the processor from the pipeline entirely to eliminate all overhead. |
| opamp            | string   | `opamp` | `false`  | Specifies the ID of the opamp extension for sending custom messages.   |
| buffer_size      | int      | `100`   | `false`  | Approximate number of log records, metric data points, or spans retained per signal type. Maximum `10000`. |
| refresh_interval | duration | `250ms` | `false`  | How often a full buffer is refreshed with a new batch. Batches arriving inside the interval pass through with no buffering cost. `0` buffers every batch. |
| signals          | []string | all     | `false`  | Limits which signal types are buffered (`logs`, `metrics`, `traces`). Signal types not listed pass through with no buffering cost. Empty buffers all signal types. |
| buffer_mode      | string   | `always`| `false`  | `always` buffers continuously. `on_demand` buffers only while snapshot requests are being received: buffering starts on the first request and stops (dropping buffered telemetry) after 60s without one. With `on_demand`, the first snapshot after an idle period is empty; the server's next poll returns a full one. |


## Examples

### Usage in pipelines

The snapshot processor may be used in a pipeline in order to temporarily catch telemetry data in a buffer, which an opamp server may request:
```yaml
receivers:
  file_log:
    include: [/var/log/logfile.txt]

processors:
  snapshotprocessor:
    enabled: true
    opamp: opamp

exporters:
  nop:

extensions:
  bindplane:
    labels: "labelA=valueA,labelB=valueB"
  opamp:
    endpoint: "https://localhost:3001/v1/opamp"

service:
  extensions: [bindplane, opamp]
  pipelines:
    logs:
      receivers: [file_log]
      processors: [snapshotprocessor]
      exporters: [nop]
```

In this instance, the OpAMP server can now request a snapshot using the `com.bindplane.snapshot` capability (see [request.go](./request.go) for more information on the payload).
