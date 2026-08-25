// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package worker provides a worker that processes GCS event notifications from Pub/Sub.
package worker // import "github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/worker"

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	subscriber "cloud.google.com/go/pubsub/apiv1"
	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"cloud.google.com/go/storage"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"
	"google.golang.org/api/googleapi"

	"github.com/observiq/bindplane-otel-contrib/internal/blobstream"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/metadata"
)

// GCS Pub/Sub notification event types
const (
	EventTypeObjectFinalize = "OBJECT_FINALIZE"
)

// GCS Pub/Sub message attribute keys
const (
	AttrBucketID         = "bucketId"
	AttrObjectID         = "objectId"
	AttrEventType        = "eventType"
	AttrObjectGeneration = "objectGeneration"
)

// PullMessage wraps the data from a pubsubpb.ReceivedMessage for worker processing.
type PullMessage struct {
	AckID      string
	MessageID  string
	Data       []byte
	Attributes map[string]string
}

// Worker processes GCS event notifications from Pub/Sub messages.
type Worker struct {
	logger        *zap.Logger
	tel           component.TelemetrySettings
	storageClient *storage.Client
	nextConsumer  consumer.Logs
	offsetStorage storageclient.StorageClient

	// newRecordProducer builds the record producer for an object's decompressed stream.
	// It defaults to blobstream.NewRecordProducer; tests override it to drive the
	// per-record error-handling branches without crafting exotic object content.
	newRecordProducer func(context.Context, blobstream.LogStream, blobstream.BufferedReader, blobstream.ParseErrorFunc) (blobstream.RecordProducer, error)
	maxLogSize        int
	maxLogsEmitted    int
	metrics           *metadata.TelemetryBuilder
	bucketNameFilter  *regexp.Regexp
	objectKeyFilter   *regexp.Regexp
	obsrecv           *receiverhelper.ObsReport
	subClient         *subscriber.SubscriberClient
	maxExtension      time.Duration
	errorBackOff      configretry.BackOffConfig
}

// Option is a functional option for configuring the Worker
type Option func(*Worker)

// WithBucketNameFilter sets the bucket name filter regex
func WithBucketNameFilter(filter *regexp.Regexp) Option {
	return func(w *Worker) {
		if filter != nil {
			w.bucketNameFilter = filter
		}
	}
}

// WithObjectKeyFilter sets the object key filter regex
func WithObjectKeyFilter(filter *regexp.Regexp) Option {
	return func(w *Worker) {
		if filter != nil {
			w.objectKeyFilter = filter
		}
	}
}

// WithTelemetryBuilder sets the telemetry builder
func WithTelemetryBuilder(tb *metadata.TelemetryBuilder) Option {
	return func(w *Worker) {
		if tb != nil {
			w.metrics = tb
		}
	}
}

// WithErrorBackOff sets the retry backoff applied when a downstream consume fails.
func WithErrorBackOff(cfg configretry.BackOffConfig) Option {
	return func(w *Worker) {
		w.errorBackOff = cfg
	}
}

// WithSubscriberClient sets the low-level Pub/Sub subscriber client used for
// explicit Acknowledge and ModifyAckDeadline RPCs.
func WithSubscriberClient(c *subscriber.SubscriberClient) Option {
	return func(w *Worker) {
		w.subClient = c
	}
}

// WithMaxExtension sets the maximum total duration for ack deadline extension.
func WithMaxExtension(d time.Duration) Option {
	return func(w *Worker) {
		w.maxExtension = d
	}
}

