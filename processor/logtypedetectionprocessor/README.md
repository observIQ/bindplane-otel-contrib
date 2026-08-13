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
BenchmarkFingerprintJSONLogs-14    382226 ns/op    958.26 MB/s  764.5 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintXMLLogs-14     126420 ns/op   1536.67 MB/s  632.1 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintCLFLogs-14      30039 ns/op    945.32 MB/s  150.2 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintSyslogLogs-14   56155 ns/op    752.40 MB/s  280.8 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintGenericLogs-14  21762 ns/op    699.80 MB/s  108.8 ns/record   0 B/op   0 allocs/op
```

```sh
go test -run XXX -bench BenchmarkFingerprint -benchmem ./internal/fingerprint
```
