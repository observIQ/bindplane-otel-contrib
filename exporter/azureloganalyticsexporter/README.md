# Azure Log Analytics Exporter

This exporter sends logs to Azure Log Analytics via the [Log Analytics Ingestion API](https://learn.microsoft.com/en-us/azure/azure-monitor/logs/logs-ingestion-api-overview). The output format depends on the log body type and the `raw_log_field` configuration:

- **Structured (default, map body):** Each key in the log body becomes a top-level JSON field, mapping directly to a column in your Log Analytics table. No `RawData` wrapper. Metadata fields (`TimeGenerated`, `SeverityText`, `SeverityNumber`, `TraceId`, `SpanId`) are included automatically.
- **Unstructured (default, string body):** String-body logs are wrapped in `{"RawData": "<string>", "TimeGenerated": "...", ...}` so ingestion works with tables that have a `RawData` column.
- **Raw Log Mode (`raw_log_field` set):** Extracts the specified field via an OTTL expression and sends `{"RawData": "<extracted value>"}`.

## Minimum Agent Versions

- Introduced: v1.75.0

## Supported Pipelines

- Logs

## How It Works

This exporter sends logs to Azure Log Analytics using the [Log Analytics Ingestion API](https://learn.microsoft.com/en-us/azure/azure-monitor/logs/logs-ingestion-api-overview). Before using the exporter, you must configure a Data Collection Rule (DCR) or Data Collection Endpoint (DCE) and a custom table within your Log Analytics workspace.

The required schema for the custom table depends on the log body type and the `raw_log_field` configuration option:

- **Structured JSON (default, map body):** If `raw_log_field` is _not_ specified and the log body is a map, each key in the body is sent as a top-level JSON field. Your custom table columns should match the keys in your log data. For example, a log body `{"source_ip": "10.0.0.1", "action": "ALLOW"}` produces:
  ```json
  [{"source_ip": "10.0.0.1", "action": "ALLOW", "TimeGenerated": "2025-01-01T00:00:00Z", "SeverityText": "INFO", "SeverityNumber": 9}]
  ```
- **Unstructured fallback (default, string body):** If `raw_log_field` is _not_ specified and the log body is a plain string, the exporter wraps it in a `RawData` field:
  ```json
  [{"RawData": "plain text log message", "TimeGenerated": "2025-01-01T00:00:00Z", "SeverityText": "INFO", "SeverityNumber": 9}]
  ```
- **Raw Log Mode:** If `raw_log_field` _is_ specified, the exporter extracts the designated field via an OTTL expression and wraps it in `RawData`:
  ```json
  [{"RawData": "<extracted field value>"}]
  ```

In all cases, `TimeGenerated` is included automatically (required by Azure).

## Configuration

| Field            | Type   | Default | Required | Description                                                                                                                  |
| ---------------- | ------ | ------- | -------- | ---------------------------------------------------------------------------------------------------------------------------- |
| endpoint         | string |         | ✓        | The DCR logs ingestion endpoint URL, or a DCE logs ingestion endpoint URL if your DCR does not expose one (see [Endpoint Configuration](#endpoint-configuration)) |
| client_id        | string |         | ✓        | Azure client ID for authentication                                                                                           |
| raw_log_field    | string | ""      |          | OTTL expression for the log field to extract and send as RawData. When empty, structured JSON is sent for map bodies and RawData for string bodies |
| client_secret    | string |         | ✓        | Azure client secret for authentication                                                                                       |
| tenant_id        | string |         | ✓        | Azure tenant ID for authentication                                                                                           |
| rule_id          | string |         | ✓        | Data Collection Rule (DCR) ID or immutableId                                                                                 |
| stream_name      | string |         | ✓        | The stream name as defined in your DCR. **Must be prefixed with `Custom-`** for custom tables (e.g., `Custom-MyTable_CL`)    |
| timeout          | string |         |          | See [doc](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/exporterhelper/README.md) for details |
| sending_queue    | map    |         |          | See [doc](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/exporterhelper/README.md) for details |
| retry_on_failure | map    |         |          | See [doc](https://github.com/open-telemetry/opentelemetry-collector/blob/main/exporter/exporterhelper/README.md) for details |

## Example Configurations

```yaml
exporters:
  azureloganalytics:
    endpoint: "<your-log-ingestion-endpoint>"
    client_id: "<your-client-id>"
    client_secret: "<your-client-secret>"
    tenant_id: "<your-tenant-id>"
    raw_log_field: body
    rule_id: "<your-dcr-id>"
    stream_name: "<your-stream-name>"
```

### Minimal Configuration

```yaml
exporters:
  azureloganalytics:
    endpoint: "<your-log-ingestion-endpoint>"
    client_id: "<your-client-id>"
    client_secret: "<your-client-secret>"
    tenant_id: "<your-tenant-id>"
    rule_id: "<your-dcr-id>"
    stream_name: "<your-stream-name>"
```

This configuration shows the minimum required fields to export logs to Azure Log Analytics. All fields are required for the exporter to function properly.

### Configuration with Queue and Retry

```yaml
exporters:
  azureloganalytics:
    endpoint: "<your-log-ingestion-endpoint>"
    client_id: "<your-client-id>"
    client_secret: "<your-client-secret>"
    tenant_id: "<your-tenant-id>"
    rule_id: "<your-dcr-id>"
    stream_name: "<your-stream-name>"
    timeout: 30s
    sending_queue:
      queue_size: 1000
      enabled: true
    retry_on_failure:
      enabled: true
      initial_interval: 5s
      max_interval: 30s
      max_elapsed_time: 300s
```

## Setup

Before configuring the exporter, you'll need to set up several components in the Azure portal:

### 1. Create an Azure AD Application (not needed if you already have one)

1. Navigate to Azure Active Directory > App registrations
2. Click "New registration"
3. Give your application a name
4. Select supported account types (usually "Single tenant")
5. Click "Register"
6. After creation, note down the following:
   - Application (client) ID
   - Directory (tenant) ID
7. Under "Certificates & secrets":
   - Create a new client secret
   - Copy the secret value immediately (you won't be able to see it again)

### 2. Create a Log Analytics Workspace Table (not needed if you already have one setup)

1.  Go to your Log Analytics workspace
2.  Navigate to "Tables" under Settings
3.  Click "New Custom Table"
4.  Configure your table:

    - Give it a name (this will be the display name in the Azure portal). **Important:** The actual `stream_name` value used in the exporter configuration must be prefixed with `Custom-`. For example, if you name the table `my_logs` in the portal, the `stream_name` configuration value should be `Custom-my_logs`.
    - Select "JSON" as the data format
    - Provide an example schema based on your log format:
      - **Structured (map body, `raw_log_field` not set):** Your table columns should match the keys in your log body, plus metadata fields:
        ```json
        [
          {
            "source_ip": "10.0.0.1",
            "action": "ALLOW",
            "bytes": 1234,
            "TimeGenerated": "2025-01-01T00:00:00Z",
            "SeverityText": "INFO",
            "SeverityNumber": 9
          }
        ]
        ```
      - **Unstructured (string body, `raw_log_field` not set):** Your table needs a `RawData` column:
        ```json
        [
          {
            "RawData": "Sample log entry content",
            "TimeGenerated": "2025-01-01T00:00:00Z",
            "SeverityText": "INFO",
            "SeverityNumber": 9
          }
        ]
        ```
      - **Raw Log Mode (`raw_log_field` set):** Your table needs a `RawData` column:
        ```json
        [
          {
            "RawData": "Sample log entry content"
          }
        ]
        ```

5.  Click "Create"

### 3. Create a Data Collection Rule (DCR)

1. Navigate to Microsoft Sentinel
2. Go to Settings > Data Collection Rules
3. Click "Create"
4. Configure the DCR:
   - Select your subscription and resource group
   - Choose your Log Analytics workspace
   - Select the custom table you created
   - Set up any necessary transformations
5. After creation, note down:
   - The Rule ID (will be your `rule_id`)
   - The endpoint URL (see [Endpoint Configuration](#endpoint-configuration) below)

### 4. Set up Permissions

1. Go to your DCR
2. Navigate to "Access control (IAM)"
3. Add a role assignment:
   - Role: "Monitoring Metrics Publisher"
   - Assign access to: User, group, or service principal
   - Select your previously created Azure AD application (you may need to use the search functionality to find it)
4. Repeat the same for the Log Analytics workspace resource if needed.

Now you have all the required information to configure the exporter:

- `endpoint`: The logs ingestion endpoint URL (see below)
- `client_id`: The Application (client) ID
- `client_secret`: The secret value you created
- `tenant_id`: The Directory (tenant) ID
- `rule_id`: The DCR Rule ID
- `stream_name`: The stream name from your DCR (must be prefixed with `Custom-` for custom tables)

### Endpoint Configuration

The `endpoint` field requires a logs ingestion endpoint URL. There are two ways to obtain this:

1. **From the DCR directly:** Open your DCR in the Azure portal, click "JSON View", and look for `properties.endpoints.logsIngestion`. If present, use this URL as the `endpoint` value. If the field is missing, try switching to a newer API version (e.g., `2023-03-11`) in the JSON view.

2. **From a Data Collection Endpoint (DCE):** If your DCR does not expose a `logsIngestion` endpoint (common with older DCRs or certain configurations), you must create a separate [Data Collection Endpoint (DCE)](https://learn.microsoft.com/en-us/azure/azure-monitor/essentials/data-collection-endpoint-overview) and use its logs ingestion endpoint URL instead. After creating the DCE, associate it with your DCR.

For more information, see the [Logs Ingestion API overview](https://learn.microsoft.com/en-us/azure/azure-monitor/logs/logs-ingestion-api-overview).

## Important Notes

- The first export of logs may take anywhere from 5-15 minutes on a freshly created table.
- The `stream_name` **must** be prefixed with `Custom-` for custom log tables (e.g., `Custom-MyTable_CL`). Omitting this prefix will cause silent ingestion failures.
- Transient HTTP errors (429, 502, 503, 504) are automatically retried. Permanent errors (400, 401, 403) are not retried.
