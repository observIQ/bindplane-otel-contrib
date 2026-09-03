# Log Type Detection Processor

This processor allows the classification of logs passing through it based on their structure and records the result
as an attribute on each log record.

## Supported pipelines

- Logs

## How It Works

1. The user configures a list of `matchers`, each pairing a log type name with a
   pattern to test log bodies against.
2. For each log record, the processor fingerprints the log body. The fingerprint
   is a hash of the log's structure rather than its content, so all log records
   sharing a structure produce the same fingerprint. It is recorded as an
   attribute when `fingerprint_field` is set.
3. The first time a fingerprint is seen, the matchers are tested against the body
   in priority order and the first match wins. The result is cached by
   fingerprint, so later records sharing that structure skip matching entirely.
   Matching cost is proportional to the number of distinct log structures
   observed, not to the volume of logs processed.
4. The detected log type is written to the `log_type_field` attribute. Records
   that match no matcher, and records under 10 characters after trimming
   whitespace, are assigned the log type `unknown`. A record too short to
   fingerprint receives no `fingerprint_field` attribute.

## Configuration

| Field             | Type   | Default         | Description                                                                                                                                              |
| ----------------- | ------ | --------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| log_type_field    | string | `log_type`      | Attribute the detected log type is written to. Required. Every log record receives this attribute, including those detected as `unknown`.                  |
| fingerprint_field | string | `fingerprint`   | Attribute the log's structure fingerprint is written to, hex encoded. Set to an empty string to omit it. |
| matchers          | list   | `[]`            | Matchers tested against each log body with a unique structure. See [Matchers](#matchers). When empty, all log records are detected as `unknown`.                                   |
| fingerprint_storage | component ID | | ID of a storage extension used to persist the fingerprint-to-log-type map across restarts. The map is loaded on startup, saved periodically, and saved on shutdown. The persisted map is tied to the `matchers` it was detected with, so editing, renaming, reordering, or removing a matcher discards it and log types are detected again from scratch. |
| fingerprint_persist_interval | duration | `5m` | How often the fingerprint map is written to the storage extension. Only used when `fingerprint_storage` is set. |
| opamp | component ID | | ID of an opamp extension. When set, the processor asks the opamp server for matchers on startup and merges them with the `matchers` below. See [OpAMP Matchers](#opamp-matchers). |
| opamp_request_timeout | duration | `30s` | How long startup waits for the opamp server to answer. Set to `0` to wait indefinitely. Only used when `opamp` is set, and only when no matchers are stored locally. |
| matcher_storage | component ID | | ID of a storage extension used to persist the matchers received over opamp, with the version they came with. Requires `opamp`. Without it the matchers are fetched again on every startup. |
| max_saved_fingerprints | int | `10000` | Maximum number of fingerprint-to-log-type mappings cached in memory. The cache is LRU; once full, the least recently seen fingerprint is evicted. Evicted mappings are also dropped from `fingerprint_storage` on the next save. |

### Matchers

| Field    | Type   | Default | Description                                                                                                                                            |
| -------- | ------ | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| name     | string |       | The log type assigned when this matcher matches. Required.                                                                                              |
| method   | string |       | How the matcher tests the log body. One of `regex`, `starts_with`. Required.                                                                            |
| value    | string |       | The pattern to test with: an [RE2](https://github.com/google/re2/wiki/Syntax) expression for `regex`, a literal prefix for `starts_with`. Required.      |
| priority | int    | unset   | Order matchers are tested in, lowest first. Matchers with no priority are tested after all matchers that have one. Matchers of equal priority keep their configured order. |

Matchers test the log body as a string, regardless of its underlying type. A map
or slice body is stringified before it is tested. It is recommended that the matcher targets
the log structure to avoid false positives.

### OpAMP Matchers

When `opamp` is set, the processor registers the `com.bindplane.logtypedetection`
custom capability with the opamp extension and asks the server for matchers on
startup.

Matchers are versioned with [semver](https://semver.org). The processor reports
the version it holds and the server answers with either `updateMatchers`, if it
has a newer set, or `matchersUpToDate`. A full matcher set only crosses the wire
when the version has actually changed.

Only a higher version of the **same major** is taken up. A major bump is treated
as a breaking change the running collector may not understand, so it is refused
and the matchers in use are kept — upgrade the collector to move to a new major.
When no version is held yet, whatever the server offers is accepted. The version
check is the only exchange: the server is asked at startup and does not push
matchers at other times, so a matcher change takes effect on the next restart.

Matchers from the server are merged with the `matchers` in the config and the
combined set is ordered by `priority` as usual. Matchers of equal priority keep
config-first order. Server matchers are validated the same way configured ones
are; an invalid or refused set is ignored and does not disturb the matchers in
use.

#### Startup

With `matcher_storage` set, the stored matchers are put in use before startup
finishes and the version check runs in the background, so a restart is not
delayed and an unreachable server does not hold up the collector.

With nothing stored — no `matcher_storage`, or the first ever start — startup
blocks until the server answers, retrying every 5s up to
`opamp_request_timeout` (`0` waits indefinitely; shutdown still interrupts it).
The collector starts receivers after processors, so no logs are read while this
waits and none are labelled with an incomplete matcher set. A timeout is not
fatal: the processor starts with the matchers from its config and applies the
server's whenever they arrive.

When `fingerprint_storage` is also set, the persisted fingerprint map is held
back until the matcher set is final and then restored, so a restart does not
relabel log structures that were already detected. Pointing `matcher_storage`
and `fingerprint_storage` at the same extension is supported.

#### Messages

All three message types carry the same YAML payload. `processor` must be the
full component ID of the processor the message is for; a message naming a
different processor is ignored.

`requestMatchers`, sent by the processor — `version` is empty on a first run:

```yaml
processor: log_type_detection
version: 1.2.3
```

`updateMatchers`, sent by the server when it has something newer:

```yaml
processor: log_type_detection
version: 1.3.0
matchers:
  - name: k8s_audit
    method: starts_with
    value: '{"kind":"Event"'
    priority: 1
```

`matchersUpToDate`, sent by the server when the reported version is current:

```yaml
processor: log_type_detection
version: 1.2.3
```

### Example Config

The following config detects Windows event logs, RFC 5424 syslog, and Kubernetes
audit logs from a file, writing the result to the `log_type` attribute of each log
record.

```yaml
extensions:
  file_storage:
receivers:
  filelog:
    include: [./example/mixed.log]
processors:
  log_type_detection:
    log_type_field: log_type
    fingerprint_field: fingerprint
    matchers:
      - name: win_event_log
        method: regex
        priority: 1
        value: (?s)^\s*(?:<\?xml[^>]*\?>\s*)?<Event\b[^>]*>\s*<System>.*?<Provider\b[^>]*\bGuid=.*?<EventRecordID>
      - name: syslog_rfc5424
        method: starts_with
        priority: 2
        value: "<1"
      - name: k8s_audit
        method: regex
        value: '"kind"\s*:\s*"Event".*"apiVersion"\s*:\s*"audit\.k8s\.io'
    fingerprint_storage: file_storage
    fingerprint_persist_interval: 5m
exporters:
  debug:

service:
  extensions: [file_storage]
  pipelines:
    logs:
      receivers: [filelog]
      processors: [log_type_detection]
      exporters: [debug]
```

## Internal Telemetry

The metrics emitted by this processor are documented in
[documentation.md](./documentation.md).

`attempts` and `attempts_matched` increment once per newly observed log
structure, so their ratio is the share of distinct structures the matchers cover.
`logs_classified` and `logs_unclassified` increment once per log record, so their
ratio is the share of log volume the matchers cover. The two ratios differ
whenever the uncovered structures are more or less frequent than the covered
ones.

`logs_unclassified` counts both log records no matcher matched and log records
too short to fingerprint.

## Benchmarks

Fingerprinting the following:
- 500 JSON log records in `internal/fingerprint/testdata/jsonLogs.csv`
- 200 XML log records in `internal/fingerprint/testdata/xmlLogs.csv`
- 200 CLF log records in `internal/fingerprint/testdata/clfLogs.csv`
- 200 syslog records in `internal/fingerprint/testdata/sysLogs.csv`
- 200 generic (key=value, CSV, plain text) log records in `internal/fingerprint/testdata/genericLogs.csv`

```
goos: darwin
goarch: arm64
cpu: Apple M4 Pro
BenchmarkFingerprintJSONLogs-14    382226 ns/op    958.26 MB/s  764.5 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintXMLLogs-14     126420 ns/op   1536.67 MB/s  632.1 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintCLFLogs-14      30039 ns/op    945.32 MB/s  150.2 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintSyslogLogs-14   56155 ns/op    752.40 MB/s  280.8 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintGenericLogs-14  21762 ns/op    699.80 MB/s  108.8 ns/record   0 B/op   0 allocs/op
```

```sh
go test -run XXX -bench BenchmarkFingerprint -benchmem ./internal/fingerprint
```
