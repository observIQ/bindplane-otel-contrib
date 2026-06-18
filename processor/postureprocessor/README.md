# Posture Processor

The posture processor gates telemetry egress by a connectivity/EMCON **posture level**. It was built
for variable-connectivity deployments (e.g. a ship at sea): in a restricted posture only high-priority
real-time data leaves the collector, while lower-priority data is persisted to disk and drained to the
**same destination** once the posture rises to permit it.

It works with logs, metrics, and traces, and sits in front of any exporter.

## How it works

1. **Classify into tiers.** Each record is assigned to the first `tiers` entry whose OTTL `condition`
   matches; unmatched records fall to an implicit default tier.
2. **Gate by posture level.** A tier egresses immediately when the current posture level is at or above
   its `min_level`. Otherwise its records are buffered to disk in that tier's own durable FIFO.
3. **Drain on recovery.** When the posture rises, the tiers that now qualify are drained to the next
   consumer (the same destination), most-important (config order) first, rate-limited to avoid a
   stampede. Draining survives collector restarts because the queue index is persisted.

The posture level comes from a [posture extension](../../extension/postureextension/README.md)
(recommended, and required for sharing one posture across logs/metrics/traces) or from an inline
`posture` block (single-signal pipelines only — an inline control server would otherwise bind the same
port from three signal instances).

## Configuration

```yaml
extensions:
  pebble:
    directory: { path: /var/lib/bdot/pebble }
  posture:
    levels: [silent, low, medium, full]
    default: silent
    signal_file: { path: /var/run/bdot/posture }
    control_server: { endpoint: 127.0.0.1:12345 }
    auto_detect: { failure_threshold: 3, recovery_threshold: 5, min_dwell: 30s }

processors:
  posture:
    levels: [silent, low, medium, full]   # must match the posture extension
    posture_extension: posture
    storage: pebble
    tiers:
      # first match wins; min_level = lowest posture at which this tier egresses
      - { name: battle, condition: 'attributes["priority"] == "realtime"', min_level: silent }
      - { name: ops,    condition: 'severity_number >= SEVERITY_NUMBER_WARN', min_level: medium }
    default_min_level: full                # unmatched data only on a full link
    drain: { interval: 1s, max_bytes_per_sec: 1048576, jitter_fraction: 0.2 }
    buffer: { max_bytes: 5368709120, overflow_policy: drop_oldest }   # per tier

service:
  extensions: [pebble, posture]
  pipelines:
    logs: { receivers: [otlp], processors: [posture], exporters: [otlp/dest] }
```

### Settings

| Field | Description |
|-------|-------------|
| `levels` | Ordered posture level names, lowest first. Must match the posture extension. |
| `tiers[].condition` | OTTL condition; first match owns the record. |
| `tiers[].min_level` | Lowest posture level at which the tier egresses. |
| `default_min_level` | `min_level` for unmatched telemetry. Default: highest level. |
| `posture_extension` / `posture` | Posture source (exactly one). |
| `storage` | Storage extension used to persist buffered telemetry. Required. |
| `drain.interval` / `max_bytes_per_sec` / `jitter_fraction` | Drain pacing. |
| `buffer.max_bytes` / `max_items` / `overflow_policy` | Per-tier on-disk limits and overflow behavior (`drop_oldest`, `drop_newest`, `block_drop`). |

## Notes and limitations

- **Ordering.** FIFO within a tier; by design the destination sees high-priority-then-backlog
  reordering. There is no cross-signal ordering.
- **Metrics granularity.** Classification is at the metric level (a metric's datapoints stay together).
  Datapoint-granularity classification is a possible future enhancement.
- **Auto-detect recovery** is driven by drain export results, so it can only step the level back up when
  there is bufferable backlog to retry. When at the lowest level with only the always-on tier flowing,
  recovery should come from the signal file, control endpoint, or OpAMP.
- **Exporter `sending_queue`.** Place this processor before the exporter so deferrable data never
  enters the exporter's (indiscriminate) persistent queue while restricted. Keep the exporter's own
  queue small for high-priority resilience.
