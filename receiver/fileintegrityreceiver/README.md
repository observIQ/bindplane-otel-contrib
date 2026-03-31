# File integrity monitoring receiver

The `file_integrity` receiver watches configured files and directories using [fsnotify](https://github.com/fsnotify/fsnotify) and emits one OpenTelemetry log record per filesystem event. Optional SHA-256 hashing (debounced) works with the `threat_enrichment` processor for IOC matching on paths, extensions, or hashes.

| Status | Stability | Pipelines |
|--------|-----------|-----------|
| Alpha  | Alpha     | Logs      |

## Supported platforms

Linux (inotify), macOS/BSD (kqueue), Windows (ReadDirectoryChangesW), per fsnotify. Network filesystems and some paths (`/proc`, SMB) may not report events reliably.

## How it works

1. On start, the receiver adds watches for each configured path. With `recursive: true`, it walks subdirectories and adds a watch per directory; new directories under a watched tree are watched when they appear.
2. fsnotify delivers create, write, remove, rename, and chmod-style events. Each event yields a log with attributes such as `file.path`, `file.extension`, and `fim.operation`.
3. If `hashing.enabled` is true, create/write events for regular files are debounced; after `hashing.debounce` elapses, the receiver reads up to `hashing.max_bytes` and sets `file.hash.sha256`. Files larger than `hashing.max_bytes` set `file.hash.skipped` and `file.hash.skip_reason`.

## Configuration

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `paths` | `[]string` | | yes | Existing files or directories to watch. |
| `recursive` | bool | false | no | Recursively watch subdirectories when the path is a directory. |
| `exclude` | `[]string` | nil | no | Glob patterns (`filepath.Match`) or plain paths; plain paths exclude that path and descendants. |
| `hashing` | object | see below | no | Optional content hashing. |

### `hashing`

| Field | Type | Default | Required when hashing | Description |
|-------|------|---------|------------------------|-------------|
| `enabled` | bool | false | | Enable SHA-256 for debounced create/write on regular files. |
| `debounce` | duration | `2s` | yes when enabled | Coalesce rapid writes before hashing. |
| `max_bytes` | int64 | `33554432` (32 MiB) | yes when enabled | Skip hashing when file size exceeds this value. |

## Log fields

| Attribute | Description |
|-----------|-------------|
| `file.path` | Path from the event (cleaned). |
| `file.name` | Base name. |
| `file.extension` | Extension including dot, if any. |
| `fim.operation` | `create`, `write`, `remove`, `rename`, `chmod`, or `unknown`. |
| `fsnotify.op` | Raw fsnotify op string. |
| `file.hash.sha256` | Hex digest when hashing succeeds. |
| `file.hash.skipped` | True when hashing was skipped. |
| `file.hash.skip_reason` | Reason for skip (e.g. `file exceeds max_bytes`). |
| `file.hash.error` | Error opening or reading the file for hash. |

Resource attribute `fim.receiver` is set to `file_integrity`. The log body is a short summary, e.g. `FIM write /var/www/html/shell.php`.

## Example with threat enrichment

```yaml
receivers:
  file_integrity:
    paths: [/var/www/html]
    recursive: true
    exclude: [/var/www/html/tmp]
    hashing:
      enabled: true
      debounce: 2s
      max_bytes: 33554432

processors:
  threat_enrichment:
    filter:
      kind: bloom
      max_estimated_count: 100000
      false_positive_rate: 0.001
    rules:
      - name: bad_hashes
        indicator_file: /etc/otel/indicators/malware_sha256.txt
        lookup_fields: ["file.hash.sha256"]
      - name: web_extensions
        indicator_file: /etc/otel/indicators/suspicious_ext.txt
        lookup_fields: ["file.extension"]

service:
  pipelines:
    logs:
      receivers: [file_integrity]
      processors: [threat_enrichment]
      exporters: [your_exporter]
```

## Registration

This component ships from `bindplane-otel-contrib`; register the factory in the Bindplane OpenTelemetry Collector build (follow-up in the collector repository).
