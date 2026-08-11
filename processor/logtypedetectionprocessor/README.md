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

Fingerprinting the 500 JSON log records in `testdata/jsonLogs.csv` and the 200 XML
log records in `testdata/xmlLogs.csv`:

```
goos: darwin
goarch: arm64
cpu: Apple M4 Pro
BenchmarkFingerprintJSONLogs-14   362728 ns/op   1009.77 MB/s  725.5 ns/record   0 B/op   0 allocs/op
BenchmarkFingerprintXMLLogs-14    126963 ns/op   1530.10 MB/s  634.8 ns/record   0 B/op   0 allocs/op
```

```sh
go test -run XXX -bench BenchmarkFingerprint -benchmem .
```
