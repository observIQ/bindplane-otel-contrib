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

| Field                           | Type              | Default                                | Required | Description                                                                                                                                                                              |
| ------------------------------- | ----------------- | -------------------------------------- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `protocol`                      | string            | `gRPC`                                 | `false`  | The protocol used to send logs. Valid values are `gRPC` and `https`. See [Protocols and APIs](#protocols-and-apis).                                                                      |
| `endpoint`                      | string            | `malachiteingestion-pa.googleapis.com` | `false`  | The endpoint for sending to Chronicle. Must not contain a `http://` or `https://` prefix. For `https`, the endpoint is combined with `location` (e.g. `chronicle.googleapis.com`) unless `override_endpoint` is set. |
| `location`                      | string            |                                        | `false`  | The Chronicle region (e.g. `us`, `europe`). Required when `protocol` is `https`.                                                                                                         |
| `project`                       | string            |                                        | `false`  | The Google Cloud project ID. Required when `protocol` is `https`.                                                                                                                        |
| `customer_id`                   | string            |                                        | `false`  | The Chronicle customer (instance) ID used for sending logs. Required when `protocol` is `https`.                                                                                         |
| `api_version`                   | string            | `v1alpha`                              | `false`  | The Chronicle API version to use. Valid values are `v1alpha` and `v1beta`. Only applies to `https` protocol.                                                                             |
| `override_endpoint`             | bool              | `false`                                | `false`  | Whether to ignore the `location` field when constructing the endpoint. Only applies to `https` protocol.                                                                                 |
| `creds_file_path`               | string            |                                        | `false`  | The file path to the Google credentials JSON file. Mutually exclusive with `creds`. If neither is set, the exporter falls back to Google Application Default Credentials.                |
| `creds`                         | string            |                                        | `false`  | The Google credentials JSON. Mutually exclusive with `creds_file_path`. If neither is set, the exporter falls back to Google Application Default Credentials.                            |
| `log_type`                      | string            |                                        | `false`  | The default Chronicle log type that will be sent.                                                                                                                                        |
| `override_log_type`             | bool              | `true`                                 | `false`  | Whether or not to override the `log_type` in the config with `attributes["log_type"]`.                                                                                                   |
| `validate_log_types`            | bool              | `false`                                | `false`  | Whether to validate configured log types against Chronicle via an API call on startup. Only applies to `https` protocol.                                                                 |
| `raw_log_field`                 | string            |                                        | `false`  | An OTTL expression for the field that will be used as the raw log payload.                                                                                                               |
| `namespace`                     | string            |                                        | `false`  | User-configured environment namespace to identify the data domain the logs originated from.                                                                                              |
| `compression`                   | string            | `none`                                 | `false`  | The compression type to use when sending logs. Valid values are `none` and `gzip`.                                                                                                       |
| `ingestion_labels`              | map[string]string |                                        | `false`  | Key-value pairs of labels to be applied to the logs when sent to Chronicle.                                                                                                              |
| `collect_agent_metrics`         | bool              | `true`                                 | `false`  | Enables collecting metrics about the agent's process and log ingestion metrics.                                                                                                          |
| `metrics_interval`              | duration          | `5m`                                   | `false`  | The interval at which agent metrics are collected and sent. Only applies when `collect_agent_metrics` is `true`.                                                                         |
| `batch_request_size_limit_grpc` | int               | `4000000`                              | `false`  | The maximum size, in bytes, allowed for a gRPC batch creation request.                                                                                                                   |
| `batch_request_size_limit_http` | int               | `4000000`                              | `false`  | The maximum size, in bytes, allowed for an HTTPS batch creation request.                                                                                                                 |
| `log_errored_payloads`          | bool              | `false`                                | `false`  | Whether to log payloads that fail to send. Useful for debugging.                                                                                                                         |
| `http_response_header_timeout`  | duration          | `10s`                                  | `false`  | The amount of time to wait for the HTTP response headers after sending requests to Chronicle. Only applies to `https` protocol.                                                          |
| `http_version`                  | string            | `2`                                    | `false`  | The HTTP version used to send logs. Valid values are `1.1` and `2`. `2` (HTTP/2) multiplexes every request over a single connection to Chronicle, which becomes a bottleneck under high throughput; setting `1.1` opens a connection pool instead, giving upload parallelism across consumers. See [High-Throughput Tuning](#high-throughput-tuning-https). Only applies to `https` protocol. |
| `max_idle_conns`                | int               | `100`                                  | `false`  | The total number of idle (keep-alive) connections kept across all hosts. Most relevant with `http_version: 1.1`. `0` means no limit. Only applies to `https` protocol.                   |
| `max_idle_conns_per_host`       | int               | `10`                                   | `false`  | The number of idle (keep-alive) connections retained per host. With `http_version: 1.1`, raise this toward `sending_queue.num_consumers` so connections are reused. Only applies to `https` protocol. |
| `max_conns_per_host`            | int               | `0` (unlimited)                        | `false`  | Caps the total number of connections per host (dialing + active + idle). When reached, further requests block until a connection frees instead of opening new ones. `0` means no limit. Use with `http_version: 1.1` to bound connection count under high load; keep `max_idle_conns_per_host` at or above this value to avoid connection churn. Only applies to `https` protocol. |

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

Any labels defined by key value pairs in a nested map at `attributes["chronicle_ingestion_label"]` are merged with the `ingestion_labels` in the config. When the same label key is set in both places, the value from the attribute takes precedence over that from the config.

## Credentials

This exporter requires a Google Cloud service account with access to the Chronicle API. The service account must have access to the endpoint specified in the config.

For additional information on accessing Chronicle, see the [Chronicle documentation](https://cloud.google.com/chronicle/docs/reference/ingestion-api#getting_api_authentication_credentials).

## Regional Endpoints

For `gRPC`, regional endpoints for the Backstory Ingestion API are listed [here](https://cloud.google.com/chronicle/docs/reference/ingestion-api#regional_endpoints).

For `https`, regional endpoints for the Chronicle API are listed [here](https://docs.cloud.google.com/chronicle/docs/reference/rest#regional-service-endpoint).

## Log Batch Creation Request Limits

`batch_request_size_limit_grpc` and `batch_request_size_limit_http` are used to ensure log batch creation requests don't exceed Chronicle's backend limits — the former for the `gRPC` protocol, and the latter for the `https` protocol. If a request exceeds the configured size limit, it will be split into multiple requests that adhere to this limit, with each request containing a subset of the logs from the original. Any single log that on its own exceeds the size limit will be dropped.

Splitting is reported by the `otelcol_exporter_payload_splits` metric (the number of *additional* requests created by splitting). A non-zero value means an upstream batch is larger than `batch_request_size_limit_http`; lower the `batch` processor's `send_batch_size` until it reaches zero. Avoiding splitting keeps each upstream batch a single HTTP request, which both reduces overhead and minimizes the duplicate-on-restart window described below.

## Retry Behavior (HTTPS)

For the `https` protocol the exporter retries **individual HTTP requests** itself rather than using the collector's `retry_on_failure` middleware. When a batch is split into several requests, a transient failure retries only the request that failed — the batch is not re-marshaled and requests that already succeeded are not re-sent. `retry_on_failure` (backoff intervals, `max_elapsed_time`) and `timeout` still apply, but per request: `timeout` bounds each attempt and `retry_on_failure` governs the backoff between attempts. A request that exhausts its retries or receives a non-retryable response is dropped and counted in `otelcol_exporter_logs_send_failed`.

This avoids the duplicate log entries that whole-batch retries would otherwise create. Duplicates are still possible if the collector restarts mid-send, because the entire batch is re-read from the sending queue and re-sent; keeping batches under `batch_request_size_limit_http` (watch `otelcol_exporter_payload_splits`) minimizes that exposure.

The `gRPC` protocol continues to use the standard `retry_on_failure` and `timeout` middleware.

## High-Throughput Tuning (HTTPS)

The `https` exporter defaults to **HTTP/2** (`http_version: 2`). Go's HTTP/2 client multiplexes **every** concurrent request over a **single** TCP connection per host. For high-volume sources that single connection becomes the throughput ceiling: large request bodies serialize behind the connection's write lock and HTTP/2 flow-control window, so adding `sending_queue.num_consumers` does not add real upload parallelism — the extra consumers simply queue behind one connection and, if requests time out, retry into the same saturated connection. Setting `http_version: 1.1` switches to a *pool* of connections (one request per connection at a time), giving real upload parallelism across consumers.

If you are sending tens of GB/hr to a single endpoint and see export latency climb, the persistent queue grow, and CPU spike while the Chronicle API itself reports low latency, the default HTTP/2 connection ceiling is the likely cause. Recommended settings:

1. **`compression: gzip`** — log text typically compresses ~10x, drastically reducing bytes on the wire. This is usually the single highest-impact change.
2. **`http_version: "1.1"`** — switches from the default HTTP/2 to a connection pool rather than multiplexing over one HTTP/2 connection.
3. **`max_idle_conns_per_host`** — set this at or above `sending_queue.num_consumers` so connections are reused (kept alive) rather than re-dialed on every request. Too low a value here is the usual cause of runaway connection *churn* under HTTP/1.1: connections are closed right after each request and re-opened, leaving many sockets in `TIME_WAIT`.
4. **`max_conns_per_host`** — caps the total connections per host. Under HTTP/1.1 the pool is otherwise unbounded, so set this if you observe excessive connections under load. When the cap is hit, requests block for a free connection (back-pressure) instead of opening more. Keep `max_idle_conns_per_host` at or above this value, or capped connections will churn.
5. **`max_idle_conns`** — the total keep-alive pool across hosts; keep it at or above `max_idle_conns_per_host`.

```yaml
chronicle:
  protocol: https
  endpoint: chronicle.googleapis.com
  location: us
  project: my-gcp-project
  customer_id: "customer-123"
  creds_file_path: "/path/to/google/creds.json"
  log_type: "FORTINET_FIREWALL"
  compression: gzip
  http_version: "1.1"
  max_conns_per_host: 100
  max_idle_conns: 200
  max_idle_conns_per_host: 100
  sending_queue:
    num_consumers: 20
```

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
