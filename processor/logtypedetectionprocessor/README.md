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

Fingerprinting the 500 JSON log records in `testdata/jsonLogs.csv`:

```
goos: darwin
goarch: arm64
cpu: Apple M4 Pro
BenchmarkFingerprintJSONLogs-14   407224 ns/op   899.44 MB/s   814.4 ns/record   0 B/op   0 allocs/op
```

```sh
go test -run XXX -bench BenchmarkFingerprintJSONLogs -benchmem .
```
