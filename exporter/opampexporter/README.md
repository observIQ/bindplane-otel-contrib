# OpAMP Exporter

This exporter sends OTLP telemetry payloads to a connected OpAMP server as
[OpAMP custom messages](https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#customcapabilities).

It registers a custom capability (default `com.bindplane.opampexporter`)
with an OpAMP extension in the collector, and for each batch of logs,
metrics, or traces it receives, it sends a custom message (default type
`otlp-snappy`) whose body is the OTLP-proto-encoded payload for that
signal, compressed with [Snappy](https://github.com/google/snappy).

The capability and message type are configurable so multiple exporter
instances can coexist and carry differently-shaped payloads (for example,
throughput metrics vs. health metrics) on their own capabilities.

## Supported signals

| Signal  | Stability |
|---------|-----------|
| logs    | alpha     |
| metrics | alpha     |
| traces  | alpha     |

## Configuration

| Field          | Default                       | Description                                                                                          |
|----------------|-------------------------------|------------------------------------------------------------------------------------------------------|
| `opamp`        | `opamp`                       | Component ID of the OpAMP extension used to register the custom capability and send custom messages. |
| `capability`   | `com.bindplane.opampexporter` | Custom capability registered on the OpAMP extension and used on every outgoing message.              |
| `message_type` | `otlp-snappy`                 | Custom message type used for each payload. The default indicates snappy-compressed OTLP-proto.       |

### Example — default

```yaml
extensions:
  opamp:
    server:
      ws:
        endpoint: wss://opamp.example.com/v1/opamp

exporters:
  opamp:
    opamp: opamp

service:
  extensions: [opamp]
  pipelines:
    logs:
      receivers: [otlp]
      exporters: [opamp]
    metrics:
      receivers: [otlp]
      exporters: [opamp]
    traces:
      receivers: [otlp]
      exporters: [opamp]
```

### Example — multiple exporters on distinct capabilities

```yaml
exporters:
  opamp/throughput:
    opamp: opamp
    capability: com.bindplane.throughput
    message_type: throughput-snappy
  opamp/health:
    opamp: opamp
    capability: com.bindplane.health
    message_type: health-snappy

service:
  pipelines:
    metrics/throughput:
      exporters: [opamp/throughput]
    metrics/health:
      exporters: [opamp/health]
```

## Message format

- Capability: configurable via `capability` (default `com.bindplane.opampexporter`).
- Message type: configurable via `message_type` (default `otlp-snappy`).
- Message body: OTLP-proto-encoded `plog.Logs`, `pmetric.Metrics`, or
  `ptrace.Traces` depending on the originating pipeline, then compressed
  with Snappy (block format, as produced by `snappy.Encode`).
