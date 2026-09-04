# Log Type Detection Processor

Detects the type of logs passing through it based on their structure and records the result as an attribute
on each log record.

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
| max_saved_fingerprints | int | `10000` | Maximum number of fingerprint-to-log-type mappings cached in memory. Once full, the least recently seen fingerprint is evicted. Evicted mappings are also dropped from `fingerprint_storage` on the next save. |

### Matchers

| Field    | Type   | Default | Description                                                                                                                                            |
| -------- | ------ | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| name     | string |       | The log type assigned when this matcher matches. Required.                                                                                              |
| method   | string |       | How the matcher tests the log body. One of `regex`, `starts_with`. Required.                                                                            |
| value    | string |       | The pattern to test with: an [RE2](https://github.com/google/re2/wiki/Syntax) expression for `regex`, a literal prefix for `starts_with`. Required.      |
| priority | int    | unset   | Order matchers are tested in, lowest first. Matchers with no priority are tested after all matchers that have one. Matchers of equal priority keep their configured order. |

Matchers test the log body as a string, regardless of its underlying type. A map
or slice body is stringified before it is tested. Target the log structure to avoid false positives.

### Example Config

Detects Windows event logs, RFC 5424 syslog, and Kubernetes audit logs from a file, writing the result
to the `log_type` attribute of each log record.

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
ratio is the share of log volume the matchers cover.

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
