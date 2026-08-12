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

Fingerprinting the 500 JSON log records in `internal/fingerprint/testdata/jsonLogs.csv`,
the 200 XML log records in `internal/fingerprint/testdata/xmlLogs.csv`, and the 200 CLF
log records in `internal/fingerprint/testdata/clfLogs.csv`:

```
goos: darwin
goarch: arm64
cpu: Apple M4 Pro
BenchmarkFingerprintJSONLogs-14   388444 ns/op    942.92 MB/s  776.9 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintXMLLogs-14    133741 ns/op   1452.55 MB/s  668.7 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintCLFLogs-14     29976 ns/op    947.28 MB/s  149.9 ns/record   0 B/op   0 allocs/op
```

```sh
go test -run XXX -bench BenchmarkFingerprint -benchmem ./internal/fingerprint
```