// New creates a new Worker
func New(tel component.TelemetrySettings, nextConsumer consumer.Logs, storageClient *storage.Client, obsrecv *receiverhelper.ObsReport, maxLogSize int, maxLogsEmitted int, opts ...Option) *Worker {
	w := &Worker{
		logger:         tel.Logger.With(zap.String("component", "gcspubsubeventreceiver")),
		tel:            tel,
		storageClient:  storageClient,
		nextConsumer:   nextConsumer,
		offsetStorage:  storageclient.NewNopStorage(),
		obsrecv:        obsrecv,
		maxLogSize:     maxLogSize,
		maxLogsEmitted: maxLogsEmitted,
		maxExtension:   1 * time.Hour, // default; overridden by WithMaxExtension
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// SetOffsetStorage sets the offset storage client
func (w *Worker) SetOffsetStorage(offsetStorage storageclient.StorageClient) {
	w.offsetStorage = offsetStorage
}

// ProcessMessage processes a Pub/Sub message containing a GCS event notification.
// It returns true if the GCS object was successfully processed (and acked), which
// signals the caller to mark the object key in the recent-dedup tracker.
// Returns false for filtered messages (still acked) and error cases (nacked).
func (w *Worker) ProcessMessage(ctx context.Context, msg *PullMessage, subscriptionPath string, deferThis func()) bool {
	defer deferThis()

	logger := w.logger.With(zap.String("message_id", msg.MessageID))

	// Start ack deadline extension immediately so long-running processing
	// does not cause the message to become eligible for redelivery.
	extCtx, extCancel := context.WithCancel(ctx)
	defer extCancel()
	go w.extendAckDeadline(extCtx, msg.AckID, subscriptionPath, logger)

	// Parse event attributes from the Pub/Sub message
	eventType := msg.Attributes[AttrEventType]
	bucketID := msg.Attributes[AttrBucketID]
	objectID := msg.Attributes[AttrObjectID]

	logger = logger.With(
		zap.String("event_type", eventType),
		zap.String("bucket", bucketID),
		zap.String("object", objectID),
	)

	// Filter for OBJECT_FINALIZE events only
	if eventType != EventTypeObjectFinalize {
		logger.Debug("skipping non-OBJECT_FINALIZE event")
		_ = w.ackMessage(ctx, msg.AckID, subscriptionPath)
		return false
	}

	// Validate required attributes
	if bucketID == "" || objectID == "" {
		logger.Warn("message missing required attributes (bucketId, objectId)")
		_ = w.ackMessage(ctx, msg.AckID, subscriptionPath)
		return false
	}

	// Apply bucket name filter
	if w.bucketNameFilter != nil && !w.bucketNameFilter.MatchString(bucketID) {
		logger.Debug("skipping message due to bucket name filter")
		_ = w.ackMessage(ctx, msg.AckID, subscriptionPath)
		return false
	}

	// Apply object key filter
	if w.objectKeyFilter != nil && !w.objectKeyFilter.MatchString(objectID) {
		logger.Debug("skipping message due to object key filter")
		_ = w.ackMessage(ctx, msg.AckID, subscriptionPath)
		return false
	}

	logger.Debug("processing GCS object")

	// Process the record, trying JSON first then falling back to line parsing
	err := w.processRecord(ctx, bucketID, objectID, logger)
	if err != nil {
		w.handleProcessingError(ctx, msg.AckID, subscriptionPath, err, logger)
		return false
	}

	w.metrics.GcseventObjectsHandled.Add(ctx, 1)

	// Ack first, then delete the offset only once the ack succeeds. If the ack fails the
	// message redelivers, so the offset must remain as the resume point; deleting it
	// first would reprocess the whole object from the start on that redelivery.
	if err := w.ackMessage(ctx, msg.AckID, subscriptionPath); err != nil {
		return false
	}
	logger.Debug("message acked")

	// Delete the offset on a detached context. A cancellation would otherwise leave a
	// stale offset for an acked object.
	deleteCtx, cancelDelete := blobstream.CleanupContext(ctx)
	offsetStorageKey := fmt.Sprintf("%s_%s", OffsetStorageKey, objectID)
	if err := w.offsetStorage.DeleteStorageData(deleteCtx, offsetStorageKey); err != nil {
		logger.Error("failed to delete offset", zap.Error(err), zap.String("offset_storage_key", offsetStorageKey))
	}
	cancelDelete()

	return true
}

// ackMessage acknowledges a message so Pub/Sub does not redeliver it. It returns the
// acknowledge error so the caller can keep the offset when the ack does not land.
func (w *Worker) ackMessage(ctx context.Context, ackID, subscriptionPath string) error {
	if w.subClient == nil {
		return nil
	}
	// Ack on a detached context. A cancellation must not leave a processed object in
	// the subscription.
	ackCtx, cancel := blobstream.CleanupContext(ctx)
	defer cancel()

	if err := w.subClient.Acknowledge(ackCtx, &pubsubpb.AcknowledgeRequest{
		Subscription: subscriptionPath,
		AckIds:       []string{ackID},
	}); err != nil {
		w.logger.Error("failed to ack message", zap.Error(err), zap.String("ack_id", ackID))
		return err
	}
	return nil
}

// nackMessage makes a message immediately available for redelivery by setting
// its ack deadline to 0 — analogous to SQS resetVisibilityTimeout.
func (w *Worker) nackMessage(ctx context.Context, ackID, subscriptionPath string) {
	if w.subClient == nil {
		return
	}
	// Nack on a detached context. The message then redelivers without waiting for the
	// full ack deadline.
	nackCtx, cancel := blobstream.CleanupContext(ctx)
	defer cancel()

	if err := w.subClient.ModifyAckDeadline(nackCtx, &pubsubpb.ModifyAckDeadlineRequest{
		Subscription:       subscriptionPath,
		AckIds:             []string{ackID},
		AckDeadlineSeconds: 0,
	}); err != nil {
		w.logger.Error("failed to nack message", zap.Error(err), zap.String("ack_id", ackID))
	}
}

// extendAckDeadline periodically extends the ack deadline for a message while
// it is being processed. Analogous to awss3eventreceiver's extendMessageVisibility.
func (w *Worker) extendAckDeadline(ctx context.Context, ackID, subscriptionPath string, logger *zap.Logger) {
	if w.subClient == nil {
		return
	}

	const extensionSecs int32 = 30
	// Extend at 50% of the extension period (safety margin).
	ticker := time.NewTicker(time.Duration(extensionSecs) * time.Second / 2)
	defer ticker.Stop()

	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Since(start) >= w.maxExtension {
				logger.Info("reached maximum ack deadline extension, stopping",
					zap.Duration("total_time", time.Since(start)))
				return
			}
			if err := w.subClient.ModifyAckDeadline(ctx, &pubsubpb.ModifyAckDeadlineRequest{
				Subscription:       subscriptionPath,
				AckIds:             []string{ackID},
				AckDeadlineSeconds: extensionSecs,
			}); err != nil {
				logger.Error("failed to extend ack deadline", zap.Error(err))
				return
			}
			logger.Debug("extended ack deadline", zap.Int32("deadline_secs", extensionSecs))
		}
	}
}

