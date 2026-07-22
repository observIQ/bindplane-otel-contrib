# Topology Processor
This processor utilizes request headers to provide extended topology functionality in Bindplane.

## Minimum agent versions
- Introduced: [v1.68.0](https://github.com/observIQ/bindplane-otel-collector/releases/tag/v1.68.0)

## Supported pipelines
- Logs
- Metrics
- Traces

## How It Works
1. When a collector sends telemetry to another collector (a "gateway"), it can attach the `X-Bindplane-Organization-ID`, `X-Bindplane-Account-ID`, `X-Bindplane-Configuration`, and `X-Bindplane-Resource-Name` request headers identifying itself.
2. For each batch of telemetry, this processor reads those headers from the request metadata. If all four are present, it records a route from the sending resource to this collector in an in-memory route table, along with the time the route was last seen. Telemetry passes through unmodified.
3. On startup, the processor registers its topology state with the bindplane extension named by `bindplane_extension`, which reports the collected topology to Bindplane. If no extension is configured, topology state is not reported.
4. Only one instance of the processor exists per processor ID, so topology is tracked once even when the same processor is used across multiple pipelines or signal types.

## Configuration
| Field                 | Type     | Default | Required | Description                                                                                                     |
|-----------------------|----------|---------|----------|-----------------------------------------------------------------------------------------------------------------|
| `configuration`       | string   |         | `true`   | The name of the Bindplane configuration where this processor is running.                                        |
| `organizationID`      | string   |         | `true`   | The Organization ID of the Bindplane configuration where this processor is running.                             |
| `accountID`           | string   |         | `true`   | The Account ID of the Bindplane configuration where this processor is running.                                  |
| `bindplane_extension` | component ID | | `false`  | The component ID of the bindplane extension to register topology state with. If unset, topology is not reported. |
| `interval`            | duration |         | `false`  | Deprecated. Only used by topology processor v1.75.0 and earlier; kept for backwards compatibility with Bindplane < v1.90.0. |

### Example configuration

```yaml
receivers:
  file_log:
    include: ["/var/log/*.log"]

extensions:
  bindplane:

processors:
  topology:
    configuration: "myConfiguration"
    organizationID: "myOrganizationID"
    accountID: "myAccountID"
    bindplane_extension: bindplane

exporters:
  googlecloud:

service:
  extensions:
    - bindplane
  pipelines:
    logs:
      receivers:
        - file_log
      processors:
        - topology
      exporters:
        - googlecloud
```
