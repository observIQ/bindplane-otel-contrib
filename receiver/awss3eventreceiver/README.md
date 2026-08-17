# AWS S3 Event Receiver

The AWS S3 Event Receiver consumes S3 event notifications for object creation events (`s3:ObjectCreated:*`) and emits the S3 object as the string body of a log record.

## How It Works

1. The receiver polls an SQS queue for S3 event notifications.
2. Supports both direct S3 events and S3 events wrapped in SNS notifications (S3 → SNS → SQS).
3. When an object creation event (`s3:ObjectCreated:*`) is received, the receiver downloads the S3 object.
4. The receiver reads the object into the body of a new log record.
5. Non-object creation events are ignored but removed from the queue.
6. If an S3 object is not found (404 error), the corresponding SQS message is preserved for retry later.

## File Format Support

The receiver detects the file format from the object's content, not from its name or content type. A correctly formatted object is parsed even when its extension or `Content-Type` is wrong, missing, or generic (S3 commonly reports `application/octet-stream`).

| Format | Detection |
|---|---|
| Avro OCF | Leading `Obj\x01` magic bytes |
| JSON | Leading `{` followed by `"`/`}`, or `[` followed by `{`/`]`. Covers arrays, `Records` wrappers, and value sequences including NDJSON |
| Plain text | Everything else; parsed line by line |

JSON covers three layouts, all read as a stream: a top-level array, an object whose `Records` key holds that array, and a sequence of top-level values one after another. That last shape is what makes newline-delimited JSON work, and it needs no format of its own: to a JSON decoder, NDJSON, a lone object, and concatenated pretty-printed documents are the same thing.

A document too large to classify within the first 4 KiB is read line by line instead, so a single very large JSON object is never buffered whole.

### Compression

Compression is detected from content, not from the `.gz` extension or a `Content-Encoding` label. Compressed objects are transparently decompressed before parsing, and the decompressed bytes are then classified as Avro, JSON, or text. When a compression label disagrees with the detected content, a warning is logged and the content wins. This fixes objects that carry a `.gz` name but hold uncompressed bytes, which previously failed to parse and were redelivered indefinitely.

| Codec | Detection |
|---|---|
| gzip | Content magic (`1f 8b`) |
| bzip2 | Content magic |
| xz | Content magic |
| zstd | Content magic |
| zlib | Content magic |
| lzip | Content magic (`4c 5a 49 50`) |
| lz4 (frame) | Content magic (`04 22 4d 18`) |
| snappy (frame) | Content magic (`ff 06 00 00 sNaPpY`) |
| raw DEFLATE | `Content-Encoding: deflate` (headerless, not detectable from content) |
| lzma (alone) | `.lzma` name, `Content-Encoding: lzma`, or a `lzma` content type (no reliable magic) |

The headerless formats (raw DEFLATE, lzma) are attempted only when a label names them, and the decode is best-effort.

### Archives

Archive objects are detected from content and expanded transparently: each entry is parsed independently (as Avro, JSON, or text) and its records are emitted as if the entries were concatenated. Entries may be heterogeneous (a tar can hold JSON, Avro, and plain-text members together).

| Archive | Detection |
|---|---|
| tar | Content magic (`ustar`) |
| zip | Content magic (`PK`) |
| 7z | Content magic (`7z\xbc\xaf\x27\x1c`) |
| rar | Content magic (`Rar!\x1a\x07`, RAR4 and RAR5) |

Because compression is detected and stripped before archive detection runs, compressed tarballs work with no extra configuration: a `.tar.gz`, `.tar.zst`, `.tar.xz`, or `.tar.bz2` object is decompressed, re-detected as a tar, and expanded. This is content-driven and does not depend on the object's name.

tar and rar are read as a stream and never fully buffered. zip and 7z require random access, so those objects are materialized to a temporary file (in the OS temp directory) that is removed once the archive is fully read or if any error occurs.

Multi-volume RAR sets (an archive split across several volume files) are not supported: one S3 object is treated as one complete archive. A single part of a multi-volume set will fail to parse and be routed to the DLQ.

Archive handling notes:

- **Directory entries** are skipped.
- **Unsupported entries** (an image, a PDF, an unknown binary inside the archive) are skipped individually with a logged warning; the rest of the archive is still parsed.
- **Archive-bomb protection**: total uncompressed bytes, per-entry uncompressed bytes, and entry count are capped. Declared entry sizes are never trusted; the limits are enforced against the bytes that actually flow. An archive that exceeds a limit is aborted and nacked for DLQ processing rather than expanded unboundedly.
- **Resumption**: offsets for archive objects track both the entry index and the position within that entry, so an interrupted read resumes at the exact entry and position it left off. Non-archive objects continue to use a single byte offset, and offsets stored by earlier receiver versions remain valid.

### Unsupported content