func (w *Worker) processRecord(ctx context.Context, bucket, object string, recordLogger *zap.Logger) error {
	err := w.consumeLogsFromGCSObject(ctx, bucket, object, true, recordLogger)
	if err != nil {
		if errors.Is(err, blobstream.ErrNotArrayOrKnownObject) {
			// try again without attempting to parse as JSON
			recordLogger.Debug("parsing as JSON failed, trying again with line parsing")
			return w.consumeLogsFromGCSObject(ctx, bucket, object, false, recordLogger)
		}
		return err
	}
	return nil
}

func (w *Worker) consumeLogsFromGCSObject(ctx context.Context, bucket, object string, tryJSON bool, recordLogger *zap.Logger) error {
	recordLogger.Debug("reading GCS object")

	obj := w.storageClient.Bucket(bucket).Object(object)
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	defer reader.Close()

	// version scopes the offset to this object; a replacement object has a different generation.
	version := strconv.FormatInt(reader.Attrs.Generation, 10)

	now := time.Now()

	// Get content type from object attributes
	var contentType *string
	if reader.Attrs.ContentType != "" {
		ct := reader.Attrs.ContentType
		contentType = &ct
	}
	var contentEncoding *string
	if reader.Attrs.ContentEncoding != "" {
		ce := reader.Attrs.ContentEncoding
		contentEncoding = &ce
	}

	stream := blobstream.LogStream{
		Name:            object,
		ContentEncoding: contentEncoding,
		ContentType:     contentType,
		Body:            reader,
		MaxLogSize:      w.maxLogSize,
		Logger:          recordLogger,
		TryDecoding:     tryJSON,
	}

	// Create the offset storage key for this object
	offsetStorageKey := fmt.Sprintf("%s_%s", OffsetStorageKey, object)

	// Load the offset from storage
	offset := blobstream.NewOffset(0)
	err = w.offsetStorage.LoadStorageData(ctx, offsetStorageKey, offset)
	if err != nil {
		return fmt.Errorf("load offset: %w", err)
	}
	startOffset := *offset

	// A mismatched version means the offset was saved for a different object that reused
	// this name (or is a legacy offset). Discard it and re-read from the start rather than
	// skipping the new object's head; a redundant re-read is tolerated, dropped records are not.
	if startOffset.Version != version {
		if startOffset.Offset != 0 || startOffset.EntryIndex != 0 {
			recordLogger.Info("stored offset was written for a different object version; restarting from the beginning",
				zap.String("offset_storage_key", offsetStorageKey),
				zap.String("stored_version", startOffset.Version),
				zap.String("object_version", version))
		}
		startOffset = *blobstream.NewOffset(0)
	}

	if startOffset.Offset == 0 && startOffset.EntryIndex == 0 {
		recordLogger.Debug("no offset found, starting from beginning", zap.String("offset_storage_key", offsetStorageKey))
	} else {
		recordLogger.Debug("loaded offset", zap.String("offset_storage_key", offsetStorageKey),
			zap.Int("entry_index", startOffset.EntryIndex), zap.Int64("offset", startOffset.Offset))
	}

	bufferedReader, err := stream.BufferedReader(ctx)
	if err != nil {
		return fmt.Errorf("get stream reader: %w", err)
	}

	newProducer := blobstream.NewRecordProducer
	if w.newRecordProducer != nil {
		newProducer = w.newRecordProducer
	}
	producer, err := newProducer(ctx, stream, bufferedReader, w.recordParseError)
	if err != nil {
		return fmt.Errorf("create parser: %w", err)
	}
	// Release a materialized archive's temp file even if the iterator is not driven below.
	defer blobstream.CloseProducer(producer)

	ld := plog.NewLogs()
	rls := ld.ResourceLogs().AppendEmpty()
	rls.Resource().Attributes().PutStr("gcs.bucket", bucket)
	rls.Resource().Attributes().PutStr("gcs.object", object)
	lrs := rls.ScopeLogs().AppendEmpty().LogRecords()

	batchesConsumedCount := 0

	// Parse logs into a sequence of log records
	logs, err := producer.Records(ctx, startOffset)
	if err != nil {
		return fmt.Errorf("parse logs: %w", err)
	}

	// parseErr records a cancellation that stopped the read. The records already read
	// are delivered below. The error is returned, so the message nacks.
	var parseErr error

	for log, err := range logs {
		if err != nil {
			if blobstream.IsCancellation(err) {
				parseErr = err
				break
			}
			// A DLQ-condition error (for example an archive-bomb limit) is fatal for
			// the whole object: fail so the message is routed to the DLQ rather than
			// silently skipped.
			if isDLQConditionError(err) {
				return err
			}
			// A broken stream is fatal for the whole object. Acking here would drop
			// every record after the break with no way to recover them, so fail and
			// let the message redeliver and resume from the saved offset.
			if blobstream.IsStreamRead(err) {
				return err
			}
			// Skipping the individual record rather than nacking the whole message, since
			// retrying a malformed record would produce the same error.  The remaining
			// records in the object can still be ingested successfully.
			recordLogger.Error("parse log", zap.Error(err))
			w.recordParseError(ctx)
			continue
		}

		// Create a log record for this line fragment
		lr := lrs.AppendEmpty()
		lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(now))
		lr.SetTimestamp(pcommon.NewTimestampFromTime(now))

		err = producer.AppendLogBody(ctx, lr, log)
		if err != nil {
			// Same rationale as above: skip the record rather than failing the whole object.
			recordLogger.Error("append log body", zap.Error(err))
			w.recordParseError(ctx)
			continue
		}

		if ld.LogRecordCount() >= w.maxLogsEmitted {
			if err := w.flush(ctx, ld, batchesConsumedCount, recordLogger); err != nil {
				return err
			}

			batchesConsumedCount++
			recordLogger.Debug("Reached max logs for single batch, starting new batch", zap.Int("batches_consumed_count", batchesConsumedCount))

			// Save the offset to storage
			w.checkpoint(ctx, offsetStorageKey, version, producer.Position(), recordLogger)

			ld = plog.NewLogs()
			rls = ld.ResourceLogs().AppendEmpty()
			rls.Resource().Attributes().PutStr("gcs.bucket", bucket)
			rls.Resource().Attributes().PutStr("gcs.object", object)
			lrs = rls.ScopeLogs().AppendEmpty().LogRecords()
		}
	}

	if ld.LogRecordCount() > 0 {
		if err := w.flush(ctx, ld, batchesConsumedCount, recordLogger); err != nil {
			return err
		}
		if parseErr == nil {
			recordLogger.Debug("processed GCS object", zap.Int("batches_consumed_count", batchesConsumedCount+1))
		}

		// Save the offset to storage
		w.checkpoint(ctx, offsetStorageKey, version, producer.Position(), recordLogger)
	}

	if parseErr != nil {
		// The read stopped partway. Everything read is delivered and checkpointed
		// above, so redelivery resumes at the first unread record.
		return fmt.Errorf("read object: %w", parseErr)
	}

	return nil
}

