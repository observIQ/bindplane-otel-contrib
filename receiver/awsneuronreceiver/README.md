# AWS Neuron Receiver

This receiver collects metrics from AWS Neuron accelerators (Inferentia and Trainium) on the host it runs on. It uses two collection streams:

- **Primary — `neuron-monitor`:** spawns the vendor-provided `neuron-monitor` binary and parses its JSON output. This is the source of truth for NeuronCore utilization, FLOPS, execution latency percentiles, execution counts/errors, per-core and runtime memory, ECC counters, and vCPU usage.
- **Secondary — sysfs:** a pure-Go, read-only reader of the Neuron kernel driver's sysfs tree (`/sys/devices/virtual/neuron_device`). It provides finer-grained memory detail than `neuron-monitor` exposes (an 11-category device-memory breakdown with present and peak, versus the monitor's 5-category aggregate), and acts as a fallback for ECC and topology when the `neuron-monitor` binary is not installed.

The two streams emit **distinct** metrics by design (the monitor's aggregate view and the sysfs fine-grained view are kept separate, not merged).

## Supported Pipelines
- Metrics

## How It Works
1. The user configures this receiver in a metrics pipeline.
2. On start, the receiver launches `neuron-monitor` (if available) and begins reading its JSON stream, and reads the sysfs tree on each scrape.
3. Metrics are emitted on the configured `collection_interval`.

> **Important — performance metrics require an active workload.** Neuron's per-runtime metrics (utilization, FLOPS, execution latency/errors, per-model memory) are produced by the Neuron runtime only while a process is actively executing a model. On an idle host these report empty; ECC, topology, and host/device memory are still collected.

## Prerequisites
- A host with AWS Neuron devices and the Neuron kernel driver installed (provides the sysfs tree and `/dev/neuron*`).
- For the primary stream: the `neuron-monitor` binary, shipped in the `aws-neuronx-tools` package. **This binary is not bundled with the collector**; install it separately if you want the full metric set.
- The collector process must be able to read the sysfs tree and execute `neuron-monitor`. The sysfs metric files are world-readable by default, so no special capability or group grant is required for read access.

## Degradation contract (read this)
Unlike a typical receiver, this receiver **does not fail** when a collection path is unavailable. This is deliberate, and each path degrades independently:

- If `neuron-monitor` (the primary path) is absent, fails to start, or its stream ends unexpectedly, the receiver logs a **single error** (not once per scrape) and continues, serving whatever the sysfs stream can provide. It does **not** crash the collector and does **not** repeatedly log the dead path.
- If the sysfs tree is unreadable, the receiver logs a **single error** and continues on the neuron-monitor stream. Individual missing or unreadable sysfs files are logged at **Debug** and skipped, so a partial tree is tolerated but still diagnosable.

Both failures are errors because both paths are first-class sources (neuron-monitor is the primary, sysfs supplies finer detail and the no-binary fallback). If you expect standard fail-fast receiver behavior, note the difference: a misconfigured `command` surfaces as one error plus a reduced metric set, not a startup failure.

## Resource attributes
When `neuron-monitor` is active, the receiver reads the instance metadata it reports and stamps it on the resource: `cloud.provider`, `cloud.region`, `cloud.availability_zone`, `host.id`, and `host.type` (from the EC2 IMDS data `neuron-monitor` already collects), plus the receiver-specific `aws.neuron.device.type` and `aws.neuron.neuroncore.version`.

The `cloud.*`/`host.*` keys are also produced by the [resourcedetectionprocessor](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/processor/resourcedetectionprocessor). This is **not** a conflict: that processor's `override` option (default `true`) deterministically resolves the overlap. With the default, the detection processor's values win; set `override: false` and the receiver's values are kept. The receiver stamps these keys so the metadata is present and correct even when no detection processor is configured (which the receiver cannot assume). If you run `resourcedetection`, you do not need to do anything — its defaults already take precedence.

## Configuration
| Field               | Type     | Default           | Description |
|---------------------|----------|-------------------|-------------|
| command             | string   | `neuron-monitor`  | Path to (or name of) the `neuron-monitor` binary, resolved against `PATH`. |
| config_file         | string   | `(none)`          | Optional path to a `neuron-monitor` JSON config file; its metric selections are used as-is. When empty, the receiver generates a default config that requests the full metric set (including ECC). The receiver always sets neuron-monitor's `period` to `collection_interval`, overriding any `period` in this file (see [Collection cadence](#collection-cadence)). |
| collection_interval | duration | `60s`             | How often the receiver scrapes and emits. This one value governs **both** halves: the sysfs read cadence and neuron-monitor's `period`. Default inherits the upstream scraperhelper `60s` (matching Bindplane's source default); see [Collection cadence](#collection-cadence) for when to lower it. A string readable by Go's [time.ParseDuration](https://pkg.go.dev/time#ParseDuration). |
| metrics             | map      | see [documentation.md](./documentation.md) | Per-metric enable/disable (the most specific layer). |
| metric_groups       | map      | `(unset)`         | Bulk enable/disable a whole group (see below). |

### Collection cadence
The receiver owns the cadence. `neuron-monitor` runs as a subprocess on its own `period`, so the receiver derives that period from `collection_interval` and launches `neuron-monitor` with it, keeping both halves in lockstep. This matters for correctness, not just tidiness: a receiver interval shorter than the monitor's period would re-emit the same report (duplicate points), and a longer one would drop neuron-monitor's per-period delta counts (execution counts/errors). For that reason a `period` set in a `config_file` is overridden; the file's metric selections are still honored.

The default is `60s`, inherited from the collector's scraperhelper default and matching Bindplane's source default. Lowering it yields finer-grained neuron-monitor performance metrics (NeuronCore utilization, FLOPS, execution counts/latency), useful for catching short bursts that a 60s sample averages away; raising it coarsens them. As a general rule do not go below `10s`, many telemetry backends cannot ingest metrics much more frequently. The sysfs power metric is an exception in the other direction: it refreshes only about once a minute on the device, so intervals below `60s` simply re-read the same power value.

### Two-layer metric enablement
Every metric the receiver can produce is defined in the catalog; a curated subset is enabled by default and the rest are defined but disabled. Enablement resolves in this precedence (most specific wins):

1. **Per-metric** — `metrics.<name>.enabled` always wins.
2. **Category** — `metric_groups.<group>` bulk-sets every metric whose name is `aws.neuron.<group>.*` (groups: `neuroncore`, `execution`, `runtime`, `system`, `device`, `errors`, `monitor`). Tri-state: unset falls through to defaults, `true` enables all in the group, `false` disables all.
3. **Default** — the catalog's default for that metric.

The `aws.neuron.system.*` metrics duplicate what the [hostmetrics receiver](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/receiver/hostmetricsreceiver) already provides, so they are defined but disabled by default.

### Example Configuration
```yaml
receivers:
  awsneuron:
    collection_interval: 60s
    command: neuron-monitor
    config_file: /etc/neuron-monitor/config.json
    metric_groups:
      system: false        # keep the hostmetrics-duplicate metrics off (already the default)
    metrics:
      aws.neuron.device.power.utilization:
        enabled: true      # opt in to a default-off metric
processors:
  batch:
exporters:
  debug:
service:
  pipelines:
    metrics:
      receivers: [awsneuron]
      processors: [batch]
      exporters: [debug]
```

### A note on `aws.neuron.device.power.utilization`
This sysfs-sourced metric is **best-effort and disabled by default**. AWS exposes it as the device's power draw as a percentage of the device's maximum power, reported as three statistics (`min`/`max`/`avg`) over a sampling period. The receiver emits all three as a fraction (unit `1`) under the `aws.neuron.power.statistic` attribute, mirroring the hostmetrics state-attribute pattern. AWS documents only that the values refresh about once a minute (the averaging-window length is unspecified), and it is **not populated on every instance or device**, some report `0` or no data. Treat it as a coarse, best-effort power signal, not a precise utilization gauge; the values come straight from the driver's sysfs node and the receiver does not synthesize them.

## Metrics
See [documentation.md](./documentation.md) for the full list of metrics, their units, types, and attributes, and which are enabled by default.