Content that is not text, Avro, or JSON (for example an image or a PDF) is not parsed as text. It is rejected with its detected MIME type and the message is nacked for DLQ processing, rather than being emitted as garbled lines.

## Visibility Extension Behavior

The receiver implements a sophisticated visibility extension strategy to handle long-running processing:

1. **Initial Visibility**: When a message is received, it becomes invisible for the duration specified by `visibility_timeout` (default: 5 minutes).

2. **Regular Extensions**: The receiver extends the visibility window by `visibility_extension_interval` (default: 1 minute) before the current window expires.

3. **Maximum Window**: Extensions stop when the total visibility time reaches `max_visibility_window` (default: 1 hour). SQS has a max window of 12 hours, and this allows the receiver to set a shorter maximum window.

4. **Safety Margins**: The receiver always extends calls to extend the visibility window 80% of the way through the current window.  This helps prevent race conditions where the message may become visible before the window has been extended.

This approach ensures that:

- Messages remain invisible during processing
- Long-running operations don't cause message expiration
- Messages eventually become visible if processing takes too long
- The system respects SQS's 12-hour visibility limit

## Configuration

| Field                            | Type     | Default    | Required | Description |
|----------------------------------|----------|------------|----------|-------------|
| sqs_queue_url                    | string   |            | `true`   | The URL of the SQS queue to poll for S3 event notifications (the AWS region is automatically extracted from this URL) |
| standard_poll_interval           | duration | 15s        | `false`  | The interval at which the SQS queue is polled for messages |
| max_poll_interval                | duration | 120s       | `false`  | The maximum interval at which the SQS queue is polled for messages |
| polling_backoff_factor           | float    | 2          | `false`  | The factor by which the polling interval is multiplied after an unsuccessful poll |
| workers                          | int      | 5          | `false`  | The number of workers to process messages in parallel |
| visibility_timeout               | duration | 5m         | `false`  | The visibility timeout for SQS messages |
| visibility_extension_interval    | duration | 1m         | `false`  | How often to extend message visibility during processing. Should be less than visibility_timeout.  Minimum is 10s. |
| max_visibility_window            | duration | 1h         | `false`  | Maximum total time a message can remain invisible before becoming visible to other consumers. Must be less than SQS's 12-hour limit |
| max_log_size                     | int      | 1048576    | `false`  | The maximum size of a log record in bytes. Logs exceeding this size will be split |
| max_logs_emitted                 | int      | 1000       | `false`  | The maximum number of log records to emit in a single batch. A higher number will result in fewer batches, but more memory |
| raw                              | bool     | `false`    | `false`  | Emit each record's original text as the body instead of a parsed structure. Content detection still runs, so unsupported binary content is routed to the dead-letter queue. Avro OCF holds no original text, so it emits the JSON encoding of each record. |
| include_log_record_original      | bool     | `false`    | `false`  | Additionally record each log record's original text on the `log.record.original` attribute, leaving the body as-is. |
| notification_type                | enum     | s3         | `false`  | The Notification Type that the receiver expects.  Valid values are `s3` or `sns` |

## AWS Setup

### Direct S3 Events Setup

To use this receiver with direct S3 events (S3 → SQS), you need to:

1. Configure S3 bucket event notifications to send directly to an SQS queue.
2. Ensure the collector has permission to read and delete messages from the SQS queue.
3. Ensure the collector has permission to read objects from the S3 bucket.

### SNS Integration Setup (S3 → SNS → SQS)

To use this receiver with SNS integration, you need to:

1. Configure S3 bucket event notifications to send to an SNS topic.
2. Subscribe an SQS queue to the SNS topic.
3. Ensure the collector has permission to read and delete messages from the SQS queue.
4. Ensure the collector has permission to read objects from the S3 bucket.

### Required IAM Permissions

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "sqs:ReceiveMessage",
                "sqs:DeleteMessage"
            ],
            "Resource": "arn:aws:sqs:REGION:ACCOUNT:QUEUE-NAME"
        },
        {
            "Effect": "Allow",
            "Action": [
                "s3:GetObject"
            ],
            "Resource": "arn:aws:s3:::BUCKET-NAME/*"
        }
    ]
}
```

## Example Configurations

### Direct S3 Events (Default)

```yaml
receivers:
  s3event:
    sqs_queue_url: https://sqs.us-west-2.amazonaws.com/123456789012/my-queue
    notification_type: s3  # Default, can be omitted

exporters:
  otlp:
    endpoint: otelcol:4317

service:
  pipelines:
    logs:
      receivers: [s3event]
      exporters: [otlp]
```### S3 Events via SNS (S3 → SNS → SQS)

```yaml
receivers:
  s3event:
    sqs_queue_url: https://sqs.us-west-2.amazonaws.com/123456789012/my-queue
    notification_type: sns

exporters:
  otlp:
    endpoint: otelcol:4317

service:
  pipelines:
    logs:
      receivers: [s3event]
      exporters: [otlp]
```