// flush sends a batch to the next consumer on a drain context. Records already read
// are delivered during wind-down.
//
// The line parser drops a truncated record, so a batch always ends on a record
// boundary and the checkpoint stays accurate. Reading stops at the next failed refill.
func (w *Worker) flush(ctx context.Context, ld plog.Logs, batchesConsumedCount int, recordLogger *zap.Logger) error {
	flushCtx, cancel := blobstream.DrainContext(ctx)
	defer cancel()

	obsCtx := w.obsrecv.StartLogsOp(flushCtx)
	err := consumeWithRetry(flushCtx, w.errorBackOff, recordLogger, func() error {
		return w.nextConsumer.ConsumeLogs(flushCtx, ld)
	})
	w.obsrecv.EndLogsOp(obsCtx, metadata.Type.String(), ld.LogRecordCount(), err)
	if err != nil {
		recordLogger.Error("consume logs", zap.Error(err), zap.Int("batches_consumed_count", batchesConsumedCount))
		return fmt.Errorf("consume logs: %w", err)
	}
	w.metrics.GcseventBatchSize.Record(flushCtx, int64(ld.LogRecordCount()))
	return nil
}

// checkpoint saves the parse position on a fully detached context, so a cancellation
// landing while the save is in flight cannot abort it and strand the offset. The trade
// is that a live shutdown waits for the save. Delivery flushes stay on the drain
// context; only the offset save is unconditionally detached.
func (w *Worker) checkpoint(ctx context.Context, offsetStorageKey, version string, pos blobstream.Offset, recordLogger *zap.Logger) {
	saveCtx, cancel := blobstream.CleanupContext(ctx)
	defer cancel()

	// Stamp the object version so a redelivery of this same object resumes here, while a
	// different object reusing the name is detected on load and read from the start.
	pos.Version = version
	if err := w.offsetStorage.SaveStorageData(saveCtx, offsetStorageKey, &pos); err != nil {
		recordLogger.Error("Failed to save offset", zap.Error(err), zap.String("offset_storage_key", offsetStorageKey), zap.Int("entry_index", pos.EntryIndex), zap.Int64("offset", pos.Offset))
	}
}

