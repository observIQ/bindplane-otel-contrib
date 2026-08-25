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

// Package worker provides a worker that processes S3 event notifications.
package worker // import "github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/worker"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client"
	"github.com/observiq/bindplane-otel-contrib/internal/blobstream"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/constants"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
)

// AWS error codes for DLQ condition detection
const (
	AWSErrorCodeAccessDenied = "AccessDenied"
	AWSErrorCodeForbidden    = "Forbidden"
	AWSErrorCodeNoSuchKey    = "NoSuchKey"
)

// parseFunc defines the signature for parsing notification messages into S3 events
type parseFunc func(messageBody string) (*events.S3Event, error)

// getParseFuncForNotificationType returns the appropriate parse function for the given notification type
func (w *Worker) getParseFuncForNotificationType(notificationType string) parseFunc {
	switch notificationType {
	case constants.NotificationTypeSNS:
		return ParseSNSToS3Event
	case constants.NotificationTypeS3:
		fallthrough
	default:
		return parseS3Event
	}
}

// parseS3Event parses a direct S3 event notification
func parseS3Event(messageBody string) (*events.S3Event, error) {
	notification := new(events.S3Event)
	err := json.Unmarshal([]byte(messageBody), notification)
	return notification, err
}

// isDLQConditionError checks if an error should trigger DLQ behavior and returns the specific error type
func isDLQConditionError(err error) error {
	// Cancellation is never a DLQ condition. The check is explicit because the
	// classifiers below match on the error text.
	if err == nil || blobstream.IsCancellation(err) {
		return nil
	}
	if isAccessDeniedError(err) {
		return &DLQError{Type: "iam_permission_denied", Err: err}
	}
	if isNoSuchKeyError(err) {
		return &DLQError{Type: "file_not_found", Err: err}
	}
	if isUnsupportedFileTypeError(err) {
		return &DLQError{Type: "unsupported_file_type", Err: err}
	}
	return nil
}

// isAccessDeniedError checks if the error is an IAM permission (AccessDenied) error
func isAccessDeniedError(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == AWSErrorCodeAccessDenied || apiErr.ErrorCode() == AWSErrorCodeForbidden
	}
	// Also check for string-based errors
	errStr := err.Error()
	return strings.Contains(errStr, AWSErrorCodeAccessDenied) || strings.Contains(errStr, AWSErrorCodeForbidden)
}

// isNoSuchKeyError checks if the error is a file not found (NoSuchKey) error
func isNoSuchKeyError(err error) bool {
	var noSuchKeyErr *s3types.NoSuchKey
	if errors.As(err, &noSuchKeyErr) {
		return true
	}
	// Also check for string-based errors
	errStr := err.Error()
	return strings.Contains(errStr, AWSErrorCodeNoSuchKey)
}

// isUnsupportedFileTypeError checks if the error indicates an unsupported file type
func isUnsupportedFileTypeError(err error) bool {
	return blobstream.IsUnsupportedContent(err)
}

// DLQError represents an error that should trigger DLQ behavior
type DLQError struct {
	Type string
	Err  error
}

func (e *DLQError) Error() string {
	return e.Err.Error()
}

func (e *DLQError) Unwrap() error {
	return e.Err
}

// Worker processes S3 event notifications.
// It is responsible for processing messages from the SQS queue and sending them to the next consumer.
// It also handles deleting messages from the SQS queue after they have been processed.
// It is designed to be used in a worker pool.
type Worker struct {
	logger        *zap.Logger
	tel           component.TelemetrySettings
	client        client.Client
	nextConsumer  consumer.Logs
	offsetStorage storageclient.StorageClient

	// newRecordProducer builds the record producer for an object's decompressed stream.
	// It defaults to blobstream.NewRecordProducer; tests override it to drive the
	// per-record error-handling branches without crafting exotic object content.
	newRecordProducer           func(context.Context, blobstream.LogStream, blobstream.BufferedReader, blobstream.ParseErrorFunc) (blobstream.RecordProducer, error)
	maxLogSize                  int
	maxLogsEmitted              int
	visibilityTimeout           time.Duration
	visibilityExtensionInterval time.Duration
	maxVisibilityWindow         time.Duration
	metrics                     *metadata.TelemetryBuilder
	bucketNameFilter            *regexp.Regexp
	objectKeyFilter             *regexp.Regexp
	notificationType            string
	parseFunc                   parseFunc
	obsrecv                     *receiverhelper.ObsReport
	errorBackOff                configretry.BackOffConfig
}

