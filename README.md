# bindplane-otel-contrib

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

Contrib components (receivers, processors, exporters, extensions) for the [BindPlane OpenTelemetry Collector](https://github.com/observIQ/bindplane-otel-collector).

## Directory Structure

```
receiver/           Custom receivers
processor/          Custom processors
exporter/           Custom exporters
extension/          Custom extensions
internal/           Shared internal packages
  aws/              AWS utilities (S3/SQS client, backoff, event types)
  azureblob/        Azure Blob Storage client interfaces
  blobconsume/      Checkpoint mechanism for blob-consuming receivers
  exporterutils/    Utility functions for exporters
  measurements/     Throughput measurement management
  storageclient/    Data storage interfaces and implementations
  testutils/        Test utilities and helpers
  tools/            Build tools and development dependencies
pkg/                Public utility packages
  counter/          Telemetry counting grouped by resource and attributes
  expr/             Expression evaluation against OTEL data structures
  osinfo/           OS information retrieval
  snapshot/         Snapshot collection and filtering
  version/          Collector version data set at compile time
```

## Components

### Receivers

| Component | Description |
|---|---|
| [awss3eventreceiver](receiver/awss3eventreceiver) | Consumes S3 event notifications for object creation events and emits the S3 object as the body of a log record |
| [awss3rehydrationreceiver](receiver/awss3rehydrationreceiver) | Rehydrates OTLP data from AWS S3 that was stored using the AWS S3 exporter |
| [azureblobpollingreceiver](receiver/azureblobpollingreceiver) | Continuously polls Azure Blob Storage at configurable intervals for new data |
| [azureblobrehydrationreceiver](receiver/azureblobrehydrationreceiver) | Rehydrates OTLP data from Azure Blob Storage that was stored using the Azure Blob exporter |
| [bindplaneauditlogs](receiver/bindplaneauditlogs) | Collects audit logs from a BindPlane instance via API |
| [gcspubsubeventreceiver](receiver/gcspubsubeventreceiver) | Consumes GCS event notifications via Google Cloud Pub/Sub and emits object contents as log records |
| [googlecloudstoragerehydrationreceiver](receiver/googlecloudstoragerehydrationreceiver) | Rehydrates OTLP data from Google Cloud Storage that was stored using the GCS exporter |
| [httpreceiver](receiver/httpreceiver) | Collects logs from services via HTTP |
| [m365receiver](receiver/m365receiver) | Receives metrics and logs from Microsoft 365 via the Microsoft Graph and Management APIs |
| [oktareceiver](receiver/oktareceiver) | Collects logs from an Okta domain |
| [pcapreceiver](receiver/pcapreceiver) | Captures network packets and emits them as OpenTelemetry logs |
| [pluginreceiver](receiver/pluginreceiver) | Runs templated OpenTelemetry pipelines stored within a plugin |
| [restapireceiver](receiver/restapireceiver) | Pulls data from any REST API endpoint, supporting logs and metrics with configurable auth and pagination |
| [routereceiver](receiver/routereceiver) | Receives telemetry routed from other pipelines (logs, metrics, traces) |
| [sapnetweaverreceiver](receiver/sapnetweaverreceiver) | Collects metrics from SAP NetWeaver via the SAPControl Web Service Interface |
| [splunksearchapireceiver](receiver/splunksearchapireceiver) | Collects Splunk events using the Splunk Search API for historical data migration |
| [telemetrygeneratorreceiver](receiver/telemetrygeneratorreceiver) | Generates synthetic telemetry for testing and configuration purposes |
| [windowseventtracereceiver](receiver/windowseventtracereceiver) | Collects Event Tracing for Windows (ETW) events *(experimental)* |

### Processors

| Component | Description |
|---|---|
| [datapointcountprocessor](processor/datapointcountprocessor) | Converts the number of datapoints received during an interval into a metric |
| [logcountprocessor](processor/logcountprocessor) | Converts the number of logs received during an interval into a metric |
| [lookupprocessor](processor/lookupprocessor) | Looks up values in a CSV file and adds matching record values to telemetry context |
| [maskprocessor](processor/maskprocessor) | Detects and masks sensitive data using configurable regex rules |
| [metricextractprocessor](processor/metricextractprocessor) | Extracts metrics from logs |
| [metricstatsprocessor](processor/metricstatsprocessor) | Calculates statistics from metrics over a configurable interval for sampling or volume reduction |
| [ocsfstandardizationprocessor](processor/ocsfstandardizationprocessor) | Creates JSON OCSF-compliant log bodies from OTEL logs *(alpha)* |
| [randomfailureprocessor](processor/randomfailureprocessor) | Tests error resiliency by randomly returning errors with configurable probability |
| [removeemptyvaluesprocessor](processor/removeemptyvaluesprocessor) | Removes empty values from telemetry attributes, resource attributes, and log record bodies |
| [resourceattributetransposerprocessor](processor/resourceattributetransposerprocessor) | Copies resource-level attributes to all individual logs or metric data points |
| [samplingprocessor](processor/samplingprocessor) | Samples incoming OTLP objects and drops those based on a configured drop ratio |
| [snapshotprocessor](processor/snapshotprocessor) | Stores telemetry temporarily in an internal buffer for snapshot functionality |
| [spancountprocessor](processor/spancountprocessor) | Converts the number of spans received during an interval into a metric |
| [throughputmeasurementprocessor](processor/throughputmeasurementprocessor) | Samples OTLP payloads and measures protobuf size and OTLP object counts |
| [topologyprocessor](processor/topologyprocessor) | Utilizes request headers to provide extended topology functionality in BindPlane |

### Exporters

| Component | Description |
|---|---|
| [awssecuritylakeexporter](exporter/awssecuritylakeexporter) | Exports OCSF-formatted logs as Parquet files to AWS Security Lake via S3 |
| [azureblobexporter](exporter/azureblobexporter) | Exports metrics, traces, and logs to Azure Blob Storage in OTLP JSON format |
| [azureloganalyticsexporter](exporter/azureloganalyticsexporter) | Exports logs to Azure Log Analytics via the Log Analytics Ingestion API |
| [chronicleexporter](exporter/chronicleexporter) | Sends logs to Chronicle using the v2 ingestion API |
| [chronicleforwarderexporter](exporter/chronicleforwarderexporter) | Forwards logs to a Chronicle Forwarder endpoint using Syslog or file-based methods |
| [googlecloudexporter](exporter/googlecloudexporter) | Sends metrics, traces, and logs to Google Cloud Monitoring |
| [googlecloudstorageexporter](exporter/googlecloudstorageexporter) | Exports logs, metrics, and traces to Google Cloud Storage in OTLP JSON format |
| [googlemanagedprometheusexporter](exporter/googlemanagedprometheusexporter) | Sends metrics to Google Cloud Managed Service for Prometheus |
| [qradar](exporter/qradar) | Forwards logs to a QRadar instance using its Syslog endpoint |
| [snowflakeexporter](exporter/snowflakeexporter) | Sends logs, metrics, and traces to Snowflake cloud data warehouse |
| [webhookexporter](exporter/webhookexporter) | Sends telemetry data to a webhook endpoint |

### Extensions

| Component | Description |
|---|---|
| [awss3eventextension](extension/awss3eventextension) | Downloads newly created S3 objects to a specified directory by reading from an SQS queue |
| [badgerextension](extension/badgerextension) | Provides persistent storage using BadgerDB |
| [bindplaneextension](extension/bindplaneextension) | Stores BindPlane-specific information for custom collector distributions |
| [opampgateway](extension/opampgateway) | Relays OpAMP messages between downstream agents and an upstream OpAMP server *(alpha)* |
| [pebbleextension](extension/pebbleextension) | Provides persistent storage using Pebble |

## Development

### Prerequisites

- Go (version specified in `internal/tools/go.mod`)

### Setup

```bash
# Install development tools
make install-tools

# Generate a go.work file for IDE support
./scripts/generate-gowork.sh
```

### Common Commands

```bash
# Run all tests
make test

# Run all CI checks (format, license, lint, gosec, test)
make ci-checks

# Build the collector using the local collector repo
make build-collector

# Lint
make lint

# Format code
make fmt

# Tidy all modules
make tidy
```

### Local Development with Collector

Create a `.local.env` file to configure the path to your local collector repo:

```bash
COLLECTOR_PATH=../bindplane-otel-collector
```

Then run `make build-collector` to build the collector binary with your local contrib changes.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
