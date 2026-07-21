# Chronicle Forwarder Exporter

The Chronicle Forwarder Exporter is designed for forwarding logs to a Chronicle Forwarder endpoint using either Syslog or File-based methods. This exporter supports customization of data export types and various configuration options to tailor the connection and data handling to specific needs.

## Minimum Agent Versions

- Introduced: [v1.42.0](https://github.com/observIQ/bindplane-otel-collector/releases/tag/v1.42.0)

## Supported Pipelines

- Logs

## How It Works

1. For Syslog, it establishes a network connection to the specified Chronicle forwarder endpoint.
2. For File, it writes logs to a specified file path.

## Configuration

| Field                | Type   | Default Value     | Required | Description                                       |
| -------------------- | ------ | ----------------- | -------- | ------------------------------------------------- |
| export_type          | string | `syslog`          | `true`   | Type of export, either `syslog` or `file`.        |
| raw_log_field        | string |                   | `false`  | The field name to send raw logs to Chronicle.     |
| syslog.endpoint      | string | `127.0.0.1:10514` | `false`  | The Chronicle forwarder endpoint.                 |
| syslog.transport     | string | `tcp`             | `false`  | The network protocol to use (e.g., `tcp`, `udp`). |
| syslog.tls.key_file  | string |                   | `false`  | Configure the exporter to use TLS.                |
| syslog.tls.cert_file | string |                   | `false`  | Configure the exporter to use TLS.                |
| file.path            | string |                   | `false`  | The path to the file for storing logs.            |

The `syslog.tls` field supports the full set of client TLS options, documented [here](https://github.com/open-telemetry/opentelemetry-collector/blob/main/config/configtls/README.md#client-configuration).

This exporter also supports the standard exporter helper options:

| Field            | Default   | Description                                                                                                                                                                          |
| ---------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| timeout          | `5s`      | Time to wait per individual attempt to send data. Documented [here](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/exporterhelper/README.md).           |
| sending_queue    | enabled   | Queueing and batching settings for the exporter. Documented [here](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/exporterhelper/README.md).            |
| retry_on_failure | enabled   | Retry settings for failed sends. Documented [here](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/exporterhelper/README.md).                            |

## Raw Log Field

The raw log field is the field name that the exporter will use to send raw logs to Chronicle. It is an OTTL expression that can be used to reference any field in the log record. If the field is not present in the log record, the exporter will not send the log to the Chronicle Forwarder. The log record context can be viewed here: [Log Record Context](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/pkg/ottl/contexts/ottllog/README.md).

## Example Configurations

### Syslog Configuration Example

```yaml
chronicleforwarder:
  export_type: "syslog"
  raw_log_field: body
  syslog:
    endpoint: "syslog.example.com:10514"
    transport: "tcp"
```

### Advanced Options Example

```yaml
chronicleforwarder:
  export_type: "syslog"
  raw_log_field: body
  syslog:
    endpoint: "syslog.example.com:10514"
    transport: "tcp"
    tls:
      cert_file: "/path/to/cert.pem"
      key_file: "/path/to/key.pem"
  timeout: 10s
  sending_queue:
    enabled: true
    queue_size: 5000
  retry_on_failure:
    enabled: true
    initial_interval: 5s
    max_interval: 30s
```

### File Configuration Example

```yaml
chronicleforwarder:
  export_type: "file"
  raw_log_field: attributes["message"]
  file:
    path: "/path/to/logfile"
```

---
