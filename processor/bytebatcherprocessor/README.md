# Byte Batcher Processor

This processor batches incoming telemetry based on accumulated byte size or a time interval, accumulating items before forwarding them to the next consumer in a single payload.

## Supported pipelines

- Logs
- Metrics
- Traces

## How It Works

1. The user configures the byte batcher processor in their pipeline with a byte threshold and flush interval.
2. Incoming telemetry items are accumulated in memory and their serialized size is computed using Protocol Buffers marshaling.
3. When either condition is met, accumulated items are flushed:
   - The accumulated byte size exceeds the `bytes` threshold, or
   - The `flush_interval` timer fires
4. All accumulated items are merged into a single telemetry payload (using move semantics, no copies) and forwarded to the next consumer.
5. The accumulator is reset and the cycle repeats.

## Configuration

| Field           | Type     | Default    | Description                                                                                               |
| --------------- | -------- | ---------- | --------------------------------------------------------------------------------------------------------- |
| `flush_interval` | duration | `1s`       | How long to wait before flushing accumulated items, even if the byte threshold has not been reached.      |
| `bytes`         | int      | `1048576`  | The byte size threshold. Items are accumulated until this size is exceeded, then flushed immediately.    |

### Example Config

The following configuration uses the byte batcher processor to batch telemetry before sending it to an OTLP exporter. Items are flushed after 2 seconds OR when 2MB of data has accumulated, whichever comes first.

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317

processors:
  bytebatcher:
    flush_interval: 2s
    bytes: 2097152  # 2MB

exporters:
  otlp:
    endpoint: collector.example.com:4317

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [bytebatcher]
      exporters: [otlp]
    metrics:
      receivers: [otlp]
      processors: [bytebatcher]
      exporters: [otlp]
    logs:
      receivers: [otlp]
      processors: [bytebatcher]
      exporters: [otlp]
```

## When to Use

**Good fit:**

- Downstream systems with per-request limits/costs (cloud storage, paid APIs)
- Scenarios where network efficiency (fewer requests, better compression) is important
- Can be placed anywhere in the pipeline (before exporters, after connectors, etc.)

### Sizing

The byte threshold uses `ProtoMarshaler` from the OpenTelemetry SDK, which measures **on-wire serialized size**:

- `plog.ProtoMarshaler{}.LogsSize(logs)`
- `pmetric.ProtoMarshaler{}.MetricsSize(metrics)`
- `ptrace.ProtoMarshaler{}.TracesSize(traces)`

This means the threshold applies to the actual bytes that would be transmitted before hitting the exporter.

### Shutdown Behavior

On graceful shutdown, the processor:
1. Closes the flush signal channel
2. The background goroutine receives the signal
3. A final flush of all remaining items occurs
4. The processor waits (with timeout) for the goroutine to complete

## Limitations

- **No item splitting** — If a single item exceeds the byte threshold, it is sent alone
- **No filtering or sampling** — Use a separate processor for those concerns
- **FIFO order** — Items are flushed in the order they arrived
- **Background context** — Flush operations use a background context, not the original request context (errors are logged, not propagated back to the caller)
