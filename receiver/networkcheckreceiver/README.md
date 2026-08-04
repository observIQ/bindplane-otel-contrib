# Network Stat Receiver

Probes a configurable list of network targets and emits latency, packet loss, and traceroute metrics. Supports two probe modes per target:

- **ICMP** — raw ICMP ping (RTT min/avg/max, packet loss). Falls back to HTTP if the process lacks `CAP_NET_RAW` / root privileges.
- **HTTP** — full HTTP round-trip timing (DNS lookup, TCP connect, TLS handshake, request write, response read). Uses `confighttp.ClientConfig` so TLS and proxy settings work out of the box.

Optional traceroute probes can run on a fixed cycle interval, when packet loss exceeds a threshold, or both.

## Privileges

| Feature | Requirement |
|---------|-------------|
| ICMP ping | Root or `CAP_NET_RAW` |
| UDP traceroute (default) | None (most Linux kernels) |
| ICMP traceroute | Root or `CAP_NET_RAW` |
| HTTP ping | None |

When ICMP is unavailable the receiver logs a warning and automatically falls back to HTTP probing for affected targets.

## Configuration

```yaml
receivers:
  networkstat:
    # How often to run a scrape cycle. Default 60s.
    collection_interval: 60s

    # How many targets to probe per scrape cycle.
    # 0 (default) = all targets every cycle.
    # 1 with 10 targets + 1m interval = each target probed once per 10 minutes.
    batch_size: 0

    targets:
      - endpoint: "8.8.8.8"        # ICMP target: IP or hostname
        method: icmp               # "icmp" (default) or "http"
        ping_count: 3              # ICMP packets per probe. Default 3.
        dns_server: ""             # Override DNS resolver, e.g. "8.8.8.8:53". Blank = system resolver.

      - endpoint: "https://example.com/health"  # HTTP target: full URL
        method: http
        http_method: HEAD          # HTTP verb. Default HEAD.
        timeout: 10s               # Request timeout.
        tls:
          insecure_skip_verify: false

    traceroute:
      enabled: false               # Disabled by default.
      method: udp                  # "udp" (default, no root required) or "icmp" (requires root).
      max_hops: 30
      timeout: 3s                  # Per-hop timeout.

      # Interval-based: run a traceroute every N times this target is probed.
      # 0 = disabled. Example: interval 10 + collection_interval 1m = traceroute every 10 minutes.
      interval: 0

      # Failure-based: run a traceroute when ICMP packet loss >= failure_threshold.
      on_failure: false
      failure_threshold: 0.5       # 0.0–1.0. Default 0.5 (50% packet loss).
```

## Metrics

### ICMP targets

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `network.ping.latency_min` | Gauge | ms | Minimum RTT |
| `network.ping.latency_avg` | Gauge | ms | Average RTT |
| `network.ping.latency_max` | Gauge | ms | Maximum RTT |
| `network.ping.packet_loss` | Gauge | 1 | Packet loss ratio (0.0–1.0) |

Attributes: `ping.method` (icmp or http after fallback), `dns.server`

### HTTP targets

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `network.http.status` | Gauge | 1 | 1 = response received, 0 = error/timeout |
| `network.http.duration` | Gauge | ms | Total round-trip time |
| `network.http.dns_lookup_duration` | Gauge | ms | DNS resolution time |
| `network.http.client_connection_duration` | Gauge | ms | TCP connect time |
| `network.http.tls_handshake_duration` | Gauge | ms | TLS handshake time (0 for plain HTTP) |
| `network.http.request_duration` | Gauge | ms | Request write time |
| `network.http.response_duration` | Gauge | ms | Time to first response byte |

Attributes: `http.response.status_code`, `dns.server`

### Traceroute

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `network.traceroute.hop.latency` | Gauge | ms | RTT to a single hop |

Attributes: `traceroute.hop.index`, `traceroute.hop.address`, `dns.server`

### Resource attributes

Each target produces its own resource with `target.endpoint` set to the configured `endpoint` value.
