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

Route collection always works the same way; on startup each processor takes exactly one
reporting path, decided by the configuration the Bindplane server rendered:

1. **`opamp` is set** (current Bindplane servers): the processor registers its topology state
   with a reporter shared by every `opamp`-configured topology processor in the collector —
   the first of them to start creates it, the last to shut down tears it down. The reporter
   registers the `com.bindplane.topology` custom capability with the referenced opamp
   extension and sends one aggregated custom message for those processors every interval —
   the same payload the bindplane extension produced. The reporter's settings come from the
   `global` block carried by one processor (defaults otherwise); if more than one processor
   carries a `global` block, or processors reference different `opamp` extensions, the last
   one to start wins and reconfigures the reporter. Reporting is disabled if the interval is
   `0`. The extension must exist and support custom messages, otherwise the collector fails
   to start. Works with both the upstream `opampextension` and the `opamp_connection`
   extension in self-managed distributions. If `bindplane_extension` is also set, it is
   ignored with a warning.
2. **Only `bindplane_extension` is set** (deprecated; older Bindplane servers): if the
   referenced extension exists in the configuration, the processor registers its topology
   state with it and the extension owns the reporting loop; an extension that exists but is
   not a topology registry fails startup. If the extension is not in the configuration at
   all — older Bindplane servers render this field without instantiating the extension — the
   processor falls back to case 3.
3. **Neither is set** (v1 bindplane agents, or standalone collectors): the processor registers
   its topology state with a package-level registry that the v1 bindplane agent runtime reads
   and reports from. This never fails startup — outside a v1 agent the registration is simply
   inert and topology is not reported.

A processor on path 2 or 3 does not feed the shared reporter: only `opamp`-configured
processors appear in the aggregated opamp message. A `global` block on a processor without
`opamp` is ignored with a warning.

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
