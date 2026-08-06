# Throughput Measurement Processor

This processor samples OTLP payloads and measures the protobuf size as well as number of OTLP objects in that payload. These measurements are added to the following counter metrics that can be accessed via the collectors internal telemetry service. Units for each `data_size` counter are in Bytes.

Counters:

- `log_data_size` - The size of the log payload, including all attributes, headers, and metadata
- `log_raw_bytes` - The raw byte size of the log body payload
- `metric_data_size` - The size of the metric payload, including all attributes, headers, and metadata
- `trace_data_size` - The size of the trace payload, including all attributes, headers, and metadata
- `log_count` - The number of log records in the payload
- `metric_count` - The number of metric data points in the payload
- `trace_count` - The number of trace spans in the payload

## Minimum agent versions

- Introduced: [v1.8.0](https://github.com/observIQ/bindplane-otel-collector/releases/tag/v1.8.0)

## Supported pipelines:

- Logs
- Metrics
- Traces

## Configuration

| Field                   | Type  | Default | Description                                                                                                                                                                          |
| ----------------------- | ----- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `enabled`               | bool  | `true`  | When `true` signals that measurements are being taken of data passing through this processor. If false this processor acts as a no-op.                                               |
| `sampling_ratio`        | float | `0.5`   | The ratio of data payloads that are sampled. Values between `0.0` and `1.0`. Values closer to `1.0` mean any individual payload is more likely to have its size measured.            |
| `measure_log_raw_bytes` | bool  | `false` | When `true`, for logs, the processor will measure the raw bytes of the payload in addition to the protobuf size. This is more expensive but provides raw measurements if designated. |
| `extra_labels`          | map   |         | Extra key-value pairs added to this processor's measurements. Win over the global block's `extra_measurement_attributes` on conflicting keys. |
| `global`                | block |         | Settings for the reporter shared by every throughput processor in the collector. Exactly one processor in a configuration should carry this block; see below. |
| `global.opamp`          | string |        | Component ID of an opamp extension (e.g. `opamp`) implementing the custom message registry. When set, the shared reporter sends all throughput processors' measurements to Bindplane as custom messages on the `com.bindplane.measurements.v1` capability. |
| `global.interval`       | duration | `1m`  | How often measurements are reported over opamp. Reporting is disabled if set to `0`. |
| `global.extra_measurement_attributes` | map | | Extra key-value pairs added to all reported datapoints. A processor's own `extra_labels` win on conflicting keys. |
| `bindplane_extension`   | string |        | Deprecated; configure `global.opamp` instead. Component ID of a bindplane extension to register measurements with. Ignored when `global.opamp` is set. |

### Startup behavior

Measurement of passing telemetry always works the same way. On startup, every throughput
processor registers its measurements with a reporter shared by every throughput processor in
the collector; the first processor to start creates it, and the last one to shut down tears it
down. What that reporter does is decided by the configuration the Bindplane server rendered:

1. **One processor carries the `global` block with `opamp` set** (current Bindplane servers):
   that processor configures the shared reporter, which registers the
   `com.bindplane.measurements.v1` custom capability with the referenced opamp extension and
   sends one aggregated custom message for all throughput processors every `interval` — the
   same payload the bindplane extension produced. If more than one processor carries a
   `global` block, the last one to start wins and reconfigures the reporter. Reporting is
   disabled if `interval` is `0`. The extension must exist and support custom messages,
   otherwise the collector fails to start. Works with both the upstream `opampextension` and
   the `opamp_connection` extension in self-managed distributions. If `bindplane_extension`
   is also set on that processor, it is ignored with a warning.
2. **No `global` block anywhere**: the shared reporter stays dormant and nothing is reported
   over opamp. Each processor then falls back to the deprecated paths: if
   `bindplane_extension` is set and the referenced extension exists, the processor registers
   its measurements with it and the extension owns the reporting loop (an extension that
   exists but is not a throughput registry fails startup; a rendered but uninstantiated
   extension — older Bindplane servers do this — falls through to the next case). Otherwise
   the processor registers with a package-level registry that the v1 bindplane agent runtime
   reads and reports from — never fatal, and simply inert outside a v1 agent. Measurements
   always remain available via the collector's internal telemetry.

### Example configuration

The example configuration below shows ingesting logs and sampling the size of 50% of the OTLP log objects.

```yaml
receivers:
  file_log:
    include: ["/var/log/*.log"]

processors:
  throughputmeasurement:
    enabled: true
    sampling_ratio: 0.5

exporters:
  googlecloud:

service:
  pipelines:
    logs:
      receivers:
        - file_log
      processors:
        - throughputmeasurement
      exporters:
        - googlecloud
```

The above configuration will add metrics to the collectors internal metrics service which can be scraped via the `http://localhost:8888/metrics` endpoint.

More info on the internal metric service can be found [here](https://opentelemetry.io/docs/collector/configuration/#service).

### Example configuration with opamp reporting

The example below reports both processors' measurements to Bindplane as one aggregated message
per minute. Only one processor carries the `global` block.

```yaml
receivers:
  file_log:
    include: ["/var/log/*.log"]

extensions:
  opamp:
    server:
      ws:
        endpoint: "wss://myserver/v1/opamp"

processors:
  throughputmeasurement/1:
    enabled: true
    sampling_ratio: 0.5
  throughputmeasurement/2:
    enabled: true
    sampling_ratio: 0.5
    global:
      opamp: opamp
      interval: 1m

exporters:
  googlecloud:

service:
  extensions:
    - opamp
  pipelines:
    logs:
      receivers:
        - file_log
      processors:
        - throughputmeasurement/1
        - throughputmeasurement/2
      exporters:
        - googlecloud
```