// Option is a functional option for configuring the Worker
type Option func(*Worker)

// WithBucketNameFilter sets the bucket name filter regex
func WithBucketNameFilter(filter *regexp.Regexp) Option {
	return func(w *Worker) {
		w.bucketNameFilter = filter
	}
}

// WithObjectKeyFilter sets the object key filter regex
func WithObjectKeyFilter(filter *regexp.Regexp) Option {
	return func(w *Worker) {
		w.objectKeyFilter = filter
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

// WithNotificationType sets the notification type
func WithNotificationType(notificationType string) Option {
	return func(w *Worker) {
		w.notificationType = notificationType
	}
}

// New creates a new Worker
func New(tel component.TelemetrySettings, nextConsumer consumer.Logs, client client.Client, obsrecv *receiverhelper.ObsReport, maxLogSize int, maxLogsEmitted int, visibilityTimeout time.Duration, visibilityExtensionInterval time.Duration, maxVisibilityWindow time.Duration, opts ...Option) *Worker {
	w := &Worker{
		logger:                      tel.Logger.With(zap.String("component", "awss3eventreceiver")),
		tel:                         tel,
		client:                      client,
		nextConsumer:                nextConsumer,
		offsetStorage:               storageclient.NewNopStorage(),
		obsrecv:                     obsrecv,
		maxLogSize:                  maxLogSize,
		maxLogsEmitted:              maxLogsEmitted,
		visibilityTimeout:           visibilityTimeout,
		visibilityExtensionInterval: visibilityExtensionInterval,
		maxVisibilityWindow:         maxVisibilityWindow,
		notificationType:            constants.NotificationTypeS3, // Default to S3 notification type
	}

	for _, opt := range opts {
		opt(w)
	}

	// Set the parse function based on the notification type
	w.parseFunc = w.getParseFuncForNotificationType(w.notificationType)

	return w
}

// SetOffsetStorage sets the offset storage client
func (w *Worker) SetOffsetStorage(offsetStorage storageclient.StorageClient) {
	w.offsetStorage = offsetStorage
}

// ProcessMessage processes a message from the SQS queue
func (w *Worker) ProcessMessage(ctx context.Context, msg types.Message, queueURL string, deferThis func()) {
	defer deferThis()

	logger := w.logger.With(zap.String("message_id", *msg.MessageId), zap.String("queue_url", queueURL))

	// Start a goroutine to periodically extend message visibility
	visibilityCtx, cancelVisibility := context.WithCancel(ctx)
	defer cancelVisibility()

	go w.extendMessageVisibility(visibilityCtx, msg, queueURL, logger)
	// Parse the message using the configured parse function
	logger.Debug("parsing message", zap.String("message_id", *msg.MessageId), zap.String("notification_type", w.notificationType))
	notification, err := w.parseFunc(*msg.Body)
	if err != nil {
		logger.Error("unmarshal notification", zap.Error(err))
		w.metrics.S3eventFailures.Add(ctx, 1)
		// We can delete messages with unmarshaling errors as they'll never succeed
		w.deleteMessage(ctx, msg, queueURL, []string{}, logger)
		return
	}
	logger.Debug("processing notification", zap.Int("event.count", len(notification.Records)))

	// Filter records to only include s3:ObjectCreated:* events
	type recordWithDecodedKey struct {
		record     events.S3EventRecord
		decodedKey string
	}
	var objectCreatedRecords []recordWithDecodedKey
	for _, record := range notification.Records {
		key := record.S3.Object.Key

		// URL decode the object key as S3 event notifications may contain URL-encoded keys
		// when object names contain special characters like =, +, /, spaces, etc.
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			logger.Warn("failed to URL decode object key, using original key",
				zap.String("original_key", key),
				zap.Error(err))
			decodedKey = key
		} else if decodedKey != key {
			logger.Debug("URL decoded object key",
				zap.String("original_key", key),
				zap.String("decoded_key", decodedKey))
		}

		recordLogger := logger.With(zap.String("event_name", record.EventName),
			zap.String("bucket", record.S3.Bucket.Name),
			zap.String("key", decodedKey))
		// S3 UI shows the prefix as "s3:ObjectCreated:", but the event name is unmarshalled as "ObjectCreated:"
		if !strings.Contains(record.EventName, "ObjectCreated:") {
			recordLogger.Warn("unexpected event: receiver handles only s3:ObjectCreated:* events",
				zap.String("event_name", record.EventName),
				zap.String("bucket", record.S3.Bucket.Name),
				zap.String("key", decodedKey))
			continue
		}

		if w.bucketNameFilter != nil && !w.bucketNameFilter.MatchString(record.S3.Bucket.Name) {
			recordLogger.Debug("skipping record due to bucket name filter", zap.String("bucket", record.S3.Bucket.Name))
			continue
		}
		if w.objectKeyFilter != nil && !w.objectKeyFilter.MatchString(decodedKey) {
			recordLogger.Debug("skipping record due to object key filter", zap.String("key", decodedKey))
			continue
		}
		objectCreatedRecords = append(objectCreatedRecords, recordWithDecodedKey{
			record:     record,
			decodedKey: decodedKey,
		})
	}

	if len(objectCreatedRecords) == 0 {
		logger.Debug("no s3:ObjectCreated:* events passed filters, skipping", zap.String("message_id", *msg.MessageId))
		w.deleteMessage(ctx, msg, queueURL, []string{}, logger)
		return
	}

	if len(objectCreatedRecords) > 1 {
		logger.Warn("duplicate logs possible: multiple s3:ObjectCreated:* events found in notification", zap.Int("event.count", len(objectCreatedRecords)))
	}

	var keys []string
	for _, recordData := range objectCreatedRecords {
		record := recordData.record
		decodedKey := recordData.decodedKey

		recordLogger := logger.With(zap.String("bucket", record.S3.Bucket.Name), zap.String("key", decodedKey))
		recordLogger.Debug("processing record")

		err := w.processRecord(ctx, record, decodedKey, recordLogger)
		if err != nil {
			w.handleProcessingError(ctx, msg, queueURL, err, recordLogger)
			return
		}
		keys = append(keys, decodedKey)
		w.metrics.S3eventObjectsHandled.Add(ctx, 1)
	}
	w.deleteMessage(ctx, msg, queueURL, keys, logger)
}

func (w *Worker) processRecord(ctx context.Context, record events.S3EventRecord, decodedKey string, recordLogger *zap.Logger) error {
	err := w.consumeLogsFromS3Object(ctx, record, decodedKey, true, recordLogger)
	if err != nil {
		if errors.Is(err, blobstream.ErrNotArrayOrKnownObject) {
			// try again without attempting to parse as JSON
			recordLogger.Debug("parsing as JSON failed, trying again with line parsing")
			return w.consumeLogsFromS3Object(ctx, record, decodedKey, false, recordLogger)
		}
		return err
	}
	return nil
}

func (w *Worker) consumeLogsFromS3Object(ctx context.Context, record events.S3EventRecord, decodedKey string, tryJSON bool, recordLogger *zap.Logger) error {
	bucket := record.S3.Bucket.Name
	size := record.S3.Object.Size
	opts := []func(o *s3.Options){
		func(o *s3.Options) {
			o.Region = record.AWSRegion
		},
	}

	recordLogger.Debug("reading S3 object", zap.Int64("size", size))

	resp, err := w.client.S3().GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(decodedKey),
	}, opts...)
	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	defer resp.Body.Close()

	// version scopes the offset to this object; a replacement object has a different ETag.
	version := aws.ToString(resp.ETag)

	now := time.Now()

	stream := blobstream.LogStream{
		Name:            decodedKey,
		ContentEncoding: resp.ContentEncoding,
		ContentType:     resp.ContentType,
		Body:            resp.Body,
		MaxLogSize:      w.maxLogSize,
		Logger:          recordLogger,
		TryDecoding:     tryJSON,
	}

	// Create the offset storage key for this object
	offsetStorageKey := fmt.Sprintf("%s_%s", OffsetStorageKey, decodedKey)

	// Load the offset from storage
	offset := blobstream.NewOffset(0)
	err = w.offsetStorage.LoadStorageData(ctx, offsetStorageKey, offset)
	if err != nil {
		return fmt.Errorf("load offset: %w", err)
	}
	startOffset := *offset

	// Discard an offset we can't confirm belongs to this object — version mismatch, or an
	// empty version (a backend that omits ETag, where a legacy empty offset would win).
	// Re-reading from the start is tolerated; skipping the new object's head is not.
	if version == "" || startOffset.Version != version {
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

	reader, err := stream.BufferedReader(ctx)
	if err != nil {
		return fmt.Errorf("get stream reader: %w", err)
	}

	newProducer := blobstream.NewRecordProducer
	if w.newRecordProducer != nil {
		newProducer = w.newRecordProducer
	}
	producer, err := newProducer(ctx, stream, reader, w.recordParseError)
	if err != nil {
		return fmt.Errorf("create parser: %w", err)
	}

	ld := plog.NewLogs()
	rls := ld.ResourceLogs().AppendEmpty()
	rls.Resource().Attributes().PutStr("aws.s3.bucket", bucket)
	rls.Resource().Attributes().PutStr("aws.s3.key", decodedKey)
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
			// Cancellation is checked first. A cancelled read is also a broken
			// stream, and shutdown has its own wind-down path.
			if blobstream.IsCancellation(err) {
				parseErr = err
				break
			}
			// A DLQ-condition error (for example an archive-bomb limit) is fatal for
			// the whole object: fail so the message is routed to the DLQ rather than
			// silently skipped.
			if blobstream.IsUnsupportedContent(err) {
				return err
			}
			// A broken stream is fatal for the whole object. Acking here would drop
			// every record after the break with no way to recover them, so fail and
			// let the message redeliver and resume from the saved offset.
			if blobstream.IsStreamRead(err) {
				return err
			}
			// Skipping the individual record rather than nacking the whole message, since
			// retrying a malformed record would produce the same error. The remaining
			// records in the object can still be ingested successfully.
			recordLogger.Error("parse log", zap.Error(err))
			w.recordParseError(ctx)
			continue
		}

		// Create a log record for this line fragment
		lr := lrs.AppendEmpty()
		lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(now))
		lr.SetTimestamp(pcommon.NewTimestampFromTime(record.EventTime))

		err := producer.AppendLogBody(ctx, lr, log)
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
			rls.Resource().Attributes().PutStr("aws.s3.bucket", bucket)
			rls.Resource().Attributes().PutStr("aws.s3.key", decodedKey)
			lrs = rls.ScopeLogs().AppendEmpty().LogRecords()
		}
	}

	if ld.LogRecordCount() > 0 {
		if err := w.flush(ctx, ld, batchesConsumedCount, recordLogger); err != nil {
			return err
		}
		if parseErr == nil {
			recordLogger.Debug("processed S3 object", zap.Int("batches_consumed_count", batchesConsumedCount+1))
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
	w.metrics.S3eventBatchSize.Record(flushCtx, int64(ld.LogRecordCount()))
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
		w.metrics.S3eventParseErrors.Add(ctx, 1)
	}
}

