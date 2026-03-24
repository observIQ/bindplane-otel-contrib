# AMQ Filter Extension

The AMQ (Approximate Membership Query) Filter Extension provides probabilistic set membership data structures for use by processors in the collector pipeline. It supports multiple filter algorithms including Bloom, Cuckoo, Scalable Cuckoo, and Vacuum filters.

AMQ filters answer the question: "Is this element in the set?" They return `false` only when the element is definitely not in the set. A `true` result means the element may be in the set (with a configurable false positive rate).

Common use cases include:
- Deduplication of telemetry data
- Threat intelligence lookups (IP addresses, domains, hashes)
- Rate limiting by unique identifiers

## Supported Filter Types

| Kind | Description | Best For |
|------|-------------|----------|
| `bloom` | Classic Bloom filter with configurable false positive rate | General purpose, when false positive rate matters |
| `cuckoo` | Cuckoo filter with better space efficiency | When deletions might be needed (future) |
| `scalable_cuckoo` | Auto-scaling Cuckoo filter | Unknown or growing data sizes |
| `vacuum` | High-performance filter with configurable fingerprint size | High-throughput scenarios |

## Configuration

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `filters` | []FilterConfig | | `true` | List of named filters to create |
| `reset_interval` | duration | `0` | `false` | Interval to reset all filters (0 = never) |
| `telemetry.enabled` | bool | `false` | `false` | Enable telemetry reporting |
| `telemetry.update_interval` | duration | | `false` | Telemetry update interval |

### FilterConfig

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `name` | string | | `true` | Unique identifier for this filter |
| `kind` | string | `bloom` | `false` | Filter algorithm: `bloom`, `cuckoo`, `scalable_cuckoo`, `vacuum` |

#### Bloom Filter Options (`kind: bloom`)

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `estimated_count` | uint | | `true` | Expected number of elements |
| `false_positive_rate` | float | | `true` | Desired false positive rate (0.0-1.0) |
| `max_estimated_count` | uint | `0` | `false` | Cap on filter sizing (0 = no cap) |

#### Cuckoo Filter Options (`kind: cuckoo`)

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `capacity` | uint | | `true` | Expected number of elements |

#### Scalable Cuckoo Filter Options (`kind: scalable_cuckoo`)

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `initial_capacity` | uint | `10000` | `false` | Starting capacity |
| `load_factor` | float | `0.9` | `false` | Load factor threshold for scaling |

#### Vacuum Filter Options (`kind: vacuum`)

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `capacity` | uint | | `true` | Expected number of elements |
| `fingerprint_bits` | uint | `8` | `false` | Fingerprint size in bits (8-32) |

## Example Configuration

### Single Default Filter (Bloom)

```yaml
extensions:
  amq:
    filters:
      - name: default
        kind: bloom
        estimated_count: 100000
        false_positive_rate: 0.01

service:
  extensions: [amq]
```

### Multiple Filters with Different Algorithms

```yaml
extensions:
  amq:
    filters:
      - name: ips
        kind: bloom
        estimated_count: 1000000
        false_positive_rate: 0.001
        max_estimated_count: 5000000
      - name: domains
        kind: cuckoo
        capacity: 500000
      - name: hashes
        kind: vacuum
        capacity: 100000
        fingerprint_bits: 16
    reset_interval: 24h
    telemetry:
      enabled: true
      update_interval: 60s

service:
  extensions: [amq]
```

### Scalable Filter for Unknown Data Size

```yaml
extensions:
  amq:
    filters:
      - name: events
        kind: scalable_cuckoo
        initial_capacity: 10000
        load_factor: 0.85

service:
  extensions: [amq]
```

## Usage from Processors

Processors should use [`ExtensionFrom`](membership.go) with the component from `host.GetExtensions()`:

```go
import "github.com/observiq/bindplane-otel-collector/extension/amqextension"

comp, ok := host.GetExtensions()[amqExtensionID]
if !ok {
    return fmt.Errorf("missing extension %v", amqExtensionID)
}
amq, err := amqextension.ExtensionFrom(comp)
if err != nil {
    return err
}

// Add a value to a named filter (e.g. on startup)
amq.AddString("ips", "192.168.1.1")

// Check if a value may be in the filter
if amq.MayContainString("ips", "192.168.1.1") {
    // Value is possibly in the set (may be false positive)
}
```

The [`Membership`](membership.go) interface is the supported surface for callers outside this package.
