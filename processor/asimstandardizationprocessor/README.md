# ASIM Standardization Processor

**Status: Alpha**

This processor transforms OpenTelemetry log records into Microsoft Sentinel
[Advanced Security Information Model (ASIM)](https://learn.microsoft.com/en-us/azure/sentinel/normalization-about-schemas)
compliant log bodies and sets routing attributes consumed by the Azure Log
Analytics (Sentinel) exporter.

## Supported pipelines

- Logs

## How it works

Each `event_mapping` defines an [expr-lang](https://github.com/expr-lang/expr)
`filter` to select incoming logs, an ASIM `target_table` to map them into,
and a list of `field_mappings` that translate source log fields to ASIM
column names.

For every transformed record, the processor:

1. Replaces the log body with a flat map keyed by ASIM column names
   populated from `field_mappings`.
2. Sets `EventSchema` in the body to the schema name corresponding to the
   target table (e.g. `Authentication` for `ASimAuthenticationEventLogs`).
3. Stashes the original pre-transform body under the `AdditionalFields`
   ASIM column so unmapped source fields stay queryable in Sentinel.
4. Sets the `sentinel_stream_name` log-record attribute to
   `Custom-<target_table>` so the Azure Log Analytics exporter routes the
   record to the right DCR stream.

Records that do **not** match any `event_mapping` are dropped by default.
Set `unmatched_stream_name` (see below) to opt in to routing them to a
Sentinel custom-log table instead.

## Supported target tables

| `target_table` | `EventSchema` | Stream name |
| -- | -- | -- |
| `ASimAuthenticationEventLogs` | `Authentication` | `Custom-ASimAuthenticationEventLogs` |
| `ASimNetworkSessionLogs` | `NetworkSession` | `Custom-ASimNetworkSessionLogs` |
| `ASimDnsActivityLogs` | `Dns` | `Custom-ASimDnsActivityLogs` |
| `ASimProcessEventLogs` | `ProcessEvent` | `Custom-ASimProcessEventLogs` |
| `ASimFileEventLogs` | `FileEvent` | `Custom-ASimFileEventLogs` |
| `ASimAuditEventLogs` | `AuditEvent` | `Custom-ASimAuditEventLogs` |
| `ASimWebSessionLogs` | `WebSession` | `Custom-ASimWebSessionLogs` |
| `ASimDhcpEventLogs` | `Dhcp` | `Custom-ASimDhcpEventLogs` |
| `ASimRegistryEventLogs` | `RegistryEvent` | `Custom-ASimRegistryEventLogs` |
| `ASimUserManagementActivityLogs` | `UserManagement` | `Custom-ASimUserManagementActivityLogs` |

## Configuration

| Field | Type | Default | Required | Description |
| -- | -- | -- | -- | -- |
| `event_mappings` | []EventMapping | `[]` | No | Ordered list of event mappings. The first mapping whose `filter` matches a record wins. |
| `unmatched_stream_name` | string | `""` | No | When set, records that don't match any `event_mapping` are kept and stamped with this Sentinel stream name (must start with `Custom-`) instead of being dropped. The original body is preserved verbatim under the body's `AdditionalFields` key. The destination DCR must declare a matching `streamDeclaration`. |

### EventMapping

| Field | Type | Default | Required | Description |
| -- | -- | -- | -- | -- |
| `filter` | string | | No | Boolean [expr-lang](https://github.com/expr-lang/expr) expression. Empty matches all logs. |
| `target_table` | string | | Yes | One of the supported ASIM table names (see table above). |
| `field_mappings` | []FieldMapping | `[]` | No | Field mappings for this event. |

### FieldMapping

| Field | Type | Default | Required | Description |
| -- | -- | -- | -- | -- |
| `from` | string | | No | [expr-lang](https://github.com/expr-lang/expr) value expression evaluated against the source log. Required if `default` is not set. |
| `to` | string | | Yes | Target ASIM column name. |
| `default` | any | | No | Fallback value used when `from` is empty / evaluates to nil. Required if `from` is not set. |

## AdditionalFields preservation

After a record matches an event mapping, the original (pre-transform) body
is stored under the `AdditionalFields` key of the new body. ASIM's
`AdditionalFields` is declared as a `dynamic` (JSON) column on every
supported native ASIM table, so any source field that wasn't promoted to an
explicit ASIM column remains queryable in Sentinel via:

```kusto
ASimAuthenticationEventLogs
| extend raw = AdditionalFields
| project TimeGenerated, ActorUsername, TargetUsername, raw
```

For records routed via `unmatched_stream_name`, the new body contains only
`AdditionalFields` (carrying the verbatim original body) — no ASIM
column-name flattening is attempted because no mapping matched.

## Example configuration

```yaml
processors:
  asim_standardization:
    event_mappings:
      - filter: 'attributes["event.type"] == "authentication"'
        target_table: ASimAuthenticationEventLogs
        field_mappings:
          - from: 'body["@timestamp"]'
            to: TimeGenerated
          - to: EventCount
            default: 1
          - from: 'body["@timestamp"]'
            to: EventStartTime
          - from: 'body["@timestamp"]'
            to: EventEndTime
          - from: 'body["event"]["action"]'
            to: EventType
          - from: 'body["event"]["outcome"]'
            to: EventResult
          - to: EventProduct
            default: WindowsSecurity
          - to: EventVendor
            default: Microsoft
          - to: EventSchemaVersion
            default: "0.1.4"
          - from: 'resource["host.name"]'
            to: Dvc
          - from: 'body["user"]["name"]'
            to: TargetUsername
          - from: 'body["source"]["ip"]'
            to: SrcIpAddr
```

## Example: `asim_windows_security` preset

A typical preset for Windows Security event mapping into the ASIM
Authentication table, with unmapped records routed to a custom log table:

```yaml
processors:
  asim_standardization/windows_security:
    unmatched_stream_name: Custom-UnmappedLogs_CL
    event_mappings:
      - filter: 'attributes["winlog.channel"] == "Security" && body["winlog"]["event_id"] in [4624, 4625]'
        target_table: ASimAuthenticationEventLogs
        field_mappings:
          - from: 'body["@timestamp"]'
            to: TimeGenerated
          - to: EventCount
            default: 1
          - from: 'body["@timestamp"]'
            to: EventStartTime
          - from: 'body["@timestamp"]'
            to: EventEndTime
          - to: EventType
            default: Logon
          - from: 'body["event"]["outcome"]'
            to: EventResult
          - to: EventProduct
            default: Windows
          - to: EventVendor
            default: Microsoft
          - to: EventSchemaVersion
            default: "0.1.4"
          - from: 'resource["host.name"]'
            to: Dvc
          - from: 'body["user"]["name"]'
            to: TargetUsername
          - from: 'body["source"]["ip"]'
            to: SrcIpAddr
          - from: 'body["winlog"]["event_data"]["LogonType"]'
            to: LogonMethod
```

The processor sets the following on each transformed record so the
Microsoft Sentinel exporter can route appropriately:

- `body["EventSchema"] = "Authentication"`
- `body["AdditionalFields"] = <pre-transform body>`
- `attributes["sentinel_stream_name"] = "Custom-ASimAuthenticationEventLogs"`
