# Snapshot Processor

The snapshot processor is used in custom distributions of the collector to provide snapshot functionality in Bindplane. It is not currently included in the official `bindplane-agent`.
## Supported pipelines

- Logs
- Metrics
- Traces

## How it works

1. The user configures the processor in one or more pipelines. If the same processor ID is used across multiple pipelines or signal types, a single shared instance is created.
2. Whenever telemetry passes through the processor, it is copied and stored in an in-memory buffer. The processor keeps a separate buffer per signal type, each holding roughly the most recent 100 log records, metric data points, or spans. Telemetry is passed through to the next consumer unmodified.
3. On startup, the processor registers the `com.bindplane.snapshot` custom capability with the OpAMP extension named by the `opamp` option (typically the `opamp` extension used alongside the `bindplane` extension).
4. An OpAMP server (such as Bindplane) requests a snapshot by sending a `requestSnapshot` custom message. The request identifies a processor ID and pipeline type (`logs`, `metrics`, or `traces`), and may include a search query and minimum timestamp to filter the buffered telemetry, as well as a maximum payload size (default 10MiB).
5. The processor serializes the matching buffered telemetry to JSON, gzip-compresses it, and sends it back over the same OpAMP connection as a `reportSnapshot` custom message. If the payload would exceed the maximum size, telemetry is sampled to fit.

## Configuration

| Field   | Type   | Default | Required | Description                                                            |
|---------|--------|---------|----------|------------------------------------------------------------------------|
| enabled | bool   | `true`  | `false`  | Whether the snapshot processor is enabled or not. When disabled, telemetry passes through without being buffered. |
| opamp   | string | `opamp` | `false`  | Specifies the ID of the opamp extension for sending custom messages.   |


## Examples

### Usage in pipelines

The snapshot processor may be used in a pipeline in order to temporarily catch telemetry data in a buffer, which an opamp server may request:
```yaml
receivers:
  filelog:
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
      receivers: [filelog]
      processors: [snapshotprocessor]
      exporters: [nop]
```

In this instance, the OpAMP server can now request a snapshot using the `com.bindplane.snapshot` capability (see [request.go](./request.go) for more information on the payload).