func (w *Worker) deleteMessage(ctx context.Context, msg types.Message, queueURL string, keys []string, recordLogger *zap.Logger) {
	// Ack on a detached context. A cancellation must not leave a consumed object in
	// the queue.
	deleteCtx, cancel := blobstream.CleanupContext(ctx)
	defer cancel()

	deleteParams := &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queueURL),
		ReceiptHandle: msg.ReceiptHandle,
	}
	_, err := w.client.SQS().DeleteMessage(deleteCtx, deleteParams)
	if err != nil {
		recordLogger.Error("delete message", zap.Error(err))
		return
	}
	recordLogger.Debug("deleted message")

	// Delete the offsets for the keys that were processed
	for _, key := range keys {
		offsetStorageKey := fmt.Sprintf("%s_%s", OffsetStorageKey, key)
		if err := w.offsetStorage.DeleteStorageData(deleteCtx, offsetStorageKey); err != nil {
			recordLogger.Error("Failed to delete offset", zap.Error(err), zap.String("offset_storage_key", offsetStorageKey))
		}
	}
}

func (w *Worker) extendMessageVisibility(ctx context.Context, msg types.Message, queueURL string, logger *zap.Logger) {
	monitor := newVisibilityMonitor(logger, msg, w.visibilityTimeout, w.visibilityExtensionInterval, w.maxVisibilityWindow)
	defer monitor.stop()

	logger.Debug("starting visibility extension monitoring",
		zap.Duration("initial_timeout", monitor.visibilityTimeout),
		zap.Duration("extension_interval", monitor.extensionInterval),
		zap.Duration("max_window", monitor.maxVisibilityEndTime.Sub(monitor.startTime)),
		zap.Time("max_end_time", monitor.maxVisibilityEndTime))

	for {
		select {
		case <-ctx.Done():
			logger.Debug("visibility extension stopped due to context cancellation")
			return
		case <-monitor.nextExtensionTimer():
			if monitor.shouldExtendToMax() {
				w.extendToMaxAndStop(ctx, msg, queueURL, monitor, logger)
				return
			}
			if err := w.extendVisibility(ctx, msg, queueURL, w.visibilityExtensionInterval, logger); err != nil {
				logger.Error("failed to extend message visibility", zap.Error(err), zap.Duration("attempted_timeout", w.visibilityExtensionInterval))
				return
			}
			monitor.scheduleNextExtension(logger)
		}
	}
}

