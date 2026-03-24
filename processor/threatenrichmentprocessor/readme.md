# Threat Enrichment Processor

The `threat_enrichment` processor enriches log telemetry with threat context by matching field values against indicator lists. When a log record contains a value that appears in an indicator set (e.g. a known-malicious IP or domain), the processor stamps the record with `threat.matched = true` and `threat.rule = <rule_name>`.

| Status | Stability | Pipelines |
|--------|-----------|-----------|
| Alpha  | Alpha     | Logs      |

## How It Works

1. At startup the processor loads one or more **rules**. Each rule points to an indicator file (a list of known-bad values) and specifies which log fields to check.
2. Indicator values are inserted into a probabilistic filter (Bloom, Cuckoo, or Scalable Cuckoo) for fast, memory-efficient lookups.
3. For every incoming log record the processor iterates the rules in order. For each rule it reads the configured lookup fields, queries the filter, and on the first match enriches the record and moves to the next log.

Because probabilistic filters are used, false positives are possible (configurable via `false_positive_rate` for Bloom filters). False negatives do not occur — if an indicator is in the file it will always match.

## Configuration

```yaml
processors:
  threat_enrichment:
    filter:
      kind: bloom                # bloom | cuckoo | scalable_cuckoo
      estimated_count: 100000    # bloom: expected number of indicators
      false_positive_rate: 0.001 # bloom: target FP rate (0 < x < 1)

    rules:
      - name: malicious_ips
        indicator_file: /etc/otel/indicators/bad_ips.txt
        lookup_fields:
          - "client.ip"
          - "source.ip"

      - name: malicious_domains
        indicator_file: /etc/otel/indicators/bad_domains.txt
        lookup_fields:
          - body
          - "dns.question.name"
        filter:                  # per-rule override
          kind: cuckoo
          capacity: 50000
```

### Top-Level Fields

| Field    | Type           | Required | Description |
|----------|----------------|----------|-------------|
| `filter` | `FilterConfig` | Yes      | Default filter algorithm and parameters used by all rules unless overridden. |
| `rules`  | `[]Rule`       | Yes      | One or more indicator rules. At least one rule is required. |
| `storage`| `string`       | No       | Component ID of a storage extension for persistent state. |

### FilterConfig

| Field                | Type    | Required | Description |
|----------------------|---------|----------|-------------|
| `kind`               | string  | Yes      | Filter algorithm: `bloom`, `cuckoo`, or `scalable_cuckoo`. |
| `estimated_count`    | uint    | Bloom    | Expected number of elements in the filter. |
| `false_positive_rate`| float64 | Bloom    | Target false-positive rate, must be between 0 and 1 exclusive. |
| `max_estimated_count`| uint    | No       | Bloom: upper bound on estimated count. |
| `capacity`           | uint    | Cuckoo   | Expected number of elements for a standard cuckoo filter. |
| `initial_capacity`   | uint    | No       | Scalable cuckoo: initial capacity (0 = library default). |
| `load_factor`        | float32 | No       | Scalable cuckoo: load factor (0 = library default). |

### Choosing a Filter Kind

- **`bloom`** — Best for **static indicator sets** that are loaded once at startup and don't change. Bloom filters offer excellent memory efficiency and lookup speed, and the false-positive rate is directly configurable. Use this when your indicator files are updated out-of-band (e.g. a periodic file drop) and the processor is restarted to pick up changes.

- **`cuckoo`** — Best for **dynamic indicator sets** where entries may need to be added or removed at runtime. Cuckoo filters support deletion, which Bloom filters do not, making them a better fit when the backend manages a mutable set of indicators. Use this when indicator lists are expected to change frequently.

- **`scalable_cuckoo`** — A variant of cuckoo that grows automatically as elements are added, without requiring an upfront capacity estimate. Use this when the eventual size of the indicator set is unknown.

In practice, most static threat-intel feeds (IP blocklists, domain blocklists) work well with `bloom`. Rules backed by indicator sets that are updated dynamically should use `cuckoo` or `scalable_cuckoo`. You can mix filter kinds across rules using per-rule `filter` overrides.

### Rule

| Field            | Type           | Required | Description |
|------------------|----------------|----------|-------------|
| `name`           | string         | Yes      | Unique identifier for this rule (e.g. `"ips"`, `"domains"`). Appears in the `threat.rule` attribute on match. |
| `indicator_file` | string         | Yes      | Path to the indicator file. Supports plain text (one value per line) or a JSON array of strings. |
| `lookup_fields`  | `[]string`     | Yes      | Log attribute keys to check against this rule's filter. Use `"body"` to match on the log body. |
| `filter`         | `*FilterConfig`| No       | Optional per-rule filter override. If omitted, the top-level `filter` config is used. |

## Indicator Files

Indicator files can be in either of two formats:

**Plain text** — one indicator per line (blank lines and leading/trailing whitespace are ignored):

```
10.0.0.1
192.168.1.100
evil.example.com
```

**JSON array** — a JSON array of strings:

```json
["10.0.0.1", "192.168.1.100", "evil.example.com"]
```

## Output Attributes

When a match is found, the following attributes are added to the log record:

| Attribute        | Type   | Description |
|------------------|--------|-------------|
| `threat.matched` | bool   | Always `true` when a match occurs. |
| `threat.rule`    | string | The `name` of the first rule that matched. |

Only the first matching rule enriches the record — subsequent rules are skipped for that log.

## Example

Given this config:

```yaml
processors:
  threat_enrichment:
    filter:
      kind: bloom
      estimated_count: 10000
      false_positive_rate: 0.01
    rules:
      - name: bad_ips
        indicator_file: /data/bad_ips.txt
        lookup_fields: ["source.ip"]
```

And an incoming log record with attribute `source.ip = "10.0.0.1"` where `10.0.0.1` is listed in `/data/bad_ips.txt`, the output log record will have these additional attributes:

```
threat.matched = true
threat.rule    = "bad_ips"
```