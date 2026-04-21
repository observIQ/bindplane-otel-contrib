# Sentinel Standardization Processor

**Status: Alpha**

This processor sets attributes on log records that the Microsoft Sentinel
(Azure Log Analytics) exporter uses to route each record to a specific Data
Collection Rule (DCR) stream/table and, optionally, to a specific DCR
immutable rule ID.

## Supported pipelines

- Logs

## How it works

For each log record, the processor evaluates the configured `sentinel_field`
rules in order. On the **first** rule whose OTTL `condition` matches, it
writes the following attributes to the record:

| Attribute | Source |
| --------- | ------ |
| `sentinel_stream_name` | Rule's `stream_name` |
| `sentinel_rule_id` | Rule's `rule_id`, if non-empty |
| `sentinel_ingestion_label["<key>"]` | One entry per `ingestion_labels` map entry |

If no rule matches, the record's attributes are left untouched.

The attributes written here are consumed by the Azure Log Analytics exporter,
which uses them — on a per-record basis — to override its configured
`stream_name` / `rule_id`.

## Configuration

| Field | Type | Default | Required | Description |
| ----- | ---- | ------- | -------- | ----------- |
| `sentinel_field` | []Rule | `[]` | No | Ordered list of conditional routing rules. |

### Rule

| Field | Type | Default | Required | Description |
| ----- | ---- | ------- | -------- | ----------- |
| `condition` | string | `true` | No | OTTL boolean expression evaluated against the log record. An empty value is treated as `true`. |
| `stream_name` | string | | Yes | DCR stream name (e.g. `Custom-MyTable_CL` or an `ASim*` stream). |
| `rule_id` | string | | No | Optional DCR immutable rule ID that overrides the exporter's configured `rule_id`. |
| `ingestion_labels` | map[string]string | | No | Optional labels written as `sentinel_ingestion_label["<key>"]` attributes. |

### Example Configuration

```yaml
processors:
  sentinel_standardization:
    sentinel_field:
      - condition: 'attributes["event.type"] == "auth"'
        stream_name: Custom-AuthEvents_CL
        rule_id: dcr-00000000000000000000000000000001
        ingestion_labels:
          env: prod
          team: security
      - condition: 'IsMatch(attributes["log.source"], "nginx")'
        stream_name: Custom-NginxAccess_CL
      - stream_name: Custom-Default_CL
```

In the example above, records are routed to the first matching rule. The
final rule has no `condition`, so it acts as a catch-all.
