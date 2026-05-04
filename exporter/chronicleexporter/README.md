# Chronicle Exporter

This exporter facilitates the sending of logs to Chronicle, which is a security analytics platform provided by Google. It is designed to integrate with OpenTelemetry collectors to export telemetry data such as logs to a Chronicle account.

## Minimum Collector Versions

- Introduced: [v1.39.0](https://github.com/observIQ/bindplane-otel-collector/releases/tag/v1.39.0)

## Supported Pipelines

- Logs

## How It Works

1. The exporter uses the configured credentials to authenticate with the Google Cloud services.
2. It marshals logs into the format expected by Chronicle.
3. It sends the logs to the appropriate Chronicle endpoint over the configured protocol.

## Protocols and APIs

The exporter supports two protocols, each of which targets a different Chronicle ingestion API:

| `protocol` | Target API                                                                                                            | Recommended |
| ---------- | --------------------------------------------------------------------------------------------------------------------- | ----------- |
| `https`    | [Chronicle API](https://docs.cloud.google.com/chronicle/docs/reference/ingestion-methods)                             | Yes         |
| `gRPC`     | [Backstory (Malachite) Ingestion API](https://docs.cloud.google.com/chronicle/docs/reference/ingestion-api)           |             |

**`https` is the recommended protocol.** It targets the newer Chronicle API, supports the `v1alpha` and `v1beta` API versions, and uses improved security practices. The `gRPC` protocol targets the legacy Backstory Ingestion API and is retained for backwards compatibility with existing deployments.

## Configuration

The exporter can be configured using the following fields:

| Field                           | Type              | Default                                | Required | Description                                                                                                                                                |
| ------------------------------- | ----------------- | -------------------------------------- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `protocol`                      | string            | `gRPC`                                 | `false`  | The protocol used to send logs. Valid values are `gRPC` and `https`. See [Protocols and APIs](#protocols-and-apis).                                        |
| `endpoint`                      | string            | `malachiteingestion-pa.googleapis.com` | `false`  | The endpoint for sending to Chronicle. Must not contain a `http://` or `https://` prefix. For `https`, the endpoint is combined with `location` (e.g. `chronicle.googleapis.com`) unless `override_endpoint` is set. |
| `location`                      | string            |                                        | `false`  | The Chronicle region (e.g. `us`, `europe`). Required when `protocol` is `https`.                                                                           |
| `project`                       | string            |                                        | `false`  | The Google Cloud project ID. Required when `protocol` is `https`.                                                                                          |
| `customer_id`                   | string            |                                        | `false`  | The Chronicle customer (instance) ID used for sending logs. Required when `protocol` is `https`.                                                           |
| `api_version`                   | string            | `v1alpha`                              | `false`  | The Chronicle API version to use. Valid values are `v1alpha` and `v1beta`. Only applies to `https` protocol.                                               |
| `override_endpoint`             | bool              | `false`                                | `false`  | Whether to ignore the `location` field when constructing the endpoint. Only applies to `https` protocol.                                                   |
| `creds_file_path`               | string            |                                        | `false`  | The file path to the Google credentials JSON file. Mutually exclusive with `creds`. If neither is set, the exporter falls back to Google Application Default Credentials. |
| `creds`                         | string            |                                        | `false`  | The Google credentials JSON. Mutually exclusive with `creds_file_path`. If neither is set, the exporter falls back to Google Application Default Credentials.             |
| `log_type`                      | string            |                                        | `false`  | The default Chronicle log type that will be sent.                                                                                                          |
| `override_log_type`             | bool              | `true`                                 | `false`  | Whether or not to override the `log_type` in the config with `attributes["log_type"]`.                                                                     |
| `validate_log_types`            | bool              | `false`                                | `false`  | Whether to validate configured log types against Chronicle via an API call on startup. Only applies to `https` protocol.                                   |
| `raw_log_field`                 | string            |                                        | `false`  | An OTTL expression for the field that will be used as the raw log payload.                                                                                 |
| `namespace`                     | string            |                                        | `false`  | User-configured environment namespace to identify the data domain the logs originated from.                                                                |
| `compression`                   | string            | `none`                                 | `false`  | The compression type to use when sending logs. Valid values are `none` and `gzip`.                                                                         |
| `ingestion_labels`              | map[string]string |                                        | `false`  | Key-value pairs of labels to be applied to the logs when sent to Chronicle.                                                                                |
| `collect_agent_metrics`         | bool              | `true`                                 | `false`  | Enables collecting metrics about the agent's process and log ingestion metrics.                                                                            |
| `metrics_interval`              | duration          | `5m`                                   | `false`  | The interval at which agent metrics are collected and sent. Only applies when `collect_agent_metrics` is `true`.                                           |
| `batch_request_size_limit_grpc` | int               | `4000000`                              | `false`  | The maximum size, in bytes, allowed for a gRPC batch creation request.                                                                                     |
| `batch_request_size_limit_http` | int               | `4000000`                              | `false`  | The maximum size, in bytes, allowed for an HTTPS batch creation request.                                                                                   |
| `license_type`                  | string            |                                        | `false`  | The license type of the Bindplane instance managing this agent. Used to determine the collector ID for Chronicle.                                          |
| `log_errored_payloads`          | bool              | `false`                                | `false`  | Whether to log payloads that fail to send. Useful for debugging.                                                                                           |
| `forwarder`                     | string            |                                        | `false`  | **Deprecated as of v1.87.1.** The forwarder (Collector ID) is now determined by the license type.                                                          |

### Log Type

If the `attributes["log_type"]` field is present in the log, and maps to a known Chronicle `log_type` the exporter will use the value of that field as the log type. If the `attributes["log_type"]` field is not present, the exporter will use the value of the `log_type` configuration field as the log type.

Currently supported `attributes["log_type"]` values that map to a Chronicle log type are:

- `windows_event.security` → `WINEVTLOG`
- `windows_event.application` → `WINEVTLOG`
- `windows_event.system` → `WINEVTLOG`
- `sql_server` → `MICROSOFT_SQL`

If the `attributes["chronicle_log_type"]` field is present in the log, its value will be used in the payload instead of the automatic detection or the `log_type` in the config.

### Namespace and Ingestion Labels

If the `attributes["chronicle_namespace"]` field is present in the log, its value will be used in the payload instead of the `namespace` in the config.

If there are nested fields in `attributes["chronicle_ingestion_label"]`, those values will be used in the payload instead of the `ingestion_labels` in the config.

## Credentials

This exporter requires a Google Cloud service account with access to the Chronicle API. The service account must have access to the endpoint specified in the config.

For additional information on accessing Chronicle, see the [Chronicle documentation](https://cloud.google.com/chronicle/docs/reference/ingestion-api#getting_api_authentication_credentials).

## Regional Endpoints

For `gRPC`, regional endpoints for the Backstory Ingestion API are listed [here](https://cloud.google.com/chronicle/docs/reference/ingestion-api#regional_endpoints).

For `https`, regional endpoints for the Chronicle API are listed [here](https://docs.cloud.google.com/chronicle/docs/reference/rest#regional-service-endpoint).

## Log Batch Creation Request Limits

`batch_request_size_limit_grpc` and `batch_request_size_limit_http` are used to ensure log batch creation requests don't exceed Chronicle's backend limits — the former for the `gRPC` protocol, and the latter for the `https` protocol. If a request exceeds the configured size limit, it will be split into multiple requests that adhere to this limit, with each request containing a subset of the logs from the original. Any single log that on its own exceeds the size limit will be dropped.

## Agent Metrics

When `collect_agent_metrics` is `true` (the default), the exporter periodically reports collector process and ingestion stats to Chronicle, surfacing metrics in GCP's Metrics Explorer which can be found nested under the "Chronicle Collector" resource and the "Agent" metric category.

Every `metrics_interval` (default `5m`), the exporter samples process CPU seconds, resident memory (RSS), and uptime, combines them with counts of logs accepted and refused since the last successful upload, and sends them to Chronicle. On success the window is reset; on failure it is retained so the next tick re-attempts with the accumulated counts.

Setting `collect_agent_metrics: false` disables the reporter without affecting log delivery.

## Example Configurations

### Basic Configuration (gRPC / Backstory Ingestion API)

```yaml
chronicle:
  creds_file_path: "/path/to/google/creds.json"
  log_type: "ABSOLUTE"
  customer_id: "customer-123"
```

### Basic Configuration with Regional Endpoint (gRPC)

```yaml
chronicle:
  endpoint: europe-malachiteingestion-pa.googleapis.com
  creds_file_path: "/path/to/google/creds.json"
  log_type: "ONEPASSWORD"
  customer_id: "customer-123"
```

### Basic Configuration (HTTPS / Chronicle API — recommended)

```yaml
chronicle:
  protocol: https
  endpoint: chronicle.googleapis.com
  location: us
  project: my-gcp-project
  customer_id: "customer-123"
  creds_file_path: "/path/to/google/creds.json"
  log_type: "ABSOLUTE"
```

### HTTPS with a Regional Endpoint

The `https` protocol prefixes `endpoint` with the value of `location` to build the regional URL. The example below targets `https://europe-chronicle.googleapis.com/...`:

```yaml
chronicle:
  protocol: https
  endpoint: chronicle.googleapis.com
  location: europe
  project: my-gcp-project
  customer_id: "customer-123"
  creds_file_path: "/path/to/google/creds.json"
  log_type: "ABSOLUTE"
```

### HTTPS with a Custom Endpoint (override_endpoint)

Set `override_endpoint: true` to use the `endpoint` value verbatim instead of prefixing it with `location`. `location` is still required because it is included in the request URL path and API calls.

```yaml
chronicle:
  protocol: https
  endpoint: chronicle.us.rep.googleapis.com
  override_endpoint: true
  location: us
  project: my-gcp-project
  customer_id: "customer-123"
  creds_file_path: "/path/to/google/creds.json"
  log_type: "ABSOLUTE"
```

### HTTPS with the v1beta API Version

```yaml
chronicle:
  protocol: https
  endpoint: chronicle.googleapis.com
  location: us
  project: my-gcp-project
  customer_id: "customer-123"
  api_version: v1beta
  creds_file_path: "/path/to/google/creds.json"
  log_type: "ABSOLUTE"
```

### Configuration with Ingestion Labels

```yaml
chronicle:
  creds_file_path: "/path/to/google/creds.json"
  log_type: ""
  customer_id: "customer-123"
  ingestion_labels:
    env: dev
    zone: USA
```
