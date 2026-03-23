# Google SecOps Exporter

This exporter facilitates the sending of logs to [Google SecOps](https://cloud.google.com/security/products/security-operations) (previously Chronicle), a security analytics platform provided by Google. It is designed to integrate with OpenTelemetry collectors to export logs Google SecOps.

## Supported APIs

This exporter supports sending logs to Google SecOps using either of the following APIs
- [Chronicle API](https://docs.cloud.google.com/chronicle/docs/reference/ingestion-methods) (Preferred)
- [(Backstory) Ingestion API](https://docs.cloud.google.com/chronicle/docs/reference/ingestion-api)

## How It Works

1. The exporter uses the configured credentials to authenticate with the Google Cloud services.
2. Logs are marshalled into the format expected by Google SecOps.
3. Logs are imported into Google SecOps using the appropriate endpoint.

## Configuration

The exporter can be configured using the following fields:

| Field                           | Type              | Default                                | Required | Description                                                                                 |
| ------------------------------- | ----------------- | -------------------------------------- | -------- | ------------------------------------------------------------------------------------------- |
| `endpoint`                      | string            | `malachiteingestion-pa.googleapis.com` | `false`  | The Endpoint for sending to chronicle.                                                      |
| `creds_file_path`               | string            |                                        | `true`   | The file path to the Google credentials JSON file.                                          |
| `creds`                         | string            |                                        | `true`   | The Google credentials JSON.                                                                |
| `log_type`                      | string            |                                        | `false`  | The type of log that will be sent.                                                          |
| `raw_log_field`                 | string            |                                        | `false`  | The field name for raw logs.                                                                |
| `customer_id`                   | string            |                                        | `false`  | The customer ID used for sending logs.                                                      |
| `override_log_type`             | bool              | `true`                                 | `false`  | Whether or not to override the `log_type` in the config with `attributes["log_type"]`       |
| `namespace`                     | string            |                                        | `false`  | User-configured environment namespace to identify the data domain the logs originated from. |
| `compression`                   | string            | `none`                                 | `false`  | The compression type to use when sending logs. valid values are `none` and `gzip`           |
| `ingestion_labels`              | map[string]string |                                        | `false`  | Key-value pairs of labels to be applied to the logs when sent to chronicle.                 |
| `collect_agent_metrics`         | bool              | `true`                                 | `false`  | Enables collecting metrics about the agent's process and log ingestion metrics              |
| `batch_request_size_limit_grpc` | int               | `4000000`                              | `false`  | The maximum size, in bytes, allowed for a gRPC batch creation request.                      |
| `batch_request_size_limit_http` | int               | `4000000`                              | `false`  | The maximum size, in bytes, allowed for a HTTP batch creation request.                      |
| `api_version`                   | string            | `v1alpha`                              | `false`  | The API version to use. Valid values are `v1alpha` and `v1beta`. Only applies to HTTPS protocol.                           |
| `override_endpoint`             | bool              | `false`                                | `false`  | Whether or not to ignore the Location field when constructing the endpoint. Only applies to HTTPS protocol.                  |

### Log Type

If the `attributes["log_type"]` field is present in the log, and maps to a known Chronicle `log_type` the exporter will use the value of that field as the log type. If the `attributes["log_type"]` field is not present, the exporter will use the value of the `log_type` configuration field as the log type.

currently supported log types are:

- windows_event.security
- windows_event.custom
- windows_event.application
- windows_event.system
- sql_server

If the `attributes["secops_log_type"]` field is present in the log, its value will be used as the Log Type in the payload instead of the automatic detection or the `log_type` in the config.

### Namespace and Ingestion Labels

If the `attributes["secops_namespace"]` or `attributes["chronicle_namespace"]` field is present in the log, its value will be used as the Namespace in the payload instead of the `namespace` in the config.

If there are nested fields in `attributes["chronicle_ingestion_label"]`, we will use the values in the payload instead of the `ingestion_labels` in the config.

## Credentials

This exporter requires a Google Cloud service account with access to the appropiate APIs. The service account must have access to the API specfied in the config.

The following IAM permissions required for the Chronicle API:
- [chronicle.logs.import](https://docs.cloud.google.com/chronicle/docs/reference/rest/v1alpha/projects.locations.instances.logTypes.logs/import)
- [chronicle.forwarders.importStatsEvents](https://docs.cloud.google.com/chronicle/docs/reference/rest/v1alpha/projects.locations.instances.forwarders/importStatsEvents) When running the exporter with `collect_agent_metrics` enabled.
- [chronicle.logTypes.list](https://docs.cloud.google.com/chronicle/docs/reference/rest/v1alpha/projects.locations.instances.logTypes/list) When running the exporter with `validate_log_types` enabled.

Besides the default base URL, there are also regional base URLs that can be used:
- For the [Chronicle API](https://docs.cloud.google.com/chronicle/docs/reference/rest?rep_location=us#regional-service-endpoint)
- For the [Backstory API](https://docs.cloud.google.com/chronicle/docs/reference/ingestion-api#regional_endpoints)

For additional information on credentials, see the relevant documentation:
- For the [Chronicle API](https://docs.cloud.google.com/chronicle/docs/reference/authentication)
- For the [Backstory API](https://docs.cloud.google.com/chronicle/docs/reference/ingestion-api#getting_api_authentication_credentials).

## Log Batch Creation Request Limits

`batch_request_size_limit` is used to ensure log batch creation requests don't exceed Google SecOps's backend limits. If a request exceeds the configured size limit, the request will be split into multiple requests that adhere to this limit, with each request containing a subset of the logs contained in the original request. Any single logs that result in the request exceeding the size limit will be dropped.

## Example Configuration

### Basic Chronicle API Configuration

```yaml
googlesecops:
  api: "chronicle"
  hostname: chronicle.googleapis.com
  creds_file_path: "/path/to/google/creds.json"
  log_type: "ONEPASSWORD"
  customer_id: "customer-123"
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
