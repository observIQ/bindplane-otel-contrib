# Posture Extension

The posture extension exposes a shared connectivity/EMCON **posture level** that the
[posture processor](../../processor/postureprocessor/README.md) consumes to decide which telemetry
tiers may egress and which are buffered to disk.

A posture is an ordered level (lowest/most-restrictive first), e.g. `silent < low < medium < full`.
Higher levels permit more telemetry to leave the collector. Because the same level must be shared
across the logs, metrics, and traces processor instances, it lives in this extension as a single
source of truth.

## Posture sources

The level is the **most restrictive (minimum)** of all enabled sources, so the most cautious source
always wins:

| Source | Use |
|--------|-----|
| `signal_file` | Polls a local file whose contents name the current level. Primary local trigger that keeps working when the OpAMP management link is down. |
| `control_server` | A small local HTTP endpoint: `GET /posture` reads the level, `POST /posture {"level":"medium"}` sets it. Explicit-command path independent of OpAMP. Bind to a local address. |
| `auto_detect` | Steps the level down after consecutive export failures and back up after consecutive successes, with hysteresis and a minimum dwell time to avoid flapping. Fed by the posture processor's export results. |
| OpAMP / Bindplane | A config push that changes `default` (or any field) takes effect through the normal reload path — but only when the management link is up, which is why the local sources above exist. |

## Configuration

```yaml
extensions:
  posture:
    levels: [silent, low, medium, full]   # ordered, lowest first
    default: silent                        # level before any source reports
    signal_file:
      path: /var/run/bdot/posture
      watch_interval: 1s
    control_server:
      endpoint: 127.0.0.1:12345
    auto_detect:
      failure_threshold: 3
      recovery_threshold: 5
      min_dwell: 30s
      floor: silent                        # lowest level auto-detect drops to
```

All sources are optional. With none enabled the level is fixed at `default` (settable via OpAMP).
