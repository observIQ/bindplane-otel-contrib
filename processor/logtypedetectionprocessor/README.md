# Log Type Detection Processor

Detects the type of logs passing through it.

## Supported pipelines

- Logs

## Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|

## Example configuration

```yaml
processors:
  log_type_detection:
```

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
BenchmarkFingerprintJSONLogs-14    381608 ns/op    959.82 MB/s  763.2 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintXMLLogs-14     124363 ns/op   1562.09 MB/s  621.8 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintCLFLogs-14      29934 ns/op    948.61 MB/s  149.7 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintSyslogLogs-14   56601 ns/op    746.47 MB/s  283.0 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintGenericLogs-14  22239 ns/op    684.79 MB/s  111.2 ns/record   0 B/op   0 allocs/op
```

```sh
go test -run XXX -bench BenchmarkFingerprint -benchmem ./internal/fingerprint
```