type visibilityMonitor struct {
	logger               *zap.Logger
	msg                  types.Message
	startTime            time.Time
	maxVisibilityEndTime time.Time
	visibilityTimeout    time.Duration
	extensionInterval    time.Duration
	timer                *time.Timer
}

func newVisibilityMonitor(logger *zap.Logger, msg types.Message, visibilityTimeout, extensionInterval, maxVisibilityWindow time.Duration) *visibilityMonitor {
	startTime := time.Now()
	firstExtensionTime := startTime.Add(getSafetyMargin(visibilityTimeout))

	return &visibilityMonitor{
		logger:               logger.With(zap.String("message_id", *msg.MessageId)),
		msg:                  msg,
		startTime:            startTime,
		maxVisibilityEndTime: startTime.Add(maxVisibilityWindow),
		visibilityTimeout:    visibilityTimeout,
		extensionInterval:    extensionInterval,
		timer:                time.NewTimer(time.Until(firstExtensionTime)),
	}
}

func getSafetyMargin(timeout time.Duration) time.Duration {
	return timeout * 50 / 100 // 50% of the timeout
}

func (vm *visibilityMonitor) stop() {
	vm.timer.Stop()
}

func (vm *visibilityMonitor) nextExtensionTimer() <-chan time.Time {
	return vm.timer.C
}

