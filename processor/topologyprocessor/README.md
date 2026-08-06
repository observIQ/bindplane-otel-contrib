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
3. On startup, the processor picks a reporting path based on the configuration (see [Startup behavior](#startup-behavior)).
4. Only one instance of the processor exists per processor ID, so topology is tracked once even when the same processor is used across multiple pipelines or signal types.

## Configuration
| Field                 | Type     | Default | Required | Description                                                                                                     |
|-----------------------|----------|---------|----------|-----------------------------------------------------------------------------------------------------------------|
| `configuration`       | string   |         | `true`   | The name of the Bindplane configuration where this processor is running.                                        |
| `organizationID`      | string   |         | `true`   | The Organization ID of the Bindplane configuration where this processor is running.                             |
| `accountID`           | string   |         | `true`   | The Account ID of the Bindplane configuration where this processor is running.                                  |
| `opamp`               | component ID | | `false`  | The component ID of an opamp extension implementing the custom message registry. When set, the reporter shared by every topology processor sends all topology state to Bindplane on the `com.bindplane.topology` capability. Every processor should reference the same extension. |
| `global`              | block    |         | `false`  | Settings for the shared reporter. Exactly one processor in a configuration should carry this block; the reporter uses the defaults below when no processor does. |
| `global.interval`     | duration | `1m` (when no block) | `false` | How often topology is reported over opamp. Reporting is disabled if set to `0`. |
| `bindplane_extension` | component ID | | `false`  | Deprecated; configure `opamp` instead. The component ID of a bindplane extension to register topology state with. Ignored when `opamp` is set. |
| `interval`            | duration |         | `false`  | Deprecated and unused. Only used by topology processor v1.75.0 and earlier; kept so configurations rendered by old Bindplane servers still unmarshal. |

### Startup behavior

Route collection always works the same way. On startup, every topology processor registers its
topology state with a reporter shared by every topology processor in the collector; the first
processor to start creates it, and the last one to shut down tears it down. What that reporter
does is decided by the configuration the Bindplane server rendered:

1. **`opamp` is set** (current Bindplane servers): the shared reporter registers the
   `com.bindplane.topology` custom capability with the referenced opamp extension and sends
   one aggregated custom message for all topology processors every interval — the same
   payload the bindplane extension produced. The reporter's settings come from the `global`
   block carried by one processor (defaults otherwise); if more than one processor carries a
   `global` block, or processors reference different `opamp` extensions, the last one to
   start wins and reconfigures the reporter. Reporting is disabled if the interval is `0`.
   The extension must exist and support custom messages, otherwise the collector fails to
   start. Works with both the upstream `opampextension` and the `opamp_connection` extension
   in self-managed distributions. If `bindplane_extension` is also set, it is ignored with a
   warning.
2. **`opamp` is not set on a processor**: its topology state still feeds the shared reporter,
   and the processor falls back to the deprecated paths: if `bindplane_extension` is set and
   the referenced extension exists, the processor registers its topology state with it and
   the extension owns the reporting loop (an extension that exists but is not a topology
   registry fails startup; a rendered but uninstantiated extension — older Bindplane servers
   do this — falls through to the next case). Otherwise the processor registers with a
   package-level registry that the v1 bindplane agent runtime reads and reports from — never
   fatal, and simply inert outside a v1 agent.

### Example configuration

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
  topology:
    configuration: "myConfiguration"
    organizationID: "myOrganizationID"
    accountID: "myAccountID"
    opamp: opamp
    global:
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
        - topology
      exporters:
        - googlecloud
```
