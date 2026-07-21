# Bindplane Extension

This extension is used by Bindplane in custom distributions to store Bindplane specific information.

- It is used for custom collector distributions and BDOT v2, but crucially not BDOT v1.

## Configuration

| Field                         | Type              | Default | Required | Description                                                                                                                                              |
| ----------------------------- | ----------------- | ------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| opamp                         | component ID      |         | `false`  | Component ID of an OpAMP extension. Needed to generate custom messages for throughput and topology measurements. If not specified, no custom messages are generated. |
| measurements_interval         | duration          | `0`     | `false`  | Interval on which to report throughput measurements. Reporting is disabled if the duration is 0. Must not be negative.                                    |
| topology_interval             | duration          | `0`     | `false`  | Interval on which to report topology. Reporting is disabled if the duration is 0. Must not be negative.                                                   |
| extra_measurements_attributes | map[string]string |         | `false`  | Key-value pairs added as attributes to all reported measurements.                                                                                         |
| labels                        | string            |         | `false`  | Deprecated. Labels in `k1=v1,k2=v2` format. This was never used, is ignored (with a warning at startup), and will be removed in a future release.         |

### OpAMP integration (`opamp`)

The `opamp` field references an OpAMP extension by its component ID (e.g. `opamp` or `opamp/name`). When set, the bindplane extension registers custom OpAMP capabilities on that extension and reports over its connection:

- Throughput measurements are sent as snappy-encoded OTLP protobuf every `measurements_interval`.
- Topology state is sent as snappy-encoded JSON every `topology_interval`.

The referenced extension must exist in the configuration and support custom OpAMP messages; the collector fails to start otherwise. The bindplane extension declares a dependency on it, so it starts after the OpAMP extension.

If `opamp` is not set, neither measurements nor topology are reported, regardless of the interval settings.

### Reporting intervals

`measurements_interval` and `topology_interval` independently enable their respective reporting loops. Each accepts a Go duration string (e.g. `30s`, `1m`). A value of `0` (the default) disables that report; negative values are rejected at config validation.

`extra_measurements_attributes` only affects throughput measurements; each entry is added as an attribute on every reported measurement.

## Example Configuration

Bindplane expects a single unnamed bindplane extension in the configuration.

### Full configuration (measurements and topology)

```yaml
receivers:
  nop:

exporters:
  nop:

extensions:
  bindplane:
    opamp: opamp
    measurements_interval: 1m
    topology_interval: 1m
    extra_measurements_attributes:
      keyA: valueB
  opamp:
    server:
      ws:
        endpoint: ws://127.0.0.1:64333/v1/opamp

service:
  extensions: [bindplane, opamp]
  pipelines:
    logs:
      receivers: [nop]
      exporters: [nop]
```

In this configuration we specify an OpAMP extension, measurement & topology intervals of 1 minute, and one extra measurement attribute.

### Measurements only

Topology reporting stays disabled because `topology_interval` defaults to `0`.

```yaml
extensions:
  bindplane:
    opamp: opamp
    measurements_interval: 30s
  opamp:
    server:
      ws:
        endpoint: ws://127.0.0.1:64333/v1/opamp
```

### Minimal (no OpAMP reporting)

Without `opamp`, the extension performs no reporting and only stores Bindplane specific information.

```yaml
extensions:
  bindplane:
```