func (vm *visibilityMonitor) shouldExtendToMax() bool {
	return !time.Now().Add(vm.extensionInterval).Before(vm.maxVisibilityEndTime)
}

func (vm *visibilityMonitor) scheduleNextExtension(logger *zap.Logger) {
	now := time.Now()
	nextExtensionTime := now.Add(getSafetyMargin(vm.extensionInterval))
	logger.Debug("resetting visibility extension timer", zap.Duration("extension_interval", vm.extensionInterval), zap.Time("next_extension_time", nextExtensionTime))
	vm.timer.Reset(time.Until(nextExtensionTime))
}

func (vm *visibilityMonitor) getRemainingTime() time.Duration {
	return time.Until(vm.maxVisibilityEndTime)
}

func (vm *visibilityMonitor) getTotalVisibilityTime() time.Duration {
	return time.Since(vm.startTime)
}

func (w *Worker) extendToMaxAndStop(ctx context.Context, msg types.Message, queueURL string, monitor *visibilityMonitor, logger *zap.Logger) {
	remainingTime := monitor.getRemainingTime()

	logger.Info("reaching maximum visibility window, extending to max and stopping",
		zap.Duration("total_visibility_time", monitor.getTotalVisibilityTime()),
		zap.Duration("remaining_time", remainingTime),
		zap.Duration("max_window", monitor.maxVisibilityEndTime.Sub(monitor.startTime)))

	if err := w.extendVisibility(ctx, msg, queueURL, remainingTime, logger); err != nil {
		logger.Error("failed to extend message visibility to max", zap.Error(err), zap.Duration("attempted_timeout", remainingTime))
	}
}

