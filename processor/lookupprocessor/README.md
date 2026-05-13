# Lookup Processor

This processor enriches telemetry by looking up values from an external data
source and adding the resulting fields to the configured `context`.

## Supported pipelines
- Logs
- Metrics
- Traces

## How It Works
1. The processor loads a lookup source. Exactly one of `csv`, `redis`, or `api`
   must be configured.
2. When telemetry is received, the processor checks if the configured `field`
   exists in the configured `context`.
3. If the field exists and the source returns a match, all other key/value
   pairs from the source row are added to the `context` of the telemetry.
4. An optional cache (enabled by default with a 5-minute TTL) stores recent
   lookups. The cache backend is either an OpenTelemetry storage extension
   (e.g. `file_storage`, `redis_storage`) for persistence across restarts, or
   a per-instance in-memory map when no `storage` is configured.

## Configuration

### Common
| Field          | Type            | Default | Description |
| ---            | ---             | ---     | --- |
| context        | string          | ` `     | Telemetry context to read/write. One of `attributes`, `body`, `resource.attributes`. |
| field          | string          | ` `     | Field in `context` whose value is used as the lookup key. |
| source_type    | string          | ` `     | Optional. One of `csv`, `redis`, `api`. When unset, the source is inferred from the source block. |
| cache_enabled  | bool            | `true`  | Enable TTL caching of lookup results. |
| cache_ttl      | duration        | `5m`    | Cache entry lifetime. |
| storage        | component.ID    | `nil`   | Storage extension to back the cache (e.g. `file_storage`). When unset, the cache is in-memory and discarded on restart. |
| csv            | string          | ` `     | Path to CSV file. See [CSV source](#csv-source). |
| redis          | object          | `nil`   | Redis source config. See [Redis source](#redis-source). |
| api            | object          | `nil`   | API source config. See [API source](#api-source). |

### CSV source
| Field | Type   | Default | Description |
| ---   | ---    | ---     | --- |
| csv   | string | ` `     | Filesystem path to a CSV file. The first row is the header. Reloaded every minute. |

### Redis source
| Field      | Type   | Default | Description |
| ---        | ---    | ---     | --- |
| address    | string | ` `     | Redis server address `host:port`. |
| username   | string | ` `     | Optional username. |
| password   | string | ` `     | Optional password. |
| db         | int    | `0`     | Redis database index. |
| tls        | bool   | `false` | Use TLS (TLS 1.2+) for the connection. |
| key_prefix | string | ` `     | Optional prefix joined to the lookup key with `:`. |

The processor first tries `HGETALL` on the resolved key. If no fields are
returned, it falls back to `GET` and decodes the value as JSON `map[string]string`.

### API source
| Field            | Type              | Default | Description |
| ---              | ---               | ---     | --- |
| url              | string            | ` `     | URL template. `$fieldValue`, `${fieldValue}`, `$key`, or `${key}` are substituted with the URL-encoded lookup key. |
| method           | string            | `GET`   | HTTP method. |
| headers          | map[string]string | `nil`   | Request headers. |
| timeout          | duration          | `10s`   | HTTP timeout. |
| response_mapping | map[string]string | `nil`   | Maps output field names to dotted JSON paths in the response (e.g. `host: data.hostname`). When unset, the top-level response object is flattened. |

The HTTP client makes up to 3 attempts (initial + 2 retries) with exponential backoff between attempts (100ms, then 200ms).

### Example: CSV
```yaml
receivers:
    otlp:
        protocols:
            grpc:
processors:
    lookup:
        csv: ./example.csv
        context: body
        field: ip
exporters:
    logging:
service:
    pipelines:
        logs:
            receivers: [otlp]
            processors: [lookup]
            exporters: [logging]
```

```csv
ip,host,region,env
0.0.0.0,host-1,us-west,prod
1.1.1.1,host-2,us-east,dev
```

### Example: Redis (with shared storage cache)
```yaml
extensions:
    file_storage:
        directory: /var/lib/otelcol/lookup-cache
processors:
    lookup:
        context: attributes
        field: user_id
        cache_ttl: 10m
        storage: file_storage
        redis:
            address: redis.internal:6379
            key_prefix: user
            tls: true
service:
    extensions: [file_storage]
    pipelines:
        logs:
            receivers: [otlp]
            processors: [lookup]
            exporters: [logging]
```

### Example: API
```yaml
processors:
    lookup:
        context: resource.attributes
        field: host.name
        api:
            url: https://cmdb.internal/hosts/${fieldValue}
            method: GET
            headers:
                Authorization: Bearer ${env:CMDB_TOKEN}
            timeout: 2s
            response_mapping:
                team: data.owner.team
                env:  data.environment
```
