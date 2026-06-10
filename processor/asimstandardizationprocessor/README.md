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

Records that do **not** match any `event_mapping` are dropped.

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
| `runtime_validation` | bool | `true` | No | Coerce mapped values to their target ASIM column types and drop records missing any ASIM common mandatory column. See [Runtime validation](#runtime-validation). |
| `attribution_fields` | map[string]string | `{}` | No | Constant key/value pairs added to `AdditionalFields.Attribution` on every transformed record. Lets downstream connector-health queries disambiguate records from this pipeline when multiple sources write into the same ASIM table. See [AdditionalFields preservation](#additionalfields-preservation). |

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

## Runtime validation

After each record's body has been mapped, the processor runs two checks
designed to keep the Azure DCR upload from being rejected with
`InvalidTransformOutput` (which would trigger a persistent-queue retry storm
ending in silent data loss):

1. **Type coercion.** *Always on.* Each populated body field is coerced to
   the type declared by Microsoft for that column. Strings are parsed as
   datetimes (RFC3339, ISO8601, common variants); as integers (decimal,
   `0x`-prefixed hex, decimal-point tolerant); as floats; or as booleans.
   Slice/map values targeting a string column are JSON-marshalled. Numeric
   `int` columns are checked against the KQL 32-bit signed range. Coercion
   failures drop the offending field with a warn log; the record continues
   with the remaining fields.
2. **Mandatory column check.** *Gated by `runtime_validation` (default
   `true`).* Records missing any of the ASIM common mandatory columns
   (`TimeGenerated`, `EventCount`, `EventStartTime`, `EventEndTime`,
   `EventType`, `EventResult`, `EventProduct`, `EventVendor`, `EventSchema`,
   `EventSchemaVersion`, `Dvc`) after coercion are dropped entirely with a
   warn log. Set `runtime_validation: false` to skip this check (coercion
   still runs).

`TimeGenerated` no longer needs an explicit field mapping: when no mapping
sets it, the processor auto-populates it from the first available of
`EventEndTime`, `EventStartTime`, the log record's timestamp, its observed
timestamp, or the current time. The auto-populated value flows through the
same datetime coercion as user-mapped columns. An explicit `TimeGenerated`
mapping always takes precedence.

The full per-table column-type map is sourced from Microsoft's
[KqlvalidationsTests CustomTables](https://github.com/Azure/Azure-Sentinel/tree/master/.script/tests/KqlvalidationsTests/CustomTables)
JSON, with hand overrides for fields where the published validation schema
disagrees with the actual native ASim* stream contract on Azure (Threat\*
counters, web byte/packet counters, etc.).

## Duplicates and at-least-once delivery

Azure's Logs Ingestion API has no idempotency-key support and the
[`azureloganalyticsexporter`](../../exporter/azureloganalyticsexporter)
retries failed uploads as a whole batch. If Azure persists a batch but the
response is lost (timeout, 5xx after write), the retry produces duplicates.
Delivery is **at-least-once**, not exactly-once.

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

### With `attribution_fields` set

When `attribution_fields` is non-empty, the processor wraps the column so
the original body and the attribution markers coexist:

```json
"AdditionalFields": {
  "OriginalEvent": { ...the pre-transform body... },
  "Attribution":   { "bindplane_source": "bindplane", "bindplane_pipeline_id": "pipeline-42" }
}
```

The original body remains queryable, one level deeper:

```kusto
ASimAuthenticationEventLogs
| where tostring(AdditionalFields.Attribution.bindplane_source) == "bindplane"
| extend raw = AdditionalFields.OriginalEvent
| project TimeGenerated, ActorUsername, TargetUsername, raw
```

When `attribution_fields` is empty/unset (the default), `AdditionalFields`
keeps the prior flat shape — the original body unwrapped — so any existing
consumer queries continue to work unchanged.

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
Authentication table:

```yaml
processors:
  asim_standardization/windows_security:
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
