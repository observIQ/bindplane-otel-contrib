# Telemetry Generator Receiver
This receiver is used to generate synthetic telemetry for testing and configuration purposes. 

## Minimum Agent Versions
- Introduced: [v1.46.0](https://github.com/observIQ/bindplane-otel-collector/releases/tag/v1.46.0)
- Updated to include host_metrics: [v1.47.0](https://github.com/observIQ/bindplane-otel-collector/releases/tag/v1.47.0)

## Supported Pipelines
- Logs
- Metrics
- Traces

## Configuration for all generators
| Field                | Type      | Default   | Required | Description |
|----------------------|-----------|-----------|----------|-------------|
| payloads_per_second  |  int      |     `1`   | `false`  | The number of payloads this receiver will generate per second.|
| generators           |  list     |           | `true`   | A list of generators to use.|
### Common Generator Configuration
| Field                | Type      | Default          | Required | Description  |
|----------------------|-----------|------------------|----------|--------------|
| type                 |  string   |                  | `true`   | The type of generator to use. Currently `logs`, `otlp`, `metrics`, `host_metrics`, and `windows_events` are supported.  |
| resource_attributes  |  map      |                  | `false`  | A map of resource attributes to be included in the generated telemetry. Values can be `any`.   |
| attributes           |  map      |                  | `false`  | A map of attributes to be included in the generated telemetry. Values can be `any`.  |
| additional_config    |  map      |                  | `false`  | A map of additional configuration options to be included in the generated telemetry. Values can be `any`.|

### Log Generator Configuration
| Field                | Type      | Default | Required | Description |
|----------------------|-----------|---------|----------|-------------|
| body                 |  string   |         | `false`  | The body of the log, set in additional_config |
| severity             |  int      |         | `false`  | The severity of the log message, set in additional_config |

#### Example Configuration
```yaml
telemetrygeneratorreceiver:
    payloads_per_second: 1
    generators:
        - type: logs
          resource_attributes:
              res_key1: res_value1
              res_key2: res_value2            
          attributes:
              attr_key1: attr_value1
              attr_key2: attr_value2            
          additional_config:
              body: this is the body   
              severity: 4
        - type: logs
          resource_attributes:
              res_key3: res_value3
              res_key4: res_value4            
          attributes:
              attr_key3: attr_value3
              attr_key4: attr_value4            
          additional_config:
              body: this is another body   
              severity: 10
```

### OTLP Replay Generator

The OTLP Replay Generator replays JSON-formatted telemetry from the variable `otlp_json`. It adjusts the timestamps of the telemetry relative the current time, with the most recent record moved to the current time, and the previous records the same relative duration in the past. The `otlp_json` variable should be valid OTLP, such as the JSON created by `plog.JSONMarshaler`,`ptrace.JSONMarshaler`, or `pmetric.JSONMarshaler`. The `otlp_json` variable is set in the `additional_config` section of the generator configuration. The `attributes` and `resource_attributes` fields are ignored.

#### additional_config:

| Field                | Type      | Default          | Required | Description  |
|----------------------|-----------|------------------|----------|--------------|
| telemetry_type       |  string   |                  | `true`   | The type of telemetry to replay: `logs`, `metrics`, or `traces`.  |
| otlp_json            |  string   |                  | `true`  | A string of JSON encoded OTLP telemetry|

#### Example Configuration
```yaml
telemetrygeneratorreceiver:
    payloads_per_second: 1
    generators:
        - type: otlp
          additional_config:
            telemetry_type: "metrics",
			otlp_json:      `{"resourceMetrics":[{"resource":{},"scopeMetrics":[{"scope":{},"metrics":[{"exponentialHistogram":{"dataPoints":[{"attributes":[{"key":"prod-machine","value":{"stringValue":"prod-1"}}],"count":"4","positive":{},"negative":{},"min":0,"max":100}]}}]}]}]}`,
```

### Metrics Generator

The metrics generator creates synthetic metrics. The generator can be configured to create metrics with arbitrary names, values, and attributes. The generator can be configured to create metrics with a random value between a minimum and maximum value, or a constant value by setting `value_max = value_min`. For `Sum` metrics with unit `s` and `Gauge` metrics, the generator will create a random `float` value. For all other `Sum` metrics, the generator will create a random `int` value.

#### additional_config:

| Field                | Type      | Default          | Required | Description  |
|----------------------|-----------|------------------|----------|--------------|
| telemetry_type       |  string   |                  | `true`   | The type of telemetry to replay: `logs`, `metrics`, or `traces`.  |
| metrics           |  array   |                  | `true`  | A list of metrics to generate|

#### metrics:


| Field                | Type      | Default          | Required | Description  |
|----------------------|-----------|------------------|----------|--------------|
| name                 |  string   |                  | `true`   | The metric name  |
| value_min           |  int   |                  | `true`  | The metric's minimum value|
| value_max           |  int   |                  | `true`  | The metric's maximum value|
| type                 |  string   |                  | `true`   | The metric type: `Gauge`, or `Sum`|
| unit                 |  string   |                  | `true`   | The metric unit, either `By`, `by`, `1`, `s`, `{thread}`, `{errors}`, `{packets}`, `{entries}`, `{connections}`, `{faults}`, `{operations}`, or `{processes}`|
| attributes           |  map      |                  | `false`  | A map of attributes to be included in the generated telemetry record. Values can be `any`.|

#### Example Configuration
```yaml
telemetrygeneratorreceiver:
    payloads_per_second: 1
    generators:
        - type: metrics
          resource_attributes:
            host.name: 2ed77de7e4c1
            os.type: linux          
          additional_config:
            metrics: 
            # memory metrics
             - name:	system.memory.usage
                value_min: 100000
                value_max: 1000000000
                type:	Sum
                unit:	By
                attributes:
                  state: cached
            # load metrics                  
              - name:	system.cpu.load_average.1m
                value_min: 0
                value_max: 1
                type: Gauge
                unit:	"{thread}"   
            # file system metrics                                          
              - name: system.filesystem.usage
                value_min: 0
                value_max: 15616700416
                type: Sum
                unit: By
                attributes:
                  device: "/dev/vda1"
                  mode: rw
                  mountpoint: "/etc/hosts"
                  state: reserved
                  type: ext4                    
```


### Host Metrics Generator

The host metrics generator creates synthetic host metrics, from a list of pre-defined metrics. The metrics resource attributes can be set in the `resource_attributes` section of the generator configuration.

#### Example Configuration
```yaml
telemetrygeneratorreceiver:
    payloads_per_second: 1
    generators:
        - type: host_metrics
          resource_attributes:
            host.name: 2ed77de7e4c1
            os.type: linux   
```       
### Windows Events Generator

The Windows Events Generator replays a sample of recorded Windows Event Log data. It has no additional configuration, and will ignore `resource_attributes` and `attributes` fields.

#### Example Configuration
```yaml
telemetrygeneratorreceiver:
    payloads_per_second: 1
    generators:
        - type: windows_events          
```       

### Blitz Generator

The `blitz` generator type pulls structured telemetry from the [blitz](https://github.com/observIQ/blitz) embed package and converts it to OTel `pdata`. It is available on **all three signal-type pipelines** — logs, metrics, and traces — and supports two complementary configuration shapes: curated recipes and pasted blitz YAML. See [PIPE-1017](https://linear.app/bindplane/issue/PIPE-1017) for context and [docs/embed.md](https://github.com/observIQ/blitz/blob/main/docs/embed.md) in the blitz repo for the underlying contract.

#### Build requirement

**Any binary that links this receiver MUST be built with `-tags embed_library`.** The tag bakes the blitz filegen data library into the binary so `package:<name>` references in `blitz_yaml` configs resolve without an on-disk install of `data_library/`. The receiver fails fast at Start time if the tag was omitted and any `Type: "blitz"` entries are configured.

For BDOT goreleaser builds, add `-tags embed_library` to the build flags. For local development:

```sh
go build -tags embed_library ./...
go test -tags embed_library ./...
```

#### Supported pipelines

| Pipeline | Generators reachable | Config shapes |
|----------|----------------------|---------------|
| logs     | apache common/combined/error, filegen, fix, json, kubernetes, nginx, okta, palo-alto, postgres, wel | recipes + `blitz_yaml` |
| metrics  | hostmetrics | `blitz_yaml` only |
| traces   | traces | `blitz_yaml` only |

Each signal-type receiver instance wires only its own consumer into blitz. A `blitz_yaml` naming a generator of a different signal type (e.g. `hostmetrics` on a logs pipeline) is rejected at startup with blitz's signal-type error. Curated recipes are log-only; using `recipe:` on a metrics or traces pipeline is rejected with a pointer to `blitz_yaml`.

#### Configuration shapes

##### Recipe (named, parameterized — logs pipelines only)

| Field                              | Type      | Default       | Required | Description |
|------------------------------------|-----------|---------------|----------|-------------|
| `additional_config.recipe`         | string    |               | `true` (one of `recipe`/`blitz_yaml`) | Recipe name. Six log recipes ship: `apache`, `apache-combined`, `apache-error`, `nginx`, `kubernetes-cluster`, `pii-stress`. |
| `additional_config.recipe_params.workers` | int  | recipe default | `false` | Per-generator worker count for the recipe's modules. Recipes use `1` (or `2` for `pii-stress`) when omitted. |
| `additional_config.recipe_params.rate` | string  | recipe default | `false` | Per-generator emission interval (Go duration string, e.g. `100ms`). Recipes use `1s` (or `100ms` for `pii-stress`) when omitted. |

##### Custom blitz YAML

| Field                          | Type     | Default | Required | Description |
|--------------------------------|----------|---------|----------|-------------|
| `additional_config.blitz_yaml` | string   |         | `true` (one of `recipe`/`blitz_yaml`) | A verbatim blitz YAML config string. Parsed by blitz's public `config.LoadModules` API; supports every embed-ready blitz generator matching the pipeline's signal type. Wrong-signal and non-embed-eligible generators (`winevt`, `nop`) are rejected with a clear error at startup. |

Users wanting env-driven values inside `blitz_yaml` use the OTel collector's native env-substitution syntax at collector-config time — the receiver does not forward process env into the blitz loader.

##### Body parsing (logs pipelines only)

| Field                          | Type | Default | Required | Description |
|--------------------------------|------|---------|----------|-------------|
| `additional_config.parse_body` | bool | `false` | `false`  | When `true`, log records whose blitz generator supplies a parse callback emit a structured map body instead of the raw message string. Falls back to the raw message on any parse error. Default `false` emits raw log lines — the typical pipeline-testing shape. |

#### Per-record metadata, `resource_attributes`, and `attributes`

Blitz generators emit per-record metadata — `host.name`, `telemetry.source`, format identifiers, and other module-known dimensions — as per-record resource and attribute maps (blitz v0.18.0+, [PIPE-1021](https://linear.app/bindplane/issue/PIPE-1021)). The receiver merges that per-record metadata over the entry's `resource_attributes` / `attributes` config:

- The receiver-config map is the **base**.
- Blitz's per-record values **win on key conflict** (the generator knew the source-of-truth at emit time).
- Records in a batch whose merged resources differ are emitted under separate `ResourceLogs` / `ResourceMetrics` / `ResourceSpans` groups; identical merged resources share a group.

##### Per-key locking

Any key in `resource_attributes` or `attributes` can be **locked** so blitz's per-record value cannot override it. Each key takes one of two forms:

```yaml
resource_attributes:
    os.type: linux                    # simple form — unlocked, blitz wins on conflict
    host.name:                        # structured form — locked
        value: edge-node-7
        lock: true
    deployment.environment:           # structured form, lock omitted — same as simple
        value: staging
```

The structured form requires a `value` sub-key; `lock` is optional and defaults to `false`. Only `value` and `lock` are allowed sub-keys — anything else is rejected at config validation with the offending key named in the error. Locking a key also collapses resource groups that differed only on that key.

When to lock: pinning `host.name` for ingestion tests that route on a fixed hostname, pinning `deployment.environment` so generated data can't masquerade as another environment, or any key where the receiver config must win over generator-emitted values. The alternative — an `attributes` processor after the receiver — works too; locking just keeps the intent in one place.

#### Example — recipe (logs)

```yaml
telemetrygeneratorreceiver:
    payloads_per_second: 1
    generators:
        - type: blitz
          resource_attributes:
              service.name: my-app
              deployment.environment:
                  value: staging
                  lock: true
          attributes:
              log.source: apache
          additional_config:
              recipe: apache-combined
              recipe_params:
                  workers: 2
                  rate: 100ms
```

#### Example — custom blitz YAML (logs)

```yaml
telemetrygeneratorreceiver:
    payloads_per_second: 1
    generators:
        - type: blitz
          resource_attributes:
              service.name: my-cluster
          additional_config:
              blitz_yaml: |
                  generators:
                    - type: apache-common
                      apache-common: {workers: 1, rate: 100ms}
                    - type: json
                      json: {workers: 1, rate: 100ms, type: default}
                    - type: kubernetes
                      kubernetes: {workers: 1, rate: 100ms}
                  output:
                    type: nop
                  logging:
                    type: stdout
                  metrics:
                    port: 19000
```

#### Example — custom blitz YAML (metrics)

```yaml
telemetrygeneratorreceiver/metrics:
    payloads_per_second: 1
    generators:
        - type: blitz
          resource_attributes:
              cluster.name: load-test-1
          additional_config:
              blitz_yaml: |
                  generators:
                    - type: hostmetrics
                      hostmetrics: {workers: 1, rate: 10s}
                  output:
                    type: nop
                  logging:
                    type: stdout
                  metrics:
                    port: 19000
```

#### Example — custom blitz YAML (traces)

```yaml
telemetrygeneratorreceiver/traces:
    payloads_per_second: 1
    generators:
        - type: blitz
          additional_config:
              blitz_yaml: |
                  generators:
                    - type: traces
                      traces: {workers: 1, rate: 500ms}
                  output:
                    type: nop
                  logging:
                    type: stdout
                  metrics:
                    port: 19000
```