// recordParseError counts a record or archive entry that could not be parsed and was
// skipped. blobstream calls it so the shared parser stack can report into this
// receiver's own metric.
func (w *Worker) recordParseError(ctx context.Context) {
	if w.metrics != nil {
		w.metrics.GcseventParseErrors.Add(ctx, 1)
	}
}

// dlqErrorKind categorizes an error into a DLQ condition bucket.
type dlqErrorKind int

const (
	dlqErrorKindNone dlqErrorKind = iota
	dlqErrorKindFileNotFound
	dlqErrorKindPermissionDenied
	dlqErrorKindUnsupportedFile
)

// dlqConditionKind returns the DLQ error kind for the given error, or
// dlqErrorKindNone if the error does not trigger a DLQ condition.
func dlqConditionKind(err error) dlqErrorKind {
	// Cancellation is never a DLQ condition. A config push must not send good data to
	// the dead-letter queue.
	if err == nil || blobstream.IsCancellation(err) {
		return dlqErrorKindNone
	}
	// GCS returns storage.ErrObjectNotExist when the object is not found.
	if errors.Is(err, storage.ErrObjectNotExist) {
		return dlqErrorKindFileNotFound
	}
	// GCS returns *googleapi.Error with Code 403 for permission denied.
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == 403 {
		return dlqErrorKindPermissionDenied
	}
	// Content this receiver can never parse: an unsupported type, a corrupt archive,
	// or an archive that tripped a bomb-safety limit.
	if blobstream.IsUnsupportedContent(err) {
		return dlqErrorKindUnsupportedFile
	}
	return dlqErrorKindNone
}

