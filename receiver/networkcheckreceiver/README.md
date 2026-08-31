# Network Check Receiver

Probes a configurable list of network targets and emits latency, packet loss, and traceroute metrics. Supports three probe modes per target:

- **ICMP** — ICMP ping (RTT min/avg/max, packet loss). On macOS, datagram ICMP works without root; on Linux, raw ICMP requires `CAP_NET_RAW` or root; on Windows, raw ICMP requires administrator. Falls back to HTTP if no ICMP mode is available.
- **HTTP** — full HTTP round-trip timing (DNS lookup, TCP connect, TLS handshake, request write, response read). Uses `confighttp.ClientConfig` so TLS and proxy settings work out of the box.
- **DNS** — sends a DNS query to a specific server and measures response time. Use this to actively test a DNS server rather than rely on the system resolver.

Optional traceroute probes can run on a fixed cycle interval, when packet loss exceeds a threshold, or both.

## Privileges

| Feature | Linux | macOS | Windows |
|---------|-------|-------|---------|
| ICMP ping | Root or `CAP_NET_RAW` | None (datagram mode) or root | Administrator (raw socket) |
| UDP traceroute (default) | None (most kernels) | Root or `CAP_NET_RAW` | None (see Windows notes) |
| ICMP traceroute | Root or `CAP_NET_RAW` | Root or `CAP_NET_RAW` | None (see Windows notes) |
| HTTP ping | None | None | None |
| DNS probe | None | None | None |

When the collector runs as a Windows service it runs as `LocalSystem`, which
already holds the privilege ICMP ping needs. Running the collector by hand from
an unelevated shell does not: ICMP probes fail with a `setsockopt` access error
and those targets fall back to HTTP.

At startup the receiver automatically detects which ICMP mode is available:

1. **Raw ICMP** (Linux root / macOS root / Windows admin): highest fidelity; used when the process has the required privilege.
2. **Datagram ICMP** (macOS only, no root required): uses macOS's unprivileged ICMP socket support via pro-bing. RTT statistics are equivalent to raw mode.
3. **HTTP fallback**: if neither ICMP mode succeeds, the receiver logs a warning and probes that target via HTTP instead.

### Windows notes

Traceroute on Windows ignores the `method` setting and always uses the IP Helper
API (`IcmpSendEcho`), the same mechanism as the built-in `tracert.exe`. Neither
portable method works there: Windows does not deliver unsolicited inbound ICMP
time-exceeded messages to a raw socket, so both the UDP and ICMP methods time out
on every hop even with Administrator rights. The native path needs no elevation,
so `traceroute.enabled: true` works out of the box and `method` can be left at
its default.

The system resolver is detected via `GetAdaptersAddresses`, the same source
`ipconfig` and `Get-DnsClientServerAddress` read, so the `dns.server` attribute
is populated on Windows without configuring `dns_server`.

### Unanswered hops

A hop that does not reply within `traceroute.timeout` is recorded as unanswered
and emits **no** `network.traceroute.hop.latency` data point, since the only
duration available for it is the timeout itself. A path that stops answering is
abandoned after five consecutive unanswered hops rather than probing all the way
to `max_hops`, which bounds how long a single traceroute can occupy a scrape.

## Configuration

```yaml
receivers:
  networkcheck:
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
        dns_server: ""             # Override DNS resolver, e.g. "8.8.8.8:53".
                                   # Blank = system resolver, auto-detected and
                                   # reported in the dns.server attribute.

      - endpoint: "https://example.com/health"  # HTTP target: full URL
        method: http
        http_method: HEAD          # HTTP verb. Default HEAD.
        timeout: 10s               # Request timeout.
        tls:
          insecure_skip_verify: false

      - endpoint: "8.8.8.8"        # DNS server to probe (IP or hostname; port 53 assumed if omitted)
        method: dns
        dns_query: "example.com"   # Hostname to resolve. Required.
        dns_record_type: A         # Record type: A (default), AAAA, CNAME, MX, TXT.
        timeout: 5s

    traceroute:
      enabled: false               # Disabled by default.
      method: udp                  # "udp" (default, no root on Linux) or "icmp" (requires root/admin).
      max_hops: 30
      timeout: 3s                  # Per-hop timeout.

      # Interval-based: run a traceroute every N times this target is probed.
      # 0 = disabled. Example: interval 10 + collection_interval 1m = traceroute every 10 minutes.
      interval: 0

      # Failure-based: run a traceroute when ICMP packet loss >= failure_threshold.
      on_failure: false
      failure_threshold: 0.5       # 0.0–1.0. Default 0.5 (50% packet loss).
```

## Logs

The receiver can emit log records as well as metrics. Which signals it produces is
decided by pipeline membership, not by configuration — reference the receiver from a
logs pipeline and it emits logs, from a metrics pipeline and it emits metrics, from
both and it emits both.

Two probe types produce records, because for them the unit of meaning is a
transaction rather than a number:

- **Traceroute** — one record per trace, with the hops as an ordered array. The path
  is a single observation, so keeping it together avoids reassembling it at query time
  and makes a route change a diff between two records.
- **HTTP** — one record per transaction, with the phases together. Averaged metric
  series cannot answer "this check was slow, which phase caused it?", because the
  phases are independent series that cannot be correlated back to one request.

