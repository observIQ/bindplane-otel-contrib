# Azure Blob Storage Polling Receiver

Continuously polls Azure Blob Storage at configurable intervals and dynamically adjusts the time window to collect only new data from each interval.

The receiver was originally built to pair with the [Azure Blob Exporter](../../exporter/azureblobexporter/README.md), which writes OTLP JSON blobs under a `year=/month=/day=/hour=/minute=/` folder structure. It also supports arbitrary Azure Blob layouts via configurable [`time_pattern`](#mode-3-custom-time-pattern) / [`use_last_modified`](#mode-2-lastmodified-timestamp) modes, [glob `root_folder`](#glob-root-folders) expansion across multiple resource directories, and several [blob payload formats](#blob-format) (`otlp`, NDJSON, raw text, and `{"records":[...]}` envelopes used by Azure NSG flow logs and most diagnostic-settings exports).

## Important Note

Unlike the `azureblobrehydrationreceiver` which is a one-time rehydration receiver, this receiver operates continuously. It polls Azure Blob Storage at regular intervals and automatically adjusts the time window to collect only new blobs since the last poll, making it suitable for production monitoring scenarios.

## Comparison with Azure Blob Rehydration Receiver

| Feature                 | azureblobpollingreceiver         | azureblobrehydrationreceiver      |
| ----------------------- | -------------------------------- | --------------------------------- |
| Operation Mode          | Continuous polling               | One-time rehydration              |
| Time Range              | Dynamic (based on poll interval) | Static (user-specified)           |
| Use Case                | Production monitoring            | Historical data recovery          |
| Configuration           | `poll_interval`                  | `starting_time` and `ending_time` |
| Stops After Empty Polls | No                               | Yes (after 3 consecutive)         |

## Minimum Agent Versions

- Introduced: v1.92.0

## Supported Pipelines

- Metrics
- Logs
- Traces

## How it works

1. On startup, the receiver loads the checkpoint from storage (if configured) to determine where it left off.
2. The receiver immediately runs the first poll, looking back by `initial_lookback` duration (defaults to `poll_interval`).
3. For each subsequent poll at the `poll_interval`:
   - The receiver calculates a dynamic time window from the last poll time to now
   - It streams blobs from Azure Blob Storage in the specified container
   - Each blob path is parsed to extract the timestamp and telemetry type
   - Blobs within the time window and matching the receiver's telemetry type are downloaded and processed (append-growable formats can be read incrementally when `enable_incremental_read` is set; see [Incremental reading](#incremental-reading-of-append-growable-blobs))
   - The checkpoint is updated with the current poll time and processed blobs
4. The cycle repeats continuously until the collector is shut down.

### Dynamic Time Windows

The receiver automatically manages time windows:

- **First Poll**: Uses `initial_lookback` to determine how far back to look (e.g., if `initial_lookback: 1h`, it will process blobs from the last hour)
- **Subsequent Polls**: Uses the timestamp of the last successful poll as the start time, and the current time as the end time
- **After Restart**: If a checkpoint exists, resumes from the last poll time; otherwise, uses `initial_lookback` again

### Incremental reading of append-growable blobs

Incremental reading is opt-in. Set `enable_incremental_read: true` to turn it on for the `records-json` and `json` formats. When it is off (the default), every format keeps the legacy behavior of parsing each blob whole on every poll and deduping by name, which loses records appended to a blob after it is first read.

> **Temporary flag.** `enable_incremental_read` exists so the incremental path can be adopted gradually. It will become the default and then be removed once the path is proven, at which point the legacy whole-blob behavior goes away.

> **Enabling on an existing deployment re-reads recent blobs once.** Turning the flag on starts per-blob offset tracking from scratch, and the widened revisit window (see below) re-lists blobs the legacy path already consumed. On the first incremental poll each such blob is read once from the beginning, so already-ingested records are delivered again (delivery is at-least-once). This is a one-time cost at the switchover, bounded by `incremental_revisit_window`.

> **Turning the flag back off resumes legacy semantics from the current poll time.** The incremental path advances the poll-time watermark every poll, so after disabling the flag the legacy path windows from the recent poll time, not the pre-incremental one. It re-reads only blobs whose blob-time falls in that window (a small duplication), and a blob still growing at the switch is read whole once and deduped by name, so its later appends are not ingested. Prefer switching the flag while blobs are quiescent.

> **Changing `blob_format` (or `enable_per_line_text`) with a persisted checkpoint is unsupported.** Stored per-blob offsets are byte positions under the old framing; reusing them under a different framing can mis-frame a blob still being tracked. Change the format only with a fresh checkpoint (a new `storage` key, or after tracked blobs have sealed).

With the flag set, the `records-json` and `json` formats are read **incrementally**. Some sources grow a blob in place instead of writing it once: Azure NSG/VNet flow logs, for example, write one `PT1H.json` per hour and append records to it every minute for the whole hour. For these formats the receiver reads only the bytes added since its last read, tracked per blob, so records appended after a blob first appears are still ingested, and already-consumed bytes are not re-read in normal operation (delivery is at-least-once).

To do this the receiver:

- **Revisits growing blobs.** Each poll looks back an extra `incremental_revisit_window` (default 2h) on top of the normal poll window, so a blob whose path time has already left the narrow window is still re-listed until it stops growing. A blob is treated as sealed once it has had no observed change for that window, which is also when it is deleted under `delete_on_read`. On the **first poll** this widening also applies on top of `initial_lookback`, so the first poll effectively looks back at least `incremental_revisit_window` even when `initial_lookback` is shorter. This is intentional (an hourly flow-log blob is typically already up to an hour old when first seen), but it means a small `initial_lookback` does not limit how far back the first incremental poll reaches; lower `incremental_revisit_window` if you need a tighter first-poll bound.
  - **Path-time modes and long-lived blobs:** when blob time comes from the path (`time_pattern` or the default folder layout) rather than `use_last_modified`, a blob is re-listed only while its *path time* is within the widened window. A blob still appended to after its path time leaves that window is no longer re-listed, so it seals against its last observed change and later appends are not ingested (and under `delete_on_read` it can be deleted while still being written). Set `incremental_revisit_window` above the longest span a blob is appended to past its path time, or use `use_last_modified`, which re-lists on the live modified time and avoids this limit. The reverse case — a blob whose modified time falls slightly *before* its path time (clock skew, or a path that encodes the period end while the blob is written at the period start) — is absorbed by a small built-in tolerance (5 minutes) so minor skew does not seal a blob that is still listed; a larger gap remains subject to the limit above.
- **Skips unchanged blobs cheaply.** A blob whose `LastModified` has not changed since the last read is skipped without a download.
- **Detects replacement.** A short fingerprint of each blob's leading bytes is stored. If a blob is replaced or truncated so the fingerprint no longer matches, it is re-read from the start. Narrow exception (accepted limitation): a blob replaced *between* a poll's identity and delta reads whose new `LastModified` matches the old one to Azure's one-second granularity can be skipped by the mtime gate until its entry prunes; `use_last_modified`, or any source that bumps `LastModified` on rewrite, avoids it.
- **Reads to the last complete record.** For `records-json` the receiver stops at the last complete record and leaves the array's closing `]}` in place, so a source that rewrites that footer as it appends cannot corrupt the read. For `json` it stops at the last newline, and flushes a final line written without a trailing newline once the blob stops growing.
- **Is more lenient than the whole-blob path on a malformed document.** The whole-blob `records-json` parser rejects a document outright if its `records` array holds a non-object element. The incremental path instead emits the complete records before that element and quarantines the blob (keeping it, never deleting it under `delete_on_read`) rather than dropping everything.
- **Bounds a blob that can never be finalized.** A blob whose seal-time flush or delete keeps failing is retried, but after the quarantine retention window (7 days) its checkpoint entry is dropped (the blob is left in Azure) so a persistent failure cannot grow the checkpoint without bound.

Gzip-compressed blobs cannot be range-read, so they are read whole; incremental byte-offset reading requires uncompressed blobs. A gzip blob is re-read in full on any `LastModified` change, so incremental works best when gzip blobs are written once. Two limitations follow for a gzip blob that changes after its first read (rewritten larger, or an appended gzip member for `json`/NDJSON):

- **Duplicate delivery.** Each growth re-reads and re-decompresses the whole blob, so already-delivered records are emitted again (at-least-once, not lost). Uncompressed blobs avoid this via byte-offset tracking; gzip cannot.
- **Same-second final append.** Azure `LastModified` is second-grained, so a final append landing in the same clock second as the read that consumed the blob does not change `LastModified` and is not re-read. The uncompressed formats recover such an append with a final seal-time flush; the gzip path cannot range-tail, so under `delete_on_read` that last same-second append can be deleted unread.

`otlp` blobs are written once rather than grown, so they too are read whole.

Delivery is **at-least-once**: no record is lost, though a record may be delivered more than once (for example after a restart without a storage extension, or if a blob is rewritten). Use a [storage extension](#using-storage-extension-configuration) to persist per-blob progress across restarts.

### Checkpoint Management

The receiver uses a checkpoint to track:

- `LastPollTime`: the timestamp when the last poll completed successfully.
- `LastTs` / `ParsedEntities`: for formats not read incrementally (`otlp`, and any format with `enable_incremental_read` off), the timestamp and set of blob names already processed in the current time bucket.
- `Progress`: for the append-growable formats, the per-blob read offset, leading-byte fingerprint, and last-modified time used for [incremental reading](#incremental-reading-of-append-growable-blobs).

This prevents duplicate processing and, with a storage extension, preserves incremental read progress across collector restarts.

## Configuration

| Field             | Type     | Default               | Required | Description                                                                                                                                                                 |
| ----------------- | -------- | --------------------- | -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| connection_string | string   |                       | `true`   | The connection string to the Azure Blob Storage account. Can be found under the `Access keys` section of your storage account.                                              |
| container         | string   |                       | `true`   | The name of the container to poll from.                                                                                                                                     |
| poll_interval     | duration |                       | `true`   | The interval at which to poll for new blobs. Must be at least 1 minute. The receiver will continuously poll at this interval and collect blobs created since the last poll. |
| root_folder       | string   |                       | `false`  | The root folder that prefixes the blob path. Should match the `root_folder` value of the Azure Blob Exporter. Supports glob patterns (`*`, `?`, `[...]`) to match multiple directories — see [Glob Root Folders](#glob-root-folders). |
| initial_lookback  | duration | same as poll_interval | `false`  | The duration to look back on the first poll when no checkpoint exists. For example, if set to `1h`, on first startup the receiver will look for blobs from the last hour.   |
| delete_on_read    | bool     | `false`               | `false`  | If `true` the blob is deleted after it is processed. **Only when `enable_incremental_read` is set** is the delete deferred until the blob seals (no observed change for `incremental_revisit_window`), so a blob still being appended to is not deleted mid-stream; with the flag off the blob is deleted right after its first whole read, losing any later appends. If a source pauses writes for longer than that window mid-blob, the blob may be treated as sealed and deleted early, so size `incremental_revisit_window` above the longest expected write pause. |
| storage           | string   |                       | `false`  | The component ID of a storage extension. The storage extension persists checkpoint data across collector restarts, ensuring no data loss or duplication.                    |
| batch_size        | int      | `30`                  | `false`  | The number of blobs to download and process in the pipeline simultaneously. This parameter directly impacts performance by controlling the concurrent blob download limit.  |
| page_size         | int      | `1000`                | `false`  | The maximum number of blob information to request in a single API call.                                                                                                     |
| blob_format       | string   | `otlp`                | `false`  | The format of blob contents. Supported values: `otlp`, `json`, `text`, `records-json`. See [Blob Format](#blob-format) below.                                              |
| enable_incremental_read | bool | `false`             | `false`  | Opt into [incremental reading](#incremental-reading-of-append-growable-blobs) of append-growable blobs for the `records-json` and `json` formats. Off by default, every format parses each blob whole and dedupes by name. Temporary: this becomes the default and is then removed once the incremental path is proven. |
| incremental_revisit_window | duration | `2h`           | `false`  | How far back each poll re-lists blobs to catch in-place appends, and how long after a blob's last observed change it is held before being treated as sealed (and deleted under `delete_on_read`). Only used when `enable_incremental_read` is set. The 2h default fits Azure NSG/VNet flow logs (one blob per hour); raise it for a source appended to across a longer span or whose writes pause longer mid-blob. Must be at least `poll_interval` (config error otherwise), since a window shorter than the poll cadence would seal blobs mid-write. |
| fingerprint_size  | int      | `512`                 | `false`  | Number of leading blob bytes used to identify an append-growable blob across reads, mirroring the filelog receiver's `fingerprint_size`. Raise it when blobs share a long common prefix (fewer head collisions); lower it to shrink the per-poll identity read. Only used when `enable_incremental_read` is set. Minimum 16; `0` selects the default. Lowering it below the size of an already-stored fingerprint forces a one-time re-read of each tracked blob from the start (duplicate delivery, no loss); raising it grows fingerprints in place. |
| assume_append_only | bool    | `false`               | `false`  | Skip the per-poll blob-identity check, saving one Azure read operation per poll per growing blob. **Only set this when a blob at a given name is never replaced or truncated in place** (append-only sources such as Azure NSG/VNet flow logs). If that guarantee is violated, the receiver emits stale bytes as records (blob replaced larger) or silently misses the new content (replaced smaller), with no error. Only used when `enable_incremental_read` is set. While this is on, a blob's stored fingerprint stays at whatever length its first read captured (growing it needs the identity read this flag skips); it grows to `fingerprint_size` on the first poll after the flag is turned off. |

## Blob Format

By default, the receiver expects blobs to contain OTLP-formatted JSON (as written by the Azure Blob Exporter). The `blob_format` option allows the receiver to parse other formats.

| Format         | Description                                                                                                                                                                                                                                                                                                                            |
| -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `otlp`         | (Default) OTLP JSON format. Blobs are unmarshaled using the standard OpenTelemetry `plog.JSONUnmarshaler`. Use this when blobs were written by the Azure Blob Exporter.                                                                                                                                                                |
| `json`         | Newline-delimited JSON (NDJSON). Each line is parsed as a JSON object and becomes a separate log record with the parsed object as the body. Malformed lines are skipped with a warning.                                                                                                                                               |
| `text`         | Raw text. The entire blob content is set as the body of a single log record.                                                                                                                                                                                                                                                          |
| `records-json` | Single JSON document with a top-level `records` array. Each element of the array becomes one log record with the element as the body. Use this for Azure NSG flow logs and most Azure diagnostic-settings exports, which ship as `{"records":[...]}`. Malformed records are skipped; an invalid top-level document is an error. |

Non-`otlp` formats are only supported on **logs** pipelines. Metrics and traces pipelines only support `otlp`.

### NDJSON Example

For blobs containing newline-delimited JSON such as:

```json
{"_time":1770273621.586,"host":"server1","_raw":"Connection established"}
{"_time":1770273622.100,"host":"server2","_raw":"Request completed"}
```

Configure the receiver with `blob_format: json`:

```yaml
azureblobpolling:
  connection_string: "..."
  container: "raw-logs"
  poll_interval: 1m
  use_last_modified: true
  telemetry_type: "logs"
  blob_format: "json"
  storage: "file_storage"
```

Each JSON line becomes a log record with:
- **Body**: A map containing the parsed JSON fields (e.g., `_time`, `host`, `_raw`)
- **ObservedTimestamp**: Set to the time the blob was processed

### Raw Text Example

For blobs containing plain text (e.g., raw syslog):

```yaml
azureblobpolling:
  connection_string: "..."
  container: "syslog-archive"
  poll_interval: 5m
  use_last_modified: true
  telemetry_type: "logs"
  blob_format: "text"
  storage: "file_storage"
```

The entire blob content is set as the string body of a single log record.

### Azure NSG Flow Logs Example

Azure NSG flow logs are written to Blob Storage under a path structure that includes the resource ID as a variable prefix and uses single-letter time segments:

```
flowLogResourceID=/<SUB>_<RG>/<NSG>/y=YYYY/m=MM/d=DD/h=HH/m=MM/macAddress=.../PT1H.json
```

Each `PT1H.json` blob is a single JSON document of the form:

```json
{
  "records": [
    { "time": "2026-05-16T16:00:00Z", "category": "NetworkSecurityGroupFlowEvent", "...": "..." },
    { "time": "2026-05-16T16:01:00Z", "category": "NetworkSecurityGroupFlowEvent", "...": "..." }
  ]
}
```

This configuration ingests flow logs across every NSG in the container with a single receiver instance:

```yaml
azureblobpolling:
  connection_string: "..."
  container: "insights-logs-networksecuritygroupflowevent"
  poll_interval: 5m
  root_folder: "flowLogResourceID=/*/*"      # one entry per NSG resource
  time_pattern: "y={year}/m={month}/d={day}/h={hour}/m={minute}"
  telemetry_type: "logs"
  blob_format: "records-json"                 # unwrap the {"records":[...]} envelope
  filename_pattern: "PT1H\\.json$"            # only ingest the hourly flow-log file
  enable_incremental_read: true               # ingest mid-hour appends, not just the first read
  storage: "file_storage"
```

How the pieces fit together:

- `enable_incremental_read: true` tracks a per-blob byte offset so each poll ingests the records appended to the still-growing `PT1H.json` since the last poll. Without it, the blob is read once when first seen and mid-hour appends are dropped (see [Incremental reading](#incremental-reading-of-append-growable-blobs)).
- `root_folder: "flowLogResourceID=/*/*"` lists one directory per NSG (subscription/RG segment, then NSG segment). New NSGs added to Azure are picked up automatically on the next poll.
- `time_pattern` extracts the timestamp from the `y=/m=/d=/h=/m=` segments. The matched `root_folder` is stripped before matching, so the same pattern works regardless of which NSG produced the blob.
- `blob_format: "records-json"` unwraps the `{"records":[...]}` envelope so each flow record becomes a separate log. (Use `json` only if the blobs are already NDJSON; use the default `otlp` only for blobs written by the Azure Blob Exporter.)
- `filename_pattern` keeps the receiver from picking up any non-`PT1H.json` files Azure may write alongside the records.

## Example Configuration

### Basic Configuration

This configuration polls every 10 minutes for new blobs in the container `my-container`. On first startup, it will look back 10 minutes (the default `initial_lookback`).

```yaml
azureblobpolling:
  connection_string: "DefaultEndpointsProtocol=https;AccountName=storage_account_name;AccountKey=storage_account_key;EndpointSuffix=core.windows.net"
  container: "my-container"
  poll_interval: 10m
  batch_size: 100
  page_size: 1000
```

### Custom Initial Lookback Configuration

This configuration polls every 5 minutes but looks back 1 hour on the first poll. This is useful when starting the receiver for the first time and you want to collect more historical data.

```yaml
azureblobpolling:
  connection_string: "DefaultEndpointsProtocol=https;AccountName=storage_account_name;AccountKey=storage_account_key;EndpointSuffix=core.windows.net"
  container: "my-container"
  poll_interval: 5m
  initial_lookback: 1h
  batch_size: 100
  page_size: 1000
```

### Using Storage Extension Configuration

This configuration shows using a storage extension to persist checkpoint data across agent restarts. The `storage` field is set to the component ID of the storage extension. This is **highly recommended** for production use to prevent data loss or duplication.

```yaml
extensions:
  file_storage:
    directory: $OIQ_OTEL_COLLECTOR_HOME/storage
receivers:
  azureblobpolling:
    connection_string: "DefaultEndpointsProtocol=https;AccountName=storage_account_name;AccountKey=storage_account_key;EndpointSuffix=core.windows.net"
    container: "my-container"
    poll_interval: 15m
    storage: "file_storage"
    batch_size: 100
    page_size: 1000
```

### Root Folder Configuration

This configuration specifies an additional field `root_folder` to match the `root_folder` value of the Azure Blob Exporter. The `root_folder` value in the exporter will prefix the blob path with the root folder and it needs to be accounted for in the polling receiver.

Such a path could look like the following:

```
root/year=2023/month=10/day=01/hour=13/minute=30/metrics_12345.json
root/year=2023/month=10/day=01/hour=13/minute=30/logs_12345.json
root/year=2023/month=10/day=01/hour=13/minute=30/traces_12345.json
```

```yaml
azureblobpolling:
  connection_string: "DefaultEndpointsProtocol=https;AccountName=storage_account_name;AccountKey=storage_account_key;EndpointSuffix=core.windows.net"
  container: "my-container"
  poll_interval: 10m
  root_folder: "root"
  batch_size: 100
  page_size: 1000
```

### Glob Root Folders

`root_folder` supports glob patterns to match multiple directories in one receiver. Supported metacharacters are `*` (any sequence within a single path segment), `?` (any single character), and `[...]` (character class). They are expanded by listing directory prefixes from Azure at startup of each poll and re-evaluated on every poll, so newly created directories are picked up automatically.

Use cases:

- One Azure container holds data for many tenants/resources under sibling directories (e.g. Azure NSG flow logs, where each NSG has its own resource-ID subtree).
- You want a single receiver instance instead of one per directory.

Example container layout:

```
flowLogResourceID=/SUB_A_RG/NSG_A/y=2026/m=05/d=16/h=16/m=00/macAddress=AA/PT1H.json
flowLogResourceID=/SUB_B_RG/NSG_B/y=2026/m=05/d=16/h=16/m=00/macAddress=BB/PT1H.json
```

```yaml
azureblobpolling:
  connection_string: "..."
  container: "flow-logs"
  poll_interval: 5m
  root_folder: "flowLogResourceID=/*/*"
```

Notes:

- The static portion of the pattern (everything before the first metacharacter) is used as the Azure listing prefix, so the directory listing scales with the number of matching subdirectories, not the entire container.
- If a glob matches zero directories, the poll completes without listing any blobs and logs a warning.
- When `time_pattern` is also set, the matched root prefix is automatically stripped from each blob name before the pattern is applied. This lets one `time_pattern` work across all matched directories regardless of the variable prefix in front of the time segment.
- If the `ListPrefixes` call used to expand the glob fails (Azure transient error, throttling, etc.), the poll lists **nothing** for that cycle and logs an error. Set `fallback_on_glob_failure: true` to instead fall back to scanning under the static portion of the glob. The fallback is opt-in because on broad globs (e.g. `flowLogResourceID=/*/*`) the static prefix can list the entire container, which is rarely what you want as a silent failure mode.

### Delete on Read Configuration

This configuration enables `delete_on_read`, which deletes a blob from Azure after it is processed and sent to the next component. **Use with caution** as this permanently deletes data from Azure Blob Storage.

The delete is deferred until the blob seals (stops growing) **only when `enable_incremental_read` is set**; the append-growable formats then keep a blob still being appended to until it seals, so live appends are not lost. Without the flag a blob is deleted right after its first whole read, so an hourly `PT1H.json` deleted at minute 1 loses the remaining appends — pair `delete_on_read` with `enable_incremental_read` for the append-growable formats.

```yaml
azureblobpolling:
  connection_string: "DefaultEndpointsProtocol=https;AccountName=storage_account_name;AccountKey=storage_account_key;EndpointSuffix=core.windows.net"
  container: "my-container"
  poll_interval: 10m
  blob_format: "records-json"
  delete_on_read: true
  enable_incremental_read: true   # defer the delete until the blob seals
  batch_size: 100
  page_size: 1000
```

### Complete Production Configuration

This example shows a complete production-ready configuration with storage extension for persistence and appropriate polling settings.

```yaml
extensions:
  file_storage:
    directory: $OIQ_OTEL_COLLECTOR_HOME/storage

receivers:
  azureblobpolling/metrics:
    connection_string: "DefaultEndpointsProtocol=https;AccountName=storage_account_name;AccountKey=storage_account_key;EndpointSuffix=core.windows.net"
    container: "otel-metrics"
    poll_interval: 5m
    initial_lookback: 30m
    storage: "file_storage"
    batch_size: 50
    page_size: 1000

  azureblobpolling/logs:
    connection_string: "DefaultEndpointsProtocol=https;AccountName=storage_account_name;AccountKey=storage_account_key;EndpointSuffix=core.windows.net"
    container: "otel-logs"
    poll_interval: 2m
    initial_lookback: 15m
    storage: "file_storage"
    batch_size: 100
    page_size: 1000

service:
  extensions: [file_storage]
  pipelines:
    metrics:
      receivers: [azureblobpolling/metrics]
      exporters: [otlp]
    logs:
      receivers: [azureblobpolling/logs]
      exporters: [otlp]
```

## Behavior on Restart

When the collector restarts:

1. **With Storage Extension**: The receiver loads the checkpoint and resumes polling from the `LastPollTime`. This ensures no data is missed or duplicated.
2. **Without Storage Extension**: The receiver starts fresh with no checkpoint, using `initial_lookback` to determine the starting point. This may result in duplicate processing of recent data.

For production deployments, **always use a storage extension** to maintain state across restarts.

## Performance Tuning

- **poll_interval**: Adjust based on your data ingestion rate. Shorter intervals provide lower latency but increase API calls.
- **batch_size**: Controls concurrent blob downloads. Higher values improve throughput but increase memory usage.
- **page_size**: Number of blobs retrieved per API call. Higher values reduce API calls but may increase latency.

Recommended starting values:

- High-frequency data (< 5 min between blobs): `poll_interval: 2m`, `batch_size: 100`
- Medium-frequency data (5-15 min): `poll_interval: 10m`, `batch_size: 50`
- Low-frequency data (> 15 min): `poll_interval: 30m`, `batch_size: 30`

## Flexible Time Patterns

The receiver supports three modes for extracting timestamps from blob paths:

### Mode 1: Structured Path (Default)

Uses the default folder structure with explicit labels. This is the recommended mode when using the Azure Blob Exporter.

**Expected blob path:**

```
year=2025/month=12/day=05/hour=14/minute=30/logs_12345.json
```

The filename must contain:

- `logs_` for logs pipeline
- `metrics_` for metrics pipeline
- `traces_` for traces pipeline

### Mode 2: LastModified Timestamp

Uses the blob's LastModified property instead of parsing the path. Useful for blobs from external sources that don't follow a specific naming structure.

```yaml
azureblobpolling:
  connection_string: "..."
  container: "unstructured-logs"
  poll_interval: 2m
  use_last_modified: true
  telemetry_type: "logs" # Required when using use_last_modified
```

**Works with any blob path:**

```
application.log
data/2025/file.json
logs/app-server.log
```

### Mode 3: Custom Time Pattern

Extracts timestamps from custom path structures using patterns.

#### Named Placeholders

Use named placeholders for easy-to-read patterns:

```yaml
azureblobpolling:
  connection_string: "..."
  container: "logs"
  poll_interval: 1m
  time_pattern: "{year}/{month}/{day}/{hour}/{minute}"
  telemetry_type: "logs"
```

**Matches paths like:** `2025/12/05/14/30/application.json`

**Available placeholders:**

- `{year}` - 4 digits (2025)
- `{month}` - 2 digits (01-12)
- `{day}` - 2 digits (01-31)
- `{hour}` - 2 digits (00-23)
- `{minute}` - 2 digits (00-59)
- `{second}` - 2 digits (00-59)

**More examples:**

```yaml
# With prefix and custom separators
time_pattern: "logs/{year}-{month}-{day}/{hour}"
# Matches: logs/2025-12-05/14/app.json

# No minutes
time_pattern: "{year}/{month}/{day}/{hour}"
# Matches: 2025/12/05/14/data.json
```

#### Go Time Format

Use Go's time format for more control:

```yaml
azureblobpolling:
  connection_string: "..."
  container: "metrics"
  poll_interval: 5m
  time_pattern: "2006/01/02/15/04"
  telemetry_type: "metrics"
```

**Go time format reference:**

- `2006` - year
- `01` - month
- `02` - day
- `15` - hour (24h format)
- `04` - minute
- `05` - second

**Example:** Pattern `"2006-01-02/15"` matches `2025-12-05/14/metrics.json`

## Filename Filtering

Use `filename_pattern` to filter blobs by their filename using regex. This is useful when multiple types of files are in the same container.

### Example: Filter Firewall Logs

```yaml
azureblobpolling:
  connection_string: "..."
  container: "security-logs"
  poll_interval: 1m
  time_pattern: "{year}/{month}/{day}/{hour}"
  telemetry_type: "logs"
  filename_pattern: "firewall\\d+_\\w+\\.json"
```

**Matches:** `firewall43_dfreds.json`, `firewall1_data.json`  
**Skips:** `application.json`, `system.log`

### Example: All JSON Files

```yaml
azureblobpolling:
  connection_string: "..."
  container: "data"
  poll_interval: 5m
  use_last_modified: true
  telemetry_type: "logs"
  filename_pattern: ".*\\.json"
```

**Matches:** Any file ending with `.json`  
**Skips:** `.log`, `.txt`, `.gz` files

### Example: Application Logs Only

```yaml
azureblobpolling:
  connection_string: "..."
  container: "logs"
  poll_interval: 2m
  time_pattern: "2006/01/02/15"
  telemetry_type: "logs"
  filename_pattern: "app-.*\\.(log|json)"
```

**Matches:** `app-server.log`, `app-client.json`, `app-api.log`  
**Skips:** `system.log`, `data.txt`

### Regex Tips

- Remember to escape special characters in YAML: `\d` becomes `\\d`
- `.` (dot) matches any character, use `\\.` to match a literal dot
- `*` means "zero or more of the previous", use `.*` to match any characters
- Test your regex at https://regex101.com/ before adding to config

## Advanced Configuration Examples

### Example: Mixed Data Sources with Filtering

```yaml
extensions:
  file_storage:
    directory: /var/lib/otelcol/storage

receivers:
  # Structured data from Azure Blob Exporter
  azureblobpolling/structured:
    connection_string: "..."
    container: "otel-data"
    poll_interval: 5m
    storage: "file_storage"

  # Unstructured application logs
  azureblobpolling/app-logs:
    connection_string: "..."
    container: "raw-logs"
    poll_interval: 2m
    root_folder: "production"
    time_pattern: "{year}/{month}/{day}/{hour}"
    telemetry_type: "logs"
    filename_pattern: "app-.*\\.json"
    storage: "file_storage"

  # External data using LastModified
  azureblobpolling/external:
    connection_string: "..."
    container: "third-party-logs"
    poll_interval: 10m
    use_last_modified: true
    telemetry_type: "logs"
    filename_pattern: ".*\\.(json|log)"
    storage: "file_storage"

service:
  extensions: [file_storage]
  pipelines:
    logs:
      receivers:
        - azureblobpolling/structured
        - azureblobpolling/app-logs
        - azureblobpolling/external
      exporters: [otlp]
```

## Additional Configuration Fields

| Field                      | Type   | Default | Required | Description                                                                                                                                                                                                                                       |
| -------------------------- | ------ | ------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| use_last_modified          | bool   | `false` | `false`  | When `true`, uses the blob's LastModified timestamp instead of parsing the folder structure. Can be combined with `time_pattern` when `use_time_pattern_as_prefix` is enabled.                                                                    |
| time_pattern               | string |         | `false`  | Custom pattern for extracting timestamps from blob paths. Supports named placeholders and Go time format. When `root_folder` (or a glob expansion of it) is a prefix of the blob name, that prefix is stripped and the pattern is matched anywhere in the remainder; otherwise the pattern is anchored to the start of the blob name. Can be combined with `use_last_modified` when `use_time_pattern_as_prefix` is enabled.                                |
| fallback_on_glob_failure   | bool   | `false` | `false`  | When `true` and `root_folder` is a glob, a failure of the Azure `ListPrefixes` call used to expand the glob falls back to scanning under the static portion of the pattern. When `false` (default), the poll lists nothing for that cycle and logs an error. Leave this off on broad globs to avoid silent full-container scans.                                                                                                                            |
| use_time_pattern_as_prefix | bool   | `false` | `false`  | When `true`, uses the `time_pattern` to generate efficient prefixes for Azure API calls, significantly reducing the number of blobs scanned. Requires `time_pattern` to be set. Can be combined with `use_last_modified` for optimal performance. |
| telemetry_type             | string |         | `false`  | Explicitly sets the telemetry type (`logs`, `metrics`, or `traces`). Required when using `time_pattern` or `use_last_modified`. Falls back to pipeline type if not set.                                                                           |
| filename_pattern           | string |         | `false`  | Regex pattern to filter blobs by filename. Only matching blobs are processed.                                                                                                                                                                     |

## Prefix Optimization for Large Containers

When dealing with containers that have a large number of blobs (100K+), the `use_time_pattern_as_prefix` option provides significant performance improvements by generating time-based prefixes for Azure API calls instead of listing all blobs.

### How It Works

Without prefix optimization, the receiver must list ALL blobs in the container and check each one's timestamp. With prefix optimization enabled, the receiver:

1. Parses the `time_pattern` to identify time components (year, month, day, hour)
2. Generates prefixes for each time bucket in the lookback window
3. Only lists blobs under those specific prefixes

For example, with `time_pattern: "{year}/{month}/{day}"` and a 5-minute lookback at 3:00 PM on January 27, 2025, the receiver generates the prefix `2025/01/27/` and only lists blobs under that path.

### Hybrid Mode: Prefix + LastModified

For blob structures that only include date (not time) in the path, combine `use_time_pattern_as_prefix` with `use_last_modified` for optimal performance:

- **Prefix optimization**: Efficiently lists only blobs from relevant date folders
- **LastModified filtering**: Precisely filters to blobs within the actual time window

```yaml
azureblobpolling:
  connection_string: "..."
  container: "logs"
  root_folder: "myapp/logs"
  poll_interval: 1m
  initial_lookback: 5m
  time_pattern: "{year}/{month}/{day}"
  use_time_pattern_as_prefix: true
  use_last_modified: true
  telemetry_type: "logs"
  storage: "file_storage"
```

**Why hybrid mode?** When the path only contains `{year}/{month}/{day}`, parsed timestamps resolve to midnight (00:00:00 UTC). A 5-minute lookback window at 3:00 PM would miss all blobs because midnight is outside the window. Using `use_last_modified: true` ensures precise time filtering based on the blob's actual modification time.

### Per-Category Receivers

For containers with multiple log categories (e.g., `logs/dns/`, `logs/firewall/`, `logs/app/`), create separate receivers for each category. This provides:

- More efficient prefix filtering per category
- Separate pipelines for different log types
- Better resource isolation and monitoring

```yaml
receivers:
  azureblobpolling/dns:
    connection_string: "..."
    container: "logs"
    root_folder: "logs/dns"
    poll_interval: 1m
    time_pattern: "{year}/{month}/{day}"
    use_time_pattern_as_prefix: true
    use_last_modified: true
    telemetry_type: "logs"
    storage: "file_storage"

  azureblobpolling/firewall:
    connection_string: "..."
    container: "logs"
    root_folder: "logs/firewall"
    poll_interval: 1m
    time_pattern: "{year}/{month}/{day}"
    use_time_pattern_as_prefix: true
    use_last_modified: true
    telemetry_type: "logs"
    storage: "file_storage"
```

## Performance Benchmarks and Recommendations

The following benchmarks were collected using Azure Blob Storage with containers containing 1M+ blobs.

### Test Environment

- Container: 1,000,000+ blobs across multiple categories
- Blob size: ~1 KB each (gzipped JSON logs, 10 events per blob)
- Poll interval: 1 minute
- Lookback: 5 minutes
- Configuration: batch_size=200, page_size=5000

### Benchmark Results

| Configuration                 | Poll Duration               | Notes                                                             |
| ----------------------------- | --------------------------- | ----------------------------------------------------------------- |
| `use_last_modified` only      | **7+ minutes** (incomplete) | Lists all 1M+ blobs every poll                                    |
| `time_pattern` only           | ~25 seconds                 | Fast listing, but timestamp parsing issues with day-only patterns |
| **Hybrid mode** (recommended) | **45-52 seconds**           | Prefix-optimized listing + precise LastModified filtering         |

### Processing Throughput

With the hybrid configuration processing new blobs:

- **Blob processing rate**: ~200 blobs/second
- **Event throughput**: ~2,000 events/second (at 10 events/blob)
- **Memory usage**: < 200 MB
- **CPU usage**: < 5% (I/O bound, not CPU bound)

### Configuration Recommendations

#### Small Containers (< 10,000 blobs)

Any configuration works well. Use the simplest option for your blob structure:

```yaml
azureblobpolling:
  connection_string: "..."
  container: "small-logs"
  poll_interval: 2m
  use_last_modified: true
  telemetry_type: "logs"
```

#### Medium Containers (10,000 - 100,000 blobs)

Consider using `time_pattern` for better performance:

```yaml
azureblobpolling:
  connection_string: "..."
  container: "medium-logs"
  poll_interval: 2m
  time_pattern: "{year}/{month}/{day}/{hour}"
  telemetry_type: "logs"
  batch_size: 100
```

#### Large Containers (100,000+ blobs)

**Required**: Use `use_time_pattern_as_prefix` to avoid listing all blobs:

```yaml
azureblobpolling:
  connection_string: "..."
  container: "large-logs"
  root_folder: "category/subcategory"
  poll_interval: 1m
  time_pattern: "{year}/{month}/{day}"
  use_time_pattern_as_prefix: true
  use_last_modified: true # Required for day-only patterns
  telemetry_type: "logs"
  batch_size: 200
  page_size: 5000
  storage: "file_storage"
```

#### Very Large Containers (1M+ blobs)

For containers with millions of blobs:

1. **Split by category**: Create separate receivers per log type/category
2. **Use hourly paths if possible**: `{year}/{month}/{day}/{hour}` provides 24x better filtering than daily paths
3. **Increase batch_size**: Use 200+ for better throughput
4. **Use storage extension**: Critical for checkpoint persistence

```yaml
# Example for 1M+ blob container with daily folders
azureblobpolling:
  connection_string: "..."
  container: "enterprise-logs"
  root_folder: "production/application"
  poll_interval: 1m
  initial_lookback: 24h # Cover full day for day-only patterns
  time_pattern: "{year}/{month}/{day}"
  use_time_pattern_as_prefix: true
  use_last_modified: true
  telemetry_type: "logs"
  batch_size: 200
  page_size: 5000
  storage: "file_storage"
```

### Blob Path Structure Recommendations

For optimal performance with large containers, structure blob paths with time components that support efficient prefix filtering:

| Structure                               | Prefix Efficiency | Recommendation                                           |
| --------------------------------------- | ----------------- | -------------------------------------------------------- |
| `{year}/{month}/{day}/{hour}/{minute}/` | Excellent         | Best for high-volume, low-latency                        |
| `{year}/{month}/{day}/{hour}/`          | Very Good         | Good balance of efficiency and simplicity                |
| `{year}/{month}/{day}/`                 | Good              | Use with hybrid mode (+ `use_last_modified`)             |
| `{category}/{year}/{month}/{day}/`      | Good              | Split receivers by category                              |
| Flat structure (no time in path)        | Poor              | Avoid for large containers; use `use_last_modified` only |

### Troubleshooting Performance Issues

**Poll times exceeding poll_interval:**

- Enable `use_time_pattern_as_prefix` if not already enabled
- Split into multiple receivers by category
- Increase `batch_size` for better throughput
- Consider adding hour to blob paths

**Missing blobs with day-only patterns:**

- Add `use_last_modified: true` to enable hybrid mode
- Verify `initial_lookback` covers at least 24 hours

**High memory usage:**

- Reduce `batch_size` to limit concurrent downloads
- Enable memory_limiter processor in the pipeline