// isDLQConditionError checks if an error should trigger DLQ behavior.
func isDLQConditionError(err error) bool {
	return dlqConditionKind(err) != dlqErrorKindNone
}

// recordDLQMetrics records metrics for DLQ conditions based on the error type.
func (w *Worker) recordDLQMetrics(ctx context.Context, err error) {
	if w.metrics == nil {
		return
	}

	switch dlqConditionKind(err) {
	case dlqErrorKindFileNotFound:
		w.metrics.GcseventDlqFileNotFoundErrors.Add(ctx, 1)
	case dlqErrorKindPermissionDenied:
		w.metrics.GcseventDlqIamErrors.Add(ctx, 1)
	case dlqErrorKindUnsupportedFile:
		w.metrics.GcseventDlqUnsupportedFileErrors.Add(ctx, 1)
	default:
		w.metrics.GcseventFailures.Add(ctx, 1)
	}
}

// handleProcessingError handles errors from processing records.
// For DLQ conditions the message is nacked (deadline set to 0) for immediate
// redelivery / DLQ processing. For transient errors the message is also nacked
// so Pub/Sub can redeliver it after the ack deadline expires.
func (w *Worker) handleProcessingError(ctx context.Context, ackID, subscriptionPath string, err error, logger *zap.Logger) {
	// Only a genuine shutdown/config-push counts as a cancellation: our own context is
	// cancelled, or the error is context.Canceled. A bare DeadlineExceeded with a live
	// context is a downstream timeout (backpressure), not a shutdown, so it must fall
	// through to the retry path below rather than nacking for immediate redelivery.
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		// A config push or a shutdown cancelled the context. Nack the message, so it
		// redelivers at once and resumes from the checkpoint. This is not a failure.
		logger.Info("processing cancelled, nacking message for redelivery", zap.Error(err))
		w.nackMessage(ctx, ackID, subscriptionPath)
		return
	}
	if isDLQConditionError(err) {
		logger.Error("DLQ condition triggered, nacking message for redelivery/DLQ processing", zap.Error(err))
		w.recordDLQMetrics(ctx, err)
		w.nackMessage(ctx, ackID, subscriptionPath)
		return
	}
	// A transient failure such as a broken source stream. Preserve the message and let
	// the ack deadline lapse, so redelivery backs off instead of retrying at once. This
	// matches the awss3 receiver's visibility-timeout behavior. The cancellation and DLQ
	// conditions above still nack for immediate redelivery.
	logger.Error("error processing record, preserving message for retry", zap.Error(err))
	w.metrics.GcseventFailures.Add(ctx, 1)
}