**DNS and ICMP probes emit no records.** A single duration plus a status is a metric,
and turning it into a log costs cheap aggregation, native threshold alerting, and
usually retention, in exchange for nothing.

### Sharing one probe cycle

A receiver wired into both pipelines is instantiated twice by the collector. The
receiver shares probe execution between the two instances, so each target is probed
**once** per collection interval and both signals describe the same observation. Both
signals run on the same `collection_interval`.

### HTTP record

```jsonc
{
  "Timestamp": "<request start, not completion>",
  "SeverityText": "INFO",              // ERROR when the check fails
  "Attributes": {
    "server.address": "https://www.cloudflare.com",
    "server.resolved_ip": "104.16.124.96",
    "http.request.method": "GET",
    "http.response.status_code": 200,
    "http.response.size": 1256,
    "network.protocol.version": "HTTP/2.0",
    "tls.cert.days_remaining": 61.4,
    "dns.server": "1.1.1.1:53"
  },
  "Body": {
    "phases": {
      "dns_ms": 2.1, "connect_ms": 11.4, "tls_ms": 184.7,
      "write_ms": 0.3, "ttfb_ms": 8.2, "total_ms": 206.7
    },
    "tls": {
      "version": "TLS 1.3",
      "cipher": "TLS_AES_128_GCM_SHA256",
      "cert": { "issuer": "...", "subject": "...", "not_after": "...", "days_remaining": 61.4 }
    }
  }
}
```

A failed check is `SeverityText: ERROR` and adds an `error` block with the failing
phase and the underlying message. This matters because a failed HTTP probe reports
status code `0` regardless of cause — the error text is the only thing separating a
DNS failure from a refused connection from a timeout.

Phase semantics match the corresponding metrics exactly, including their quirks:
`connect_ms` is measured from DNS-done rather than from connection start, `write_ms`
spans the TLS handshake, and `ttfb_ms` is time to first byte rather than a full body
read.

### Traceroute record

```jsonc
{
  "Timestamp": "<trace start>",
  "SeverityText": "INFO",              // WARN when the destination was not reached
  "Attributes": {
    "server.address": "www.cloudflare.com",
    "server.resolved_ip": "104.16.124.96",
    "traceroute.method": "udp",
    "traceroute.hop_count": 11,
    "traceroute.hops_answered": 8,
    "traceroute.reached_dest": true,
    "traceroute.aborted_early": false
  },
  "Body": {
    "hops": [
      { "index": 1, "address": "172.16.1.1",     "timed_out": false, "rtt_ms": 0.443 },
      { "index": 2, "address": "10.112.162.67",  "timed_out": false, "rtt_ms": 11.34 },
      { "index": 3, "address": "*",              "timed_out": true }
    ]
  }
}
```

Hops that never answered are included with `timed_out: true` and the conventional `*`
address, carrying no `rtt_ms` — the only duration available for such a hop is the
timeout that was configured, which is not a measurement. `traceroute.aborted_early`
distinguishes a path truncated by the consecutive-timeout bail-out from a genuinely
short one.

### Security

Request and response **headers and bodies are never recorded**, and there is no option
to enable it. Auth headers, cookies, and payloads are exactly what HTTP checks carry.
Only the response size is captured.

Endpoint credentials are stripped by default. A target configured as
`https://user:pass@host` reaches records as `https://user:xxxxx@host`. Disable with
`logs.redact_url_userinfo: false` only if endpoints are known to carry no credentials.

### Volume

Records scale with targets and interval, not with hops: one traceroute record per trace
rather than one per hop. At a 60s interval that is ~1,440 records per target per day for
HTTP plus the same for traceroute — roughly 30x fewer than per-hop records would be.

### Configuration

```yaml
receivers:
  networkcheck:
    logs:
      # Include certificate and handshake detail in HTTPS records. Default true.
      include_tls_details: true
      # Strip credentials from endpoints before they reach a record. Default true.
      redact_url_userinfo: true
```

### Example

```yaml
service:
  pipelines:
    metrics:
      receivers: [networkcheck]
      exporters: [otlphttp]
    logs:
      receivers: [networkcheck]
      exporters: [otlphttp]
```

## Metrics

### Failed probes

A probe that fails emits only its outcome metric and no timings:

| Failure | Emitted | Suppressed |
|---------|---------|------------|
| DNS query failed or timed out | `network.dns.status` = 0 | `network.dns.lookup_duration` |
| HTTP request failed or timed out | `network.http.status` = 0 | all six `network.http.*` durations |
| ICMP lost every packet | `network.ping.packet_loss` = 1.0 | `network.ping.latency_min`/`avg`/`max` |
| Traceroute hop did not answer | nothing for that hop | `network.traceroute.hop.latency` |

The timing metrics are suppressed because a failed probe has no duration to
report. The only figure available is how long the receiver waited before giving
up, and the per-phase timers never fire at all, so publishing them would write
the configured timeout into the latency series as though it were a measurement
and report 0 ms for phases that never ran.

Alert on the status metrics, not on durations. A failure produces a gap in the
timing series rather than a fabricated value, so averages and percentiles over
those series stay meaningful.

### DNS targets

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `network.dns.status` | Gauge | 1 | 1 = server responded successfully, 0 = error or timeout |
| `network.dns.lookup_duration` | Gauge | ms | Time for the DNS server to respond to the query |

Attributes: `dns.query` (the hostname that was resolved)

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
