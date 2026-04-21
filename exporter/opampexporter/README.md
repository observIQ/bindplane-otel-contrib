# OpAMP Exporter

This exporter sends OTLP telemetry payloads to a connected OpAMP server as
[OpAMP custom messages](https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#customcapabilities).

It registers the custom capability `com.bindplane.opampexporter` with an
OpAMP extension in the collector, and for each batch of logs, metrics, or
traces it receives, it sends a custom message of type `otlp` whose body is
the OTLP-proto-encoded payload for that signal.

## Supported signals

| Signal  | Stability |
|---------|-----------|
| logs    | alpha     |
| metrics | alpha     |
| traces  | alpha     |

## Configuration

| Field   | Default | Description                                                                                         |
|---------|---------|-----------------------------------------------------------------------------------------------------|
| `opamp` | `opamp` | Component ID of the OpAMP extension used to register the custom capability and send custom messages. |

### Example

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

## Message format

- Capability: `com.bindplane.opampexporter`
- Message type: `otlp`
- Message body: OTLP-proto-encoded `plog.Logs`, `pmetric.Metrics`, or
  `ptrace.Traces` depending on the originating pipeline.
