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
3. Sets two log-record attributes for downstream Sentinel routing:
   - `sentinel_stream_name = "Custom-" + target_table`
   - `EventSchema = <PascalCase>` (matches the body field).

Records that do not match any `event_mapping` are dropped.

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
| `runtime_validation` | bool | `false` | No | When `true`, after writing the new body, the processor verifies that all required ASIM columns are present. Missing columns are logged at debug level — the record is **not** dropped. |

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

### Required columns (when `runtime_validation` is enabled)

The following columns are required for **every** target table. If any are
missing after field mapping, a debug-level log is emitted; the record is
kept either way.

```
TimeGenerated, EventCount, EventStartTime, EventEndTime, EventType,
EventResult, EventProduct, EventVendor, EventSchema, EventSchemaVersion,
Dvc
```

> Note: per-table required-column whitelists are not currently enforced; all
> columns declared in the Sentinel DCR templates for a given stream are
> permissible, but only the columns above are validated when
> `runtime_validation` is enabled.

## Example configuration

```yaml
processors:
  asim_standardization:
    runtime_validation: true
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
            default: "0.1.3"
          - from: 'resource["host.name"]'
            to: Dvc
          - from: 'body["user"]["name"]'
            to: TargetUsername
          - from: 'body["source"]["ip"]'
            to: SrcIpAddr
```

## Example: `asim_windows_security` preset

A typical preset for Windows Security event mapping into the ASIM
Authentication table:

```yaml
processors:
  asim_standardization/windows_security:
    runtime_validation: true
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
            default: "0.1.3"
          - from: 'resource["host.name"]'
            to: Dvc
          - from: 'body["user"]["name"]'
            to: TargetUsername
          - from: 'body["source"]["ip"]'
            to: SrcIpAddr
          - from: 'body["winlog"]["event_data"]["LogonType"]'
            to: LogonMethod
```

The processor sets these attributes on each transformed record so the
Microsoft Sentinel exporter can route appropriately:

- `attributes["sentinel_stream_name"] = "Custom-ASimAuthenticationEventLogs"`
- `attributes["EventSchema"] = "Authentication"`