func (w *Worker) extendVisibility(ctx context.Context, msg types.Message, queueURL string, timeout time.Duration, logger *zap.Logger) error {
	changeParams := &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(queueURL),
		ReceiptHandle:     msg.ReceiptHandle,
		VisibilityTimeout: int32(timeout.Seconds()),
	}
	logger.Debug("extending message visibility", zap.Duration("timeout", timeout))
	_, err := w.client.SQS().ChangeMessageVisibility(ctx, changeParams)
	return err
}

// recordDLQMetrics records metrics for DLQ conditions based on the error type.
func (w *Worker) recordDLQMetrics(ctx context.Context, errorType string) {
	if w.metrics == nil {
		return
	}

	switch errorType {
	case "iam_permission_denied":
		w.metrics.S3eventDlqIamErrors.Add(ctx, 1)
	case "file_not_found":
		w.metrics.S3eventDlqFileNotFoundErrors.Add(ctx, 1)
	case "unsupported_file_type":
		w.metrics.S3eventDlqUnsupportedFileErrors.Add(ctx, 1)
	default:
		// General failure metric for unknown errors
		w.metrics.S3eventFailures.Add(ctx, 1)
	}
}

// handleDLQCondition handles messages that should be sent to DLQ by resetting visibility and logging
func (w *Worker) handleDLQCondition(ctx context.Context, msg types.Message, queueURL string, err error, logger *zap.Logger) {
	var errorType string
	if err != nil {
		var dlqErr *DLQError
		if errors.As(err, &dlqErr) {
			errorType = dlqErr.Type
			logger.Error("DLQ condition triggered, resetting visibility for DLQ processing",
				zap.Error(dlqErr.Err),
				zap.String("error_type", errorType))
			w.recordDLQMetrics(ctx, errorType)
		} else {
			// Fallback for other errors
			errorType = "unknown_dlq_error"
			logger.Error("DLQ condition triggered for unknown error, resetting visibility for DLQ processing",
				zap.Error(err),
				zap.String("error_type", errorType))
			w.recordDLQMetrics(ctx, errorType)
		}
	}

	if err := w.resetVisibilityTimeout(ctx, msg, queueURL, logger); err != nil {
		logger.Error("failed to reset visibility timeout for DLQ condition", zap.Error(err))
	}
}

// handleProcessingError handles errors from processing records, determining if they should trigger DLQ behavior
func (w *Worker) handleProcessingError(ctx context.Context, msg types.Message, queueURL string, err error, logger *zap.Logger) {
	// Only a genuine shutdown/config-push counts as a cancellation: our own context is
	// cancelled, or the error is context.Canceled. A bare DeadlineExceeded with a live
	// context is a downstream timeout (backpressure), not a shutdown, so it must fall
	// through to the retry path below rather than nacking for immediate redelivery.
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		// A config push or a shutdown cancelled the context. Nack the message, so it
		// redelivers at once and resumes from the checkpoint. This is not a failure.
		logger.Info("processing cancelled, nacking message for redelivery", zap.Error(err))
		if nackErr := w.resetVisibilityTimeout(ctx, msg, queueURL, logger); nackErr != nil {
			logger.Error("failed to nack message after cancellation", zap.Error(nackErr))
		}
		return
	}
	if dlqErr := isDLQConditionError(err); dlqErr != nil {
		w.handleDLQCondition(ctx, msg, queueURL, dlqErr, logger)
		return
	}
	logger.Error("error processing record, preserving message in SQS for retry", zap.Error(err))
	w.metrics.S3eventFailures.Add(ctx, 1)
}

// resetVisibilityTimeout resets the message visibility timeout to 0, making it immediately available for DLQ processing
func (w *Worker) resetVisibilityTimeout(ctx context.Context, msg types.Message, queueURL string, logger *zap.Logger) error {
	// Nack on a detached context. The message then redelivers without waiting for the
	// full visibility timeout.
	nackCtx, cancel := blobstream.CleanupContext(ctx)
	defer cancel()

	changeParams := &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(queueURL),
		ReceiptHandle:     msg.ReceiptHandle,
		VisibilityTimeout: 0, // Reset to 0 to make message immediately available
	}
	logger.Debug("resetting message visibility timeout for DLQ processing")
	_, err := w.client.SQS().ChangeMessageVisibility(nackCtx, changeParams)
	return err
}
