# QRadar Exporter

The QRadar Exporter is designed for forwarding logs to a QRadar instance using its Syslog endpoint. This exporter supports customization of data export types and various configuration options to tailor the connection and data handling to specific needs.

## Minimum Agent Versions

- Introduced: [v1.61.0](https://github.com/observIQ/bindplane-otel-collector/releases/tag/v1.61.0)

## Supported Pipelines

- Logs

## Configuration

| Field                | Type   | Default Value     | Required | Description                                       |
| -------------------- | ------ | ----------------- | -------- | ------------------------------------------------- |
| raw_log_field        | string |                   | `false`  | The field name to send raw logs to QRadar.     |
| syslog.endpoint      | string | `127.0.0.1:10514` | `false`  | The QRadar endpoint.                 |
| syslog.transport     | string | `tcp`             | `false`  | The network protocol to use (e.g., `tcp`, `udp`). |
| syslog.tls.key_file  | string |                   | `false`  | Configure the receiver to use TLS.                |
| syslog.tls.cert_file | string |                   | `false`  | Configure the receiver to use TLS.                |
| timeout              | string | `5s`              | `false`  | See [doc](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/exporterhelper/README.md) for details |
| sending_queue        | map    |                   | `false`  | See [doc](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/exporterhelper/README.md) for details |
| retry_on_failure     | map    |                   | `false`  | See [doc](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/exporterhelper/README.md) for details |

## Raw Log Field

The raw log field is the field name that the exporter will use to send raw logs to QRadar. It is an OTTL expression that can be used to reference any field in the log record. If the field is not present in the log record, the exporter will not send the log to QRadar. The log record context can be viewed here: [Log Record Context](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/pkg/ottl/contexts/ottllog/README.md).

## Example Configurations

### Syslog Configuration Example

```yaml
qradar:
  raw_log_field: body
  syslog:
    endpoint: "syslog.example.com:10514"
    transport: "tcp"
```

### TLS Configuration Example

```yaml
qradar:
  raw_log_field: body
  syslog:
    endpoint: "syslog.example.com:10514"
    transport: "tcp"
    tls:
      key_file: "/path/to/client.key"
      cert_file: "/path/to/client.crt"
```

### Queue and Retry Configuration Example

```yaml
qradar:
  raw_log_field: body
  syslog:
    endpoint: "syslog.example.com:10514"
    transport: "tcp"
  timeout: 30s
  sending_queue:
    enabled: true
    queue_size: 1000
  retry_on_failure:
    enabled: true
    initial_interval: 5s
    max_interval: 30s
    max_elapsed_time: 300s
```

