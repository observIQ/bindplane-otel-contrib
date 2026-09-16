// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package azureblobpollingreceiver //import "github.com/observiq/bindplane-otel-contrib/receiver/azureblobpollingreceiver"

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/observiq/bindplane-otel-contrib/internal/azureblob"
	"github.com/observiq/bindplane-otel-contrib/internal/blobconsume"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pipeline"
	"go.uber.org/zap"
)

// newAzureBlobClient is the function used to create new Azure Blob Clients.
// Meant to be overwritten for tests
var newAzureBlobClient = azureblob.NewAzureBlobClient

// defaultIncrementalRevisitWindow is the default for Config.IncrementalRevisitWindow:
// how far back each poll re-lists blobs to pick up in-place appends, and how long
// after a blob's last observed change it is held before being treated as sealed.
// Azure NSG/VNet flow logs write one blob per hour, grown all hour via PutBlock, so
// two hours covers the current partition plus the previous one until it seals.
const defaultIncrementalRevisitWindow = 2 * time.Hour

// defaultFingerprintSize is the fallback number of leading blob bytes used to
// identify an append-growable blob across reads (so a replaced or truncated blob
// resets its offset instead of resuming at a stale position), used when the config
// does not set fingerprint_size. minFingerprintSize is the smallest a caller may
// configure, mirroring the filelog receiver's floor.
const (
	defaultFingerprintSize = blobconsume.DefaultFingerprintSize
	minFingerprintSize     = 16
)

// quarantineRetention bounds how long a quarantined (unparseable) blob's progress
// entry is kept before it is pruned, so a source that keeps producing unparseable
// blobs cannot grow the checkpoint without bound. Generous, since a blob that changes
// within the window is picked up again through the normal read path.
const quarantineRetention = 7 * 24 * time.Hour

// sealSkewTolerance keeps a blob from being sealed prematurely when its mtime falls
// slightly before the horizon while its path-time is still in the listing window (clock
// skew, or a path that encodes the period end but was written at the period start).
// Without the tolerance such a blob ages out while still listed, so a no-checkpoint
// re-read from offset 0 repeats every poll. This absorbs minor skew; a larger gap (an
// intentional period-end path) is a documented limitation (see the README).
const sealSkewTolerance = 5 * time.Minute

// maxFinalizeFailures bounds seal-time flush/delete retries: after this many consecutive
// failures a blob is quarantined instead of retried, so a permanently failing blob (e.g.
// a 403 after an ACL change) stops re-erroring every poll and eventually prunes.
const maxFinalizeFailures = 5

type pollingReceiver struct {
	logger             *zap.Logger
	id                 component.ID
	cfg                *Config
	azureClient        azureblob.BlobClient
	supportedTelemetry pipeline.Signal
	consumer           blobconsume.Consumer
	checkpoint         *PollingCheckPoint
	checkpointStore    storageclient.StorageClient

	pollInterval    time.Duration
	initialLookback time.Duration
	revisitWindow   time.Duration

	// mutexes for ensuring a thread safe checkpoint
	mut *sync.Mutex
	wg  *sync.WaitGroup

	// lastBlob and lastBlobTime should not be modified without locking the mut mutex
	lastBlob     *azureblob.BlobInfo
	lastBlobTime *time.Time

	filenameRegex *regexp.Regexp
	cancelFunc    context.CancelFunc

	// pollDone is closed when the poll-loop goroutine exits, so Shutdown can join it.
	// Not r.wg: the loop calls r.wg.Wait() in processBlobs, so tracking it there would
	// deadlock. Nil until Start runs.
	pollDone chan struct{}

	// nowFn returns the current time; overridable in tests for the revisit
	// horizon and offset pruning. Nil means use time.Now().UTC().
	nowFn func() time.Time

	// expandedRoots holds the root_folder prefixes resolved for the current poll
	// (after glob expansion, or the static root_folder if no glob was used).
	// It is consulted by shouldProcessBlob to strip the matching root prefix
	// from blob.Name before applying time_pattern, so a single time_pattern can
	// be used across multiple resource directories (e.g. NSG flow logs).
	// Written from generatePrefixes; read from shouldProcessBlob on the same poll goroutine.
	expandedRoots []string
}

// newMetricsReceiver creates a new metrics specific receiver.
func newMetricsReceiver(id component.ID, logger *zap.Logger, cfg *Config, nextConsumer consumer.Metrics) (*pollingReceiver, error) {
	if cfg.BlobFormat != "" && cfg.BlobFormat != BlobFormatOTLP {
		return nil, fmt.Errorf("blob_format %q is not supported for metrics pipelines, only %q is supported", cfg.BlobFormat, BlobFormatOTLP)
	}

	r, err := newPollingReceiver(id, logger, cfg)
	if err != nil {
		return nil, err
	}

	r.supportedTelemetry = pipeline.SignalMetrics
	r.consumer = blobconsume.NewMetricsConsumer(nextConsumer)

	return r, nil
}

// newLogsReceiver creates a new logs specific receiver.
func newLogsReceiver(id component.ID, logger *zap.Logger, cfg *Config, nextConsumer consumer.Logs) (*pollingReceiver, error) {
	r, err := newPollingReceiver(id, logger, cfg)
	if err != nil {
		return nil, err
	}

	r.supportedTelemetry = pipeline.SignalLogs

	switch cfg.BlobFormat {
	case BlobFormatJSON:
		logger.Debug("Using NDJSON blob format consumer")
		r.consumer = blobconsume.NewNDJSONLogsConsumer(nextConsumer, logger)
	case BlobFormatText:
		logger.Debug("Using raw text blob format consumer")
		r.consumer = blobconsume.NewRawTextLogsConsumer(nextConsumer)
	case BlobFormatRecordsJSON:
		logger.Debug("Using records-json blob format consumer")
		r.consumer = blobconsume.NewRecordsJSONLogsConsumer(nextConsumer, logger)
	case BlobFormatOTLP, "":
		logger.Debug("Using OTLP blob format consumer")
		r.consumer = blobconsume.NewLogsConsumer(nextConsumer)
	default:
		return nil, fmt.Errorf("unsupported blob_format %q, must be one of: %s, %s, %s, %s", cfg.BlobFormat, BlobFormatOTLP, BlobFormatJSON, BlobFormatText, BlobFormatRecordsJSON)
	}

	return r, nil
}

// newTracesReceiver creates a new traces specific receiver.
func newTracesReceiver(id component.ID, logger *zap.Logger, cfg *Config, nextConsumer consumer.Traces) (*pollingReceiver, error) {
	if cfg.BlobFormat != "" && cfg.BlobFormat != BlobFormatOTLP {
		return nil, fmt.Errorf("blob_format %q is not supported for traces pipelines, only %q is supported", cfg.BlobFormat, BlobFormatOTLP)
	}

	r, err := newPollingReceiver(id, logger, cfg)
	if err != nil {
		return nil, err
	}

	r.supportedTelemetry = pipeline.SignalTraces
	r.consumer = blobconsume.NewTracesConsumer(nextConsumer)

	return r, nil
}

// newPollingReceiver creates a new polling receiver
func newPollingReceiver(id component.ID, logger *zap.Logger, cfg *Config) (*pollingReceiver, error) {
	azureClient, err := newAzureBlobClient(cfg.ConnectionString, cfg.BatchSize, cfg.PageSize, logger)
	if err != nil {
		return nil, fmt.Errorf("new Azure client: %w", err)
	}

	// Set initialLookback to pollInterval if not specified
	initialLookback := cfg.InitialLookback
	if initialLookback == 0 {
		initialLookback = cfg.PollInterval
	}

	revisitWindow := cfg.IncrementalRevisitWindow
	if revisitWindow == 0 {
		revisitWindow = defaultIncrementalRevisitWindow
	}

	// Compile filename regex if provided
	var filenameRegex *regexp.Regexp
	if cfg.FilenamePattern != "" {
		filenameRegex, err = regexp.Compile(cfg.FilenamePattern)
		if err != nil {
			return nil, fmt.Errorf("compile filename pattern: %w", err)
		}
	}

	return &pollingReceiver{
		logger:          logger,
		id:              id,
		cfg:             cfg,
		azureClient:     azureClient,
		checkpointStore: storageclient.NewNopStorage(),
		pollInterval:    cfg.PollInterval,
		initialLookback: initialLookback,
		revisitWindow:   revisitWindow,
		mut:             &sync.Mutex{},
		wg:              &sync.WaitGroup{},
		filenameRegex:   filenameRegex,
	}, nil
}

// Start starts the polling receiver
func (r *pollingReceiver) Start(ctx context.Context, host component.Host) error {
	if r.cfg.StorageID != nil {
		checkpointStore, err := storageclient.NewStorageClient(ctx, host, *r.cfg.StorageID, r.id, r.supportedTelemetry)
		if err != nil {
			return fmt.Errorf("NewCheckpointStorage: %w", err)
		}
		r.checkpointStore = checkpointStore
	}

	// Load checkpoint
	checkpoint := NewPollingCheckpoint()
	err := r.checkpointStore.LoadStorageData(ctx, r.checkpointKey(), checkpoint)
	if err != nil {
		r.logger.Warn("Error loading checkpoint, starting fresh", zap.Error(err))
		checkpoint = NewPollingCheckpoint()
	}
	r.checkpoint = checkpoint

	cancelCtx, cancel := context.WithCancel(context.Background())
	r.cancelFunc = cancel

	// Join point for Shutdown; see pollDone's field doc for why it isn't r.wg.
	r.pollDone = make(chan struct{})
	go func() {
		defer close(r.pollDone)
		r.pollLoop(cancelCtx)
	}()
	return nil
}

// Shutdown shuts down the polling receiver
func (r *pollingReceiver) Shutdown(ctx context.Context) error {
	if r.cancelFunc != nil {
		r.cancelFunc()
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Join the poll loop before touching the store, so its final checkpoint save can't race
	// the Close below. pollDone is nil if Start was never called.
	if r.pollDone != nil {
		select {
		case <-r.pollDone:
		case <-shutdownCtx.Done():
			return fmt.Errorf("shutdown timeout waiting for poll loop: %w", shutdownCtx.Err())
		}
	}

	// Wait for any in-progress per-blob operations to finish.
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-shutdownCtx.Done():
		return fmt.Errorf("shutdown timeout: %w", shutdownCtx.Err())
	}

	var errs error
	if err := r.makeCheckpoint(shutdownCtx); err != nil {
		errs = errors.Join(errs, fmt.Errorf("error while saving checkpoint: %w", err))
	}

	r.mut.Lock()
	defer r.mut.Unlock()

	if err := r.checkpointStore.Close(shutdownCtx); err != nil {
		errs = errors.Join(errs, fmt.Errorf("error while closing checkpoint store: %w", err))
	}
	r.logger.Info("Shutdown complete")
	return errs
}

// getTelemetryType returns the telemetry type for the receiver
// It first checks if an explicit telemetry_type is configured,
// otherwise falls back to the pipeline type the receiver is configured in
func (r *pollingReceiver) getTelemetryType() pipeline.Signal {
	if r.cfg.TelemetryType != "" {
		switch r.cfg.TelemetryType {
		case "logs":
			return pipeline.SignalLogs
		case "metrics":
			return pipeline.SignalMetrics
		case "traces":
			return pipeline.SignalTraces
		}
	}
	return r.supportedTelemetry
}

// pollLoop continuously polls for new blobs at the configured interval
func (r *pollingReceiver) pollLoop(ctx context.Context) {
	r.logger.Info("Starting continuous polling", zap.Duration("poll_interval", r.pollInterval))

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	// Run first poll immediately
	r.runPoll(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("Context cancelled, stopping polling")
			return
		case <-ticker.C:
			r.runPoll(ctx)
		}
	}
}

// incrementalHorizonStart widens startingTime back to at least revisitWindow before the anchor.
// The anchor is lastPollTime, or now when the clock stepped back (lastPollTime not in the past),
// so a future persisted LastPollTime can't push the window forward and skip growing blobs.
func incrementalHorizonStart(startingTime, lastPollTime, now time.Time, revisitWindow time.Duration) time.Time {
	anchor := now
	if !lastPollTime.IsZero() && lastPollTime.Before(now) {
		anchor = lastPollTime
	}
	if horizonStart := anchor.Add(-revisitWindow); horizonStart.Before(startingTime) {
		return horizonStart
	}
	return startingTime
}

// runPoll executes a single poll operation with dynamic time window
func (r *pollingReceiver) runPoll(ctx context.Context) {
	now := r.now()

	r.mut.Lock()
	lastPollTime := r.checkpoint.LastPollTime
	r.mut.Unlock()

	// Calculate time window
	var startingTime, endingTime time.Time
	if lastPollTime.IsZero() {
		// First poll - use initial lookback
		startingTime = now.Add(-r.initialLookback)
		r.logger.Info("First poll, using initial lookback",
			zap.Time("starting_time", startingTime),
			zap.Time("ending_time", now),
			zap.Duration("lookback", r.initialLookback))
	} else {
		// Subsequent polls - use last poll time
		startingTime = lastPollTime
		r.logger.Debug("Polling with dynamic window",
			zap.Time("starting_time", startingTime),
			zap.Time("ending_time", now))
	}
	endingTime = now

	// For append-growable formats, widen the window back to at least the revisit window before
	// the last poll so a blob still growing when we last polled stays listed until it seals, even
	// across a restart. Fingerprint and offset dedup prevent any byte from being consumed twice.
	if r.isIncrementalFormat() {
		startingTime = incrementalHorizonStart(startingTime, lastPollTime, now, r.revisitWindow)
	}

	// Reset lastBlob tracking for this poll.
	r.mut.Lock()
	r.lastBlob = nil
	r.lastBlobTime = nil
	r.mut.Unlock()

	// Create fresh channels for this poll to avoid closing already-closed channels
	blobChan := make(chan []*azureblob.BlobInfo)
	errChan := make(chan error)
	doneChan := make(chan struct{})

	// time.Now (not r.now) so time.Since below stays monotonic: r.now returns a UTC time with the
	// monotonic reading stripped, which would make duration_seconds wrong on an NTP step. This is
	// only the elapsed-duration log; the window math uses r.now.
	pollStartTime := time.Now()
	r.logger.Info("Starting poll",
		zap.Time("poll_time", pollStartTime),
		zap.Time("start", startingTime),
		zap.Time("end", endingTime))

	// Scope the producer to this poll and cancel it once the consumer loop returns. On an
	// error or a cancellation the loop stops draining blobChan/errChan, so without this the
	// pullBlobs goroutine (and any StreamBlobs send it is blocked on) would hang until
	// Shutdown, leaking one goroutine per errored poll.
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := r.pullBlobs(pollCtx, startingTime, endingTime, doneChan, errChan, blobChan); err != nil {
		// Glob-expansion failure: skip processBlobsLoop so the poll returns without finalizePoll.
		// Finalizing a fake-empty poll would advance the checkpoint and delete still-growing blobs.
		r.logger.Error("Error during poll", zap.Error(err))
		return
	}

	r.processBlobsLoop(pollCtx, doneChan, errChan, blobChan, pollStartTime, startingTime, endingTime)
}

func (r *pollingReceiver) pullBlobs(ctx context.Context, startingTime, endingTime time.Time, doneChan chan struct{}, errChan chan error, blobChan chan []*azureblob.BlobInfo) error {
	// A glob-expansion failure is returned to the caller (which skips finalizePoll); it is known
	// synchronously, so it need not go through errChan like the async StreamBlobs errors.
	prefixes, err := r.generatePrefixes(ctx, startingTime, endingTime)
	if err != nil {
		return err
	}

	// Stream blobs in a goroutine
	go func() {
		defer close(doneChan)
		for _, prefix := range prefixes {
			select {
			case <-ctx.Done():
				return
			default:
			}

			r.logger.Debug("Polling with prefix", zap.Stringp("prefix", prefix))

			// StreamBlobs closes the done channel passed to it, so we create a fresh one
			prefixDoneChan := make(chan struct{})
			r.azureClient.StreamBlobs(ctx, r.cfg.Container, prefix, errChan, blobChan, prefixDoneChan)

			// Wait for this step to complete
			select {
			case <-ctx.Done():
				return
			case <-prefixDoneChan:
				// Continue to the next prefix
			}
		}
	}()
	return nil
}

func (r *pollingReceiver) generatePrefixes(ctx context.Context, startingTime, endingTime time.Time) ([]*string, error) {
	// Expand glob patterns in root_folder. nil = no root_folder (scan everything); empty slice =
	// glob matched zero directories (scan nothing); error = expansion failed (poll must not finalize).
	rootFolders, err := r.expandGlobRootFolders(ctx)
	if err != nil {
		return nil, err
	}
	r.expandedRoots = rootFolders

	if rootFolders != nil && len(rootFolders) == 0 {
		// Glob matched zero directories — return empty to scan nothing
		return []*string{}, nil
	}

	if r.cfg.UseTimePatternAsPrefix && r.cfg.TimePattern != "" {
		var allPrefixes []*string
		for _, root := range rootFolders {
			prefixes := r.generatePrefixesForRoot(startingTime, endingTime, root)
			allPrefixes = append(allPrefixes, prefixes...)
		}
		if len(allPrefixes) > 0 {
			return allPrefixes, nil
		}
		// we return a nil entry here to indicate that there is no prefix for this poll
		// when there is no prefix, the StreamBlobs call will scan the entire container
		return []*string{nil}, nil
	}

	if len(rootFolders) > 0 {
		prefixes := make([]*string, len(rootFolders))
		for i := range rootFolders {
			rootCopy := rootFolders[i]
			prefixes[i] = &rootCopy
		}
		return prefixes, nil
	}

	// we return a nil entry here to indicate that there is no prefix for this poll
	// when there is no prefix, the StreamBlobs call will scan the entire container
	return []*string{nil}, nil
}

// expandGlobRootFolders expands glob patterns in root_folder by listing directory prefixes from
// Azure. No glob characters: returns root_folder as-is with no API call. It errors only when the
// listing fails and fallback_on_glob_failure is off, so that failure is not mistaken for a
// legitimate zero-match (which returns an empty slice, nil error).
func (r *pollingReceiver) expandGlobRootFolders(ctx context.Context) ([]string, error) {
	if r.cfg.RootFolder == "" {
		return nil, nil
	}

	if !isGlobPattern(r.cfg.RootFolder) {
		return []string{r.cfg.RootFolder}, nil
	}

	staticPrefix, globPattern := splitGlobPrefix(r.cfg.RootFolder)

	prefixes, err := r.azureClient.ListPrefixes(ctx, r.cfg.Container, staticPrefix)
	if err != nil {
		if r.cfg.FallbackOnGlobFailure {
			r.logger.Error("Failed to list prefixes for glob expansion, falling back to static prefix (fallback_on_glob_failure=true)",
				zap.String("root_folder", r.cfg.RootFolder),
				zap.String("static_prefix", staticPrefix),
				zap.Error(err))
			return []string{staticPrefix}, nil
		}
		// Surface the failure so the poll returns before finalizePoll. Returning an empty slice
		// here would look like a zero-match and finalize, aging out still-growing blobs.
		return nil, fmt.Errorf("list prefixes for glob expansion of %q (set fallback_on_glob_failure=true to scan under %q instead): %w", r.cfg.RootFolder, staticPrefix, err)
	}

	var matched []string
	for _, p := range prefixes {
		if matchGlob(globPattern, p) {
			matched = append(matched, p)
		}
	}

	if len(matched) == 0 {
		r.logger.Warn("Glob pattern matched zero directories, no blobs will be scanned",
			zap.String("root_folder", r.cfg.RootFolder),
			zap.String("static_prefix", staticPrefix),
			zap.Int("candidates", len(prefixes)))
		return []string{}, nil
	}

	r.logger.Debug("Glob expanded root_folder",
		zap.String("pattern", r.cfg.RootFolder),
		zap.Strings("matched", matched))

	return matched, nil
}

// generatePrefixesForRoot generates time-based prefixes for a single root folder.
func (r *pollingReceiver) generatePrefixesForRoot(startingTime, endingTime time.Time, rootFolder string) []*string {
	generated, err := generateTimePrefixes(startingTime, endingTime, r.cfg.TimePattern, rootFolder)
	if err != nil {
		r.logger.Error("Failed to generate time prefixes, falling back to root folder",
			zap.String("root_folder", rootFolder),
			zap.Error(err))
		return []*string{&rootFolder}
	}

	prefixes := make([]*string, len(generated))
	for i, prefix := range generated {
		p := prefix
		prefixes[i] = &p
	}
	return prefixes
}

func (r *pollingReceiver) processBlobsLoop(ctx context.Context, doneChan chan struct{}, errChan chan error, blobChan chan []*azureblob.BlobInfo, pollStartTime time.Time, startingTime, endingTime time.Time) {
	totalProcessed := 0
	totalListed := 0
	totalSkipped := 0
	totalParseFailed := 0
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("Context cancelled during poll")
			return
		case <-doneChan:
			// pullBlobs closes doneChan on a ctx-abort too, so a cancelled poll can land here. Don't
			// finalize it: that would advance LastPollTime past blobs it never listed (silent loss).
			if ctx.Err() != nil {
				r.logger.Info("Context cancelled during poll")
				return
			}
			// total_skipped counts blobs genuinely filtered out (filename, time range, dedup),
			// not blobs that were read but had no new records this poll — those are neither
			// processed nor skipped.
			r.logger.Info("Poll completed",
				zap.Int("total_listed", totalListed),
				zap.Int("total_processed", totalProcessed),
				zap.Int("total_skipped", totalSkipped),
				zap.Int("duration_seconds", int(time.Since(pollStartTime).Seconds())))

			// Every listed blob failed to parse a time: almost always a time-source misconfiguration
			// (default path layout expected, but the blobs aren't laid out that way). Gate on "all
			// failed" so a single stray non-conforming blob among good ones doesn't warn every poll.
			if totalListed > 0 && totalParseFailed == totalListed {
				r.logger.Warn("Listed blobs but none had a parseable time; nothing was ingested. If these blobs aren't in the year=/month=/day=/hour=/ layout, set use_last_modified: true or a matching time_pattern.",
					zap.Int("total_listed", totalListed),
					zap.Int("parse_failed", totalParseFailed))
			}

			r.finalizePoll(ctx, startingTime, endingTime)
			return
		case err := <-errChan:
			r.logger.Error("Error during poll", zap.Error(err))
			return
		case br, ok := <-blobChan:
			// Production never closes blobChan (pullBlobs closes only doneChan). A plain return is the
			// fail-safe default: finalizing a poll the producer didn't signal complete could advance
			// the watermark past unlisted blobs. A future close-as-completion can finalize deliberately.
			if !ok {
				return
			}
			totalListed += len(br)
			numProcessed, numSkipped, numParseFailed := r.processBlobs(ctx, br, startingTime, endingTime)
			totalProcessed += numProcessed
			totalSkipped += numSkipped
			totalParseFailed += numParseFailed
		}
	}
}

// processBlobs dispatches the in-range, unprocessed blobs and returns how many emitted at least
// one record (processed), how many were filtered out without being read (skipped), and how many
// were skipped specifically because their time did not parse (parseFailed, a subset of skipped).
func (r *pollingReceiver) processBlobs(ctx context.Context, blobs []*azureblob.BlobInfo, startingTime, endingTime time.Time) (processed, skipped, parseFailed int) {
	r.logger.Debug("Received a batch of blobs, parsing through them", zap.Int("num_blobs", len(blobs)))
	processedBlobCount := atomic.Int64{}
	skippedFilename := 0
	skippedOther := 0

blobLoop:
	for i, blob := range blobs {
		select {
		case <-ctx.Done():
			// Cancelled mid-batch: count the unvisited blobs as skipped so listed == processed +
			// skipped stays consistent instead of undercounting.
			skippedOther += len(blobs) - i
			break blobLoop
		default:
		}

		// Filter by filename pattern if configured
		if r.filenameRegex != nil {
			// Extract just the filename (not the full path)
			filename := filepath.Base(blob.Name)
			if !r.filenameRegex.MatchString(filename) {
				r.logger.Debug("Skipping blob, filename doesn't match pattern",
					zap.String("blob", blob.Name),
					zap.String("filename", filename),
					zap.String("pattern", r.cfg.FilenamePattern))
				skippedFilename++
				continue
			}
		}

		blobTime, shouldProcess, blobParseFailed := r.shouldProcessBlob(blob, startingTime, endingTime)

		if shouldProcess && blobTime != nil {
			r.wg.Add(1)
			go r.processBlobGoRoutine(ctx, blob, blobTime, &processedBlobCount)
		} else {
			skippedOther++
			if blobParseFailed {
				parseFailed++
			}
		}
	}

	r.wg.Wait()

	processed = int(processedBlobCount.Load())
	r.logger.Debug("Batch processing summary",
		zap.Int("num_in_batch", len(blobs)),
		zap.Int("processed", processed),
		zap.Int("skipped_filename", skippedFilename),
		zap.Int("skipped_other", skippedOther))

	return processed, skippedFilename + skippedOther, parseFailed
}

// shouldParseBlob reports whether a blob has content worth processing. In an
// append-growable config (every blob, gzip included) it defers to the per-blob
// progress record: a blob is (re)read whenever its LastModified changes since the
// last read, so a growing blob is revisited until it seals and an unchanged one is
// skipped. Every other config defers to the name/time-based checkpoint dedup.
func (r *pollingReceiver) shouldParseBlob(blob *azureblob.BlobInfo, blobTime time.Time) bool {
	if r.isIncrementalFormat() {
		// A zero LastModified can't be tracked by the mtime gate (it would read once, then
		// zero==zero skips it forever, never sealed): skip it, as UseLastModified does.
		if blob.LastModified.IsZero() {
			r.logger.Debug("Skipping blob with zero LastModified on the incremental path", zap.String("blob", blob.Name))
			return false
		}
		// The Progress map is written concurrently by per-blob goroutines, so read
		// it under the mutex.
		r.mut.Lock()
		prog, ok := r.checkpoint.ProgressFor(blob.Name)
		r.mut.Unlock()
		if !ok {
			return true
		}
		// A never-read blob keeps retrying every poll (including a static-mtime gzip) rather than
		// being skipped by the mtime gate, until it succeeds or ages out.
		if prog.LastReadError != "" {
			return true
		}
		return !blob.LastModified.Equal(prog.LastModified)
	}
	return r.checkpoint.ShouldParse(blobTime, blob.Name)
}

// shouldProcessBlob decides whether a blob is in range and unprocessed. The third return,
// parseFailed, is true only when the blob carried no parseable time under the configured mode
// (default path layout or time_pattern), so the caller can warn about a likely misconfiguration
// (e.g. a container of plain-named blobs that needs use_last_modified).
func (r *pollingReceiver) shouldProcessBlob(blob *azureblob.BlobInfo, startingTime, endingTime time.Time) (blobTime *time.Time, shouldProcess bool, parseFailed bool) {
	if r.cfg.UseLastModified {
		// Use LastModified timestamp mode
		if blob.LastModified.IsZero() {
			r.logger.Debug("Skipping blob with zero LastModified", zap.String("blob", blob.Name))
			return nil, false, false
		}
		shouldParse := r.shouldParseBlob(blob, blob.LastModified)
		inRange := blobconsume.IsInTimeRange(blob.LastModified, startingTime, endingTime)
		if !shouldParse {
			r.logger.Debug("Skipping blob, already processed (checkpoint)",
				zap.String("blob", blob.Name),
				zap.Time("last_ts", r.checkpoint.LastTs))
		} else if !inRange {
			r.logger.Debug("Skipping blob, outside time range",
				zap.String("blob", blob.Name),
				zap.Time("blob_time", blob.LastModified),
				zap.Time("start", startingTime),
				zap.Time("end", endingTime))
		}
		return &blob.LastModified, shouldParse && inRange, false
	}

	if r.cfg.TimePattern != "" {
		// Use custom time pattern mode. When root_folder (or a glob expansion of
		// it) matches the start of the blob name, trim that prefix and parse the
		// remainder unanchored — this lets a pattern like "y={year}/m={month}/..."
		// work regardless of the variable prefix Azure prepends per resource.
		// When no root matched, the pattern must anchor at the start of the blob
		// name as before, to avoid accidental matches in arbitrary positions.
		pathForPattern, rootTrimmed := r.trimMatchedRoot(blob.Name)
		parsedTime, err := parseTimeFromPattern(pathForPattern, r.cfg.TimePattern, !rootTrimmed)
		if err != nil {
			r.logger.Debug("Skipping blob, failed to parse time from pattern",
				zap.String("blob", blob.Name),
				zap.String("pattern", r.cfg.TimePattern),
				zap.Error(err))
			return nil, false, true
		}
		shouldParse := r.shouldParseBlob(blob, *parsedTime)
		inRange := blobconsume.IsInTimeRange(*parsedTime, startingTime, endingTime)
		if !shouldParse {
			r.logger.Debug("Skipping blob, already processed (checkpoint)",
				zap.String("blob", blob.Name),
				zap.Time("last_ts", r.checkpoint.LastTs))
		} else if !inRange {
			r.logger.Debug("Skipping blob, outside time range",
				zap.String("blob", blob.Name),
				zap.Time("blob_time", *parsedTime),
				zap.Time("start", startingTime),
				zap.Time("end", endingTime))
		}
		return parsedTime, shouldParse && inRange, false
	}

	// Use default structured path parsing mode (year=YYYY/month=MM/...)
	parsedTime, parsedType, err := blobconsume.ParseEntityPath(blob.Name)
	switch {
	case errors.Is(err, blobconsume.ErrInvalidEntityPath):
		r.logger.Debug("Skipping Blob, non-matching blob path", zap.String("blob", blob.Name))
		return nil, false, true
	case err != nil:
		r.logger.Error("Error processing blob path", zap.String("blob", blob.Name), zap.Error(err))
		return nil, false, false
	}
	shouldParse := r.shouldParseBlob(blob, *parsedTime)
	inRange := blobconsume.IsInTimeRange(*parsedTime, startingTime, endingTime)
	if !shouldParse {
		r.logger.Debug("Skipping blob, already processed (checkpoint)",
			zap.String("blob", blob.Name),
			zap.Time("last_ts", r.checkpoint.LastTs))
	} else if !inRange {
		r.logger.Debug("Skipping blob, outside time range",
			zap.String("blob", blob.Name),
			zap.Time("blob_time", *parsedTime),
			zap.Time("start", startingTime),
			zap.Time("end", endingTime))
	}
	return parsedTime, shouldParse && inRange &&
		parsedType == r.supportedTelemetry, false
}

func (r *pollingReceiver) processBlobGoRoutine(ctx context.Context, blob *azureblob.BlobInfo, blobTime *time.Time, processedBlobCount *atomic.Int64) {
	defer r.wg.Done()
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Route the blob: incremental from the stored offset, whole-but-tracked (gzip), or legacy
	// whole-read (otlp). processed reports whether records were emitted (false on quarantine).
	var err error
	processed := true // the legacy path emits on success; the tracked paths report their own
	switch {
	case r.useIncremental(blob.Name):
		processed, err = r.processBlobIncremental(ctx, blob)
	case r.isIncrementalFormat():
		processed, err = r.processBlobWholeTracked(ctx, blob)
	default:
		err = r.processBlob(ctx, blob)
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			r.logger.Error("Error consuming blob", zap.String("blob", blob.Name), zap.Error(err))
		}
		return
	}
	if processed {
		processedBlobCount.Add(1)
	}

	if r.isIncrementalFormat() {
		// Append-growable blobs are deleted at seal (see finalizeAgedBlobs), never
		// mid-stream, and their dedup lives in the progress map rather than the
		// LastTs/ParsedEntities watermark. Nothing more to do here.
		return
	}

	// Legacy whole-blob path (otlp, or any format with the opt-in off): delete after
	// the read and advance the name/time watermark.
	if err := r.conditionallyDeleteBlob(ctx, blob); err != nil {
		r.logger.Error("Error while attempting to delete blob", zap.String("blob", blob.Name), zap.Error(err))
	}

	r.mut.Lock()
	if r.lastBlobTime == nil || r.lastBlobTime.Before(*blobTime) {
		r.lastBlob = blob
		r.lastBlobTime = blobTime
	}
	r.mut.Unlock()
}

// now returns the current time, honoring an injected clock in tests.
func (r *pollingReceiver) now() time.Time {
	if r.nowFn != nil {
		return r.nowFn()
	}
	return time.Now().UTC()
}

// isIncrementalFormat reports whether the configured blob format is one whose
// blobs can grow in place (records-json, json/NDJSON) and therefore should be
// read incrementally by byte offset rather than whole-file once. Gated behind the
// enable_incremental_read opt-in; when it is off every format uses the legacy
// whole-blob path.
func (r *pollingReceiver) isIncrementalFormat() bool {
	if !r.cfg.EnableIncrementalRead {
		return false
	}
	switch r.cfg.BlobFormat {
	case BlobFormatRecordsJSON, BlobFormatJSON:
		return true
	default: // BlobFormatOTLP, BlobFormatText, ""
		return false
	}
}

// useIncremental reports whether this specific blob should be read incrementally.
// Gzip blobs are excluded: a gzip stream cannot be range-tailed, so they fall
// back to whole-download (live-append incremental requires uncompressed blobs).
func (r *pollingReceiver) useIncremental(blobName string) bool {
	return r.isIncrementalFormat() && strings.ToLower(filepath.Ext(blobName)) != ".gz"
}

// fpSize resolves the configured fingerprint_size, falling back to the default when
// unset (0). Validate rejects a positive value below minFingerprintSize, so any
// configured value that reaches here is either 0 (default) or >= minFingerprintSize.
func (r *pollingReceiver) fpSize() int {
	if r.cfg.FingerprintSize >= minFingerprintSize {
		return r.cfg.FingerprintSize
	}
	return defaultFingerprintSize
}

// consumeDelta emits the complete records/lines in delta and reports the bytes consumed (to
// advance the offset) plus whether any real record was emitted. emitted is false when the delta
// yields no complete record (a records-json delta that frames nothing yet, or a whitespace-only
// line), so the poll summary excludes those no-ops. On a consume (emit) error the byte count is 0
// BY CONTRACT: nothing was delivered, so the caller must not advance past these bytes, which are
// re-read next poll. Do not "fix" this to return the parsed count; a caller that advanced on it
// would skip un-emitted records (permanent loss).
func (r *pollingReceiver) consumeDelta(ctx context.Context, delta []byte, atStart bool) (int, bool, error) {
	if r.cfg.BlobFormat == BlobFormatRecordsJSON {
		reframed, bytesConsumed := blobconsume.SplitRecordsJSONDelta(delta, atStart)
		if reframed == nil {
			return bytesConsumed, false, nil
		}
		if err := r.consumer.Consume(ctx, reframed); err != nil {
			return 0, false, fmt.Errorf("consume: %w", err)
		}
		return bytesConsumed, true, nil
	}

	// json (NDJSON) and text are newline-delimited; consume only up to the last
	// complete line and leave any partial trailing line for the next read.
	complete, bytesConsumed := blobconsume.SplitLineDelta(delta)
	if bytesConsumed == 0 {
		return 0, false, nil
	}
	if err := r.consumeContent(ctx, complete); err != nil {
		return 0, false, err
	}
	return bytesConsumed, len(bytes.TrimSpace(complete)) > 0, nil
}

// consumeContent hands newline-delimited content (json/NDJSON) to the consumer,
// which splits it into one log record per line.
func (r *pollingReceiver) consumeContent(ctx context.Context, content []byte) error {
	if err := r.consumer.Consume(ctx, content); err != nil {
		return fmt.Errorf("consume: %w", err)
	}
	return nil
}

// saveProgress records prog for name under the checkpoint mutex. The Progress map is written
// concurrently by per-blob goroutines, so every plain write goes through here.
func (r *pollingReceiver) saveProgress(name string, prog BlobProgress) {
	r.mut.Lock()
	r.checkpoint.UpdateProgress(name, prog)
	r.mut.Unlock()
}

// quarantineBlob marks a blob quarantined (kept, but ignored while unchanged) and refreshes
// its LastModified. It preserves any already-committed Offset/Fingerprint so quarantining a
// mid-blob entry (a permanent read error partway through) does not force a revival to re-read
// from byte 0 and re-emit the whole blob. It runs warn only on the transition into quarantine:
// a growing blob is re-listed with a new mtime every poll and so re-enters this path each time,
// so warning on every visit would spam the log.
func (r *pollingReceiver) quarantineBlob(name string, lastModified time.Time, warn func()) {
	r.mut.Lock()
	prog, existed := r.checkpoint.ProgressFor(name)
	wasQuarantined := existed && prog.Quarantined
	prog.LastModified = lastModified
	prog.Quarantined = true
	prog.LastReadError = "" // quarantined, not a never-read-keep-retrying entry
	r.checkpoint.UpdateProgress(name, prog)
	r.mut.Unlock()
	if !wasQuarantined {
		warn()
	}
}

// trackNeverReadFailure records the failure reason on a never-read (offset 0) entry so its drop is
// logged if it ages out unread. It keeps an existing Quarantined flag but resets Offset/Fingerprint,
// since a stale offset (e.g. after a fingerprint reset) would strand finalize on a replaced blob.
func (r *pollingReceiver) trackNeverReadFailure(blob *azureblob.BlobInfo, cause error) {
	r.mut.Lock()
	prog, _ := r.checkpoint.ProgressFor(blob.Name)
	r.checkpoint.UpdateProgress(blob.Name, BlobProgress{
		Quarantined:   prog.Quarantined,
		LastModified:  blob.LastModified,
		LastReadError: truncateError(cause),
	})
	r.mut.Unlock()
}

// maxLastReadErrorLen bounds the persisted LastReadError: Azure SDK errors carry multi-KB response
// dumps, and this string is JSON-marshaled into the checkpoint on every poll while the entry lives.
const maxLastReadErrorLen = 256

// truncateError returns cause's message bounded to maxLastReadErrorLen (plus a 3-char ellipsis on a
// cut) so a verbose error does not bloat the persisted checkpoint.
func truncateError(cause error) string {
	msg := cause.Error()
	if len(msg) <= maxLastReadErrorLen {
		return msg
	}
	truncated := msg[:maxLastReadErrorLen]
	// The byte cut may split a multi-byte rune; back up to a rune boundary (at most UTFMax-1 bytes)
	// so the cut does not introduce one. Input already invalid before the cut is left as-is.
	for i := 0; i < utf8.UTFMax-1 && !utf8.ValidString(truncated); i++ {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "..."
}

// quarantineOnPermanentReadError quarantines the blob and returns true when err is a
// permanent read failure (auth/config); such a read would otherwise re-error every poll on
// the normal incremental path, so quarantining bounds it as the seal path already does. A
// transient error returns false so the caller retries it on a later poll.
func (r *pollingReceiver) quarantineOnPermanentReadError(blob *azureblob.BlobInfo, err error) bool {
	if !azureblob.IsPermanentError(err) {
		return false
	}
	r.quarantineBlob(blob.Name, blob.LastModified, func() {
		r.logger.Warn("Quarantining blob after a permanent read error; ignoring it while unchanged",
			zap.String("blob", blob.Name), zap.Error(err))
	})
	return true
}

// verifyResumeIdentity re-reads the blob's leading fpSize bytes and reports whether they still
// match the stored fingerprint (needsReset=true on a mismatch, or when the blob shrank below the
// stored offset → restart from 0), returning head so the caller can re-establish the fingerprint.
// Skipped — (nil, false, nil) — when there is nothing to verify (offset 0, no fingerprint, or
// assume_append_only). Read errors are returned raw; the two callers classify not-found / 416 /
// permanent differently.
func (r *pollingReceiver) verifyResumeIdentity(ctx context.Context, name string, offset int64, fingerprint []byte) (head []byte, needsReset bool, err error) {
	if offset == 0 || len(fingerprint) == 0 || r.cfg.AssumeAppendOnly {
		return nil, false, nil
	}
	head, total, err := r.azureClient.DownloadBlobRange(ctx, r.cfg.Container, name, 0, int64(r.fpSize()))
	if err != nil {
		return nil, false, err
	}
	// A shorter replacement can share the fpSize-byte prefix and pass SameBlob, so also reset when
	// the authoritative size shows the blob no longer reaches offset (-1 = unknown, skip).
	needsReset = !blobconsume.SameBlob(fingerprint, blobconsume.Fingerprint(head, r.fpSize()))
	if total >= 0 && total < offset {
		needsReset = true
	}
	return head, needsReset, nil
}

// processBlobIncremental reads only the bytes appended to the blob since the last
// poll (from the stored offset to the current end), consumes the complete
// records/lines found, and advances the stored offset. A fingerprint of the blob's
// leading bytes guards the resume point: if the blob was replaced or truncated, the
// offset resets to 0. Append-growable blobs (e.g. Azure flow logs written all hour
// into one PT1H.json) are thereby ingested without loss and without re-reading.
//
// Returns processed=true only when it emitted at least one record, so the poll summary's
// blob count excludes no-ops (an empty delta, or a quarantined wrong-extension blob).
func (r *pollingReceiver) processBlobIncremental(ctx context.Context, blob *azureblob.BlobInfo) (processed bool, err error) {
	// JSON formats expect .json; quarantine anything else (ignored while unchanged) instead of
	// erroring every poll. Text is content-agnostic.
	if r.cfg.BlobFormat == BlobFormatRecordsJSON || r.cfg.BlobFormat == BlobFormatJSON {
		if ext := strings.ToLower(filepath.Ext(blob.Name)); ext != ".json" {
			r.quarantineBlob(blob.Name, blob.LastModified, func() {
				r.logger.Warn("Ignoring unsupported file type on the incremental path", zap.String("blob", blob.Name), zap.String("ext", ext))
			})
			return false, nil
		}
	}

	r.mut.Lock()
	prog, _ := r.checkpoint.ProgressFor(blob.Name)
	r.mut.Unlock()
	offset := prog.Offset

	// The head and delta are two unconditioned reads (no ETag), so a blob replaced between them
	// re-reads from 0 next poll (at-least-once), not lost. A nonzero offset with no stored
	// fingerprint (a seal-time reset+quarantine dropped it) has nothing to verify, so — like
	// flushSealedBlob — it resumes at the offset rather than resetting and re-emitting the prefix.
	fingerprint := prog.Fingerprint
	head, needsReset, err := r.verifyResumeIdentity(ctx, blob.Name, offset, fingerprint)
	if err != nil {
		if azureblob.IsBlobNotFound(err) {
			return false, nil // blob gone between listing and read; let it age out
		}
		if !azureblob.IsRangeNotSatisfiable(err) {
			if r.quarantineOnPermanentReadError(blob, err) {
				return false, nil
			}
			return false, fmt.Errorf("fingerprint read: %w", err)
		}
		// A 416 on a read starting at byte 0 means the blob is now empty
		// (truncated/emptied/replaced): its empty head won't match the stored
		// fingerprint, so restart from byte 0.
		needsReset = true
	}
	if needsReset {
		r.logger.Debug("Stored fingerprint no longer matches; re-reading the blob from the start (duplicate delivery of already-emitted records is expected)",
			zap.String("blob", blob.Name))
		offset = 0
	}

	delta, _, err := r.azureClient.DownloadBlobRange(ctx, r.cfg.Container, blob.Name, offset, 0)
	if err != nil {
		if azureblob.IsBlobNotFound(err) {
			return false, nil // blob gone; let it age out
		}
		if !azureblob.IsRangeNotSatisfiable(err) {
			if r.quarantineOnPermanentReadError(blob, err) {
				return false, nil
			}
			wrapped := fmt.Errorf("download blob range: %w", err)
			if offset == 0 {
				r.trackNeverReadFailure(blob, wrapped)
			}
			return false, wrapped
		}
		// Offset at/past the blob end: no new bytes. Fall through with an empty delta
		// so LastModified still advances and the mtime gate stops re-firing this read.
		delta = nil
	}

	bytesConsumed, emitted, err := r.consumeDelta(ctx, delta, offset == 0)
	if err != nil {
		if offset == 0 {
			r.trackNeverReadFailure(blob, err)
		}
		return false, err
	}

	// A records-json read that consumed nothing is either still growing or permanently stuck (a
	// bad envelope, or a non-object element the array can never advance past). The stuck case would
	// otherwise re-read silently until seal, losing every record after it, so quarantine it now
	// (warned once) instead. Emit-before-then-quarantine matches the documented leniency; this also
	// covers the continuation delta, where the offset is parked just before the non-object element.
	if r.cfg.BlobFormat == BlobFormatRecordsJSON && bytesConsumed == 0 &&
		blobconsume.RecordsJSONDeltaStuck(delta, offset == 0) {
		r.quarantineBlob(blob.Name, blob.LastModified, func() {
			r.logger.Warn("records-json blob cannot be parsed (bad envelope or a non-object element); ignoring it while unchanged (check blob_format)",
				zap.String("blob", blob.Name))
		})
		return false, nil
	}

	newOffset := offset + int64(bytesConsumed)

	// Fingerprint only the committed prefix [0,newOffset): never the tail the writer
	// still rewrites (records-json footer / partial line), or a small blob's fingerprint
	// would flip on every append and force a spurious re-read.
	switch {
	case offset == 0:
		fingerprint = blobconsume.Fingerprint(delta[:bytesConsumed], r.fpSize())
	case len(fingerprint) < r.fpSize() && len(head) > 0:
		// Grow from the committed prefix head[:offset]+delta[:bytesConsumed] (not head[:newOffset],
		// whose tail may be a stale footer); fall back to head when offset outruns the readable head.
		committed := head
		if offset <= int64(len(head)) {
			committed = append(append([]byte{}, head[:offset]...), delta[:bytesConsumed]...)
		}
		fingerprint = blobconsume.Fingerprint(committed, r.fpSize())
	}

	r.saveProgress(blob.Name, BlobProgress{
		Offset:       newOffset,
		Fingerprint:  fingerprint,
		LastModified: blob.LastModified,
	})
	return emitted, nil
}

// errBlobDownloadTransient wraps a blob download failure so processBlobWholeTracked can
// tell a retryable network error (leave untracked so a later poll retries) apart from a
// permanent content error (quarantine, or it re-errors every poll forever).
var errBlobDownloadTransient = errors.New("blob download failed")

// processBlobWholeTracked reads a whole gzip blob (gzip can't be range-tailed) in an
// append-growable config and records it consumed so the mtime gate skips it, re-reading only
// when LastModified changes. Reports whether records were emitted.
func (r *pollingReceiver) processBlobWholeTracked(ctx context.Context, blob *azureblob.BlobInfo) (bool, error) {
	if err := r.processBlob(ctx, blob); err != nil {
		// Transient (download/downstream): if the blob was never read successfully, track it as
		// never-read so its drop is logged at age-out (the LastReadError bypass keeps retrying a
		// static-mtime gzip). Don't clobber a prior successful entry (Offset>0): a rewritten blob
		// that now fails keeps that entry and retries via its changed mtime.
		if errors.Is(err, errBlobDownloadTransient) || errors.Is(err, blobconsume.ErrDownstream) {
			r.mut.Lock()
			prog, existed := r.checkpoint.ProgressFor(blob.Name)
			r.mut.Unlock()
			if !existed || prog.Offset == 0 {
				r.trackNeverReadFailure(blob, err)
			}
			return false, err
		}
		// A permanent content error would re-error every poll; quarantine it (tracked, ignored
		// while unchanged, revived by a later LastModified).
		r.quarantineBlob(blob.Name, blob.LastModified, func() {
			r.logger.Warn("Quarantining blob that could not be processed; ignoring it while unchanged",
				zap.String("blob", blob.Name), zap.Error(err))
		})
		return false, nil
	}
	// Offset is only a nonzero "whole blob consumed" marker for a gzip blob: gzip is never
	// range-tailed, so this value never drives a range read. It uses the listed size (not the
	// actual decompressed/streamed length), which is fine for that marker purpose.
	r.saveProgress(blob.Name, BlobProgress{Offset: blob.Size, LastModified: blob.LastModified})
	return true, nil
}

// processBlob does the following:
// 1. Downloads the blob
// 2. Decompresses the blob if applicable
// 3. Pass the blob to the consumer
func (r *pollingReceiver) processBlob(ctx context.Context, blob *azureblob.BlobInfo) error {
	var blobBuffer []byte
	var err error
	// otlp blobs are written once, so their listed size is accurate: use the SDK's
	// parallel, exactly-sized buffered download. The append-growable formats
	// (json/text/records-json) are grown in place, so their listed size is stale and
	// would overflow a pre-sized buffer; stream those instead.
	if r.cfg.BlobFormat == BlobFormatOTLP || r.cfg.BlobFormat == "" {
		buf := make([]byte, blob.Size)
		n, derr := r.azureClient.DownloadBlob(ctx, r.cfg.Container, blob.Name, buf)
		if derr != nil {
			// Plain error: otlp is not tracked, so nothing classifies it (the append-growable
			// branch below wraps errBlobDownloadTransient, which processBlobWholeTracked keys on).
			return fmt.Errorf("download: %w", derr)
		}
		blobBuffer = buf[:n]
	} else {
		blobBuffer, err = r.azureClient.DownloadBlobStream(ctx, r.cfg.Container, blob.Name)
		if err != nil {
			// A permanent read failure (auth/config, e.g. 403) re-errors every poll, so return it
			// unwrapped and let processBlobWholeTracked quarantine it, as the incremental path does.
			if azureblob.IsPermanentError(err) {
				return fmt.Errorf("download: %w", err)
			}
			return fmt.Errorf("%w: %w", errBlobDownloadTransient, err)
		}
	}

	// Check file extension to see if we need to decompress. Compare case-insensitively so an
	// uppercase .GZ is decompressed rather than read as raw compressed bytes.
	switch ext := strings.ToLower(filepath.Ext(blob.Name)); ext {
	case ".gz":
		blobBuffer, err = blobconsume.GzipDecompress(blobBuffer)
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
	case ".json":
		// Uncompressed; nothing to decompress.
	default:
		return fmt.Errorf("unsupported file type: %s", ext)
	}

	if err := r.consumer.Consume(ctx, blobBuffer); err != nil {
		return fmt.Errorf("consume: %w", err)
	}
	return nil
}

// checkpointStorageKey the key used for storing the checkpoint
const checkpointStorageKey = "azure_blob_polling_checkpoint"

// checkpointKey returns the key used for storing the checkpoint
func (r *pollingReceiver) checkpointKey() string {
	return fmt.Sprintf("%s_%s_%s", checkpointStorageKey, r.id, r.supportedTelemetry.String())
}

func (r *pollingReceiver) makeCheckpoint(ctx context.Context) error {
	r.mut.Lock()
	defer r.mut.Unlock()
	// Shutdown can run without a successful Start (this receiver's Start erroring, or another
	// component's failure shutting down the whole graph), leaving r.checkpoint nil: nothing was
	// loaded, so there is nothing to save.
	if r.checkpoint == nil {
		return nil
	}
	// The legacy (non-incremental) path persists only when a blob advanced the
	// name/time watermark, exactly as it did before incremental reads existed, so
	// flags-off restart recovery is unchanged (an empty poll does not persist an
	// advanced LastPollTime). The incremental path persists every poll so the
	// per-blob progress map and the revisit anchor survive restarts.
	if !r.isIncrementalFormat() && (r.lastBlob == nil || r.lastBlobTime == nil) {
		return nil
	}
	// Advance the name/time watermark when a blob set it (never on the append-growable path).
	if r.lastBlob != nil && r.lastBlobTime != nil {
		r.checkpoint.UpdateCheckpoint(*r.lastBlobTime, r.lastBlob.Name)
	}
	// The incremental path saves every poll, so log at Debug to avoid a line per interval;
	// the legacy path saves only on a watermark advance, so keep it at Info.
	logCheckpointSaved := r.logger.Info
	if r.isIncrementalFormat() {
		logCheckpointSaved = r.logger.Debug
	}
	// Safe to hold r.mut across this write: no blob goroutine is live during finalizePoll/Shutdown.
	if err := r.checkpointStore.SaveStorageData(ctx, r.checkpointKey(), r.checkpoint); err != nil {
		return err
	}
	// Log only after a successful write, so "Checkpoint saved" never precedes a failed save.
	logCheckpointSaved("Checkpoint saved",
		zap.Time("last_ts", r.checkpoint.LastTs),
		zap.Int("parsed_entities_count", len(r.checkpoint.ParsedEntities)),
		zap.Int("tracked_blobs", len(r.checkpoint.Progress)),
		zap.Time("last_poll_time", r.checkpoint.LastPollTime))
	return nil
}

// finalizePoll records the poll time, retires blobs that have aged out of the
// revisit horizon (final tail flush and delete-on-read), and persists the
// checkpoint.
func (r *pollingReceiver) finalizePoll(ctx context.Context, startingTime, endingTime time.Time) {
	r.mut.Lock()
	r.checkpoint.UpdatePollTime(endingTime)
	// pruneAfter = max(quarantine retention, revisit window): a window set beyond the retention
	// must still not prune an entry while its blob is listable (see AgedOutProgress for why).
	pruneAfter := quarantineRetention
	if r.revisitWindow > pruneAfter {
		pruneAfter = r.revisitWindow
	}
	quarantinePruneCutoff := endingTime.Add(-pruneAfter)
	// Path-time mode prunes a sealed blob at the revisit window (past it the blob can't be re-listed,
	// so the entry is dead weight); use_last_modified keeps the longer retention, since a changed
	// mtime can re-list the blob.
	sealedPruneCutoff := quarantinePruneCutoff
	if !r.cfg.UseLastModified {
		sealedPruneCutoff = endingTime.Add(-r.revisitWindow)
	}
	aged, giveUp := r.checkpoint.AgedOutProgress(startingTime.Add(-sealSkewTolerance), quarantinePruneCutoff, sealedPruneCutoff)
	r.mut.Unlock()

	// AgedOutProgress can't log; log its budget-exhausted drops here so a drop is never silent.
	for name, prog := range giveUp {
		r.logger.Warn("Dropping sealed blob after exhausting finalize retries; a trailing append may not have been ingested",
			zap.String("blob", name), zap.Int("failures", prog.FinalizeFailures))
	}

	r.finalizeAgedBlobs(ctx, aged)

	if err := r.makeCheckpoint(ctx); err != nil {
		r.logger.Error("Error saving checkpoint after poll", zap.Error(err))
	}
}

// finalizeAgedBlobs runs the final per-blob work for blobs that have aged out of
// the revisit horizon: flush any unterminated tail (line formats), then delete the
// blob if delete_on_read is set. The blob is sealed by this point, so deleting it
// cannot drop live appends.
func (r *pollingReceiver) finalizeAgedBlobs(ctx context.Context, aged map[string]BlobProgress) {
	// Seal/delete belongs to the incremental path. With the opt-in off, aged entries
	// are stale checkpoint data (already pruned by AgedOutProgress): never delete a
	// blob a legacy config did not read incrementally.
	if !r.isIncrementalFormat() {
		return
	}
	for name, prog := range aged {
		// A never-read blob (offset 0, only failures): log the drop and reason so it is not
		// dropped silently, and skip the flush/delete (nothing was read; don't delete unread data).
		if prog.LastReadError != "" && prog.Offset == 0 {
			r.logger.Warn("Dropping blob that aged out without a successful read; its records were not ingested",
				zap.String("blob", name), zap.String("last_error", prog.LastReadError))
			continue
		}
		// Re-read whatever remains past the stored offset one final time before
		// retiring the blob. This recovers a final append the mtime gate could not
		// re-trigger, namely a last write that lands in the same clock second as the
		// read that reached the prior offset (Azure LastModified is second-grained).
		// Gzip blobs are excluded (useIncremental is false for them): they are read
		// whole and never range-tailed, so a range read would only return compressed
		// bytes past the stored size.
		if r.useIncremental(name) {
			newOffset, newFingerprint, err := r.flushSealedBlob(ctx, name, prog)
			// Persist the offset past the bytes the flush just emitted so no retry, delete, or
			// resume re-emits them, and the fingerprint it returns: unchanged on a normal resume,
			// or re-established from the replacement's leading bytes when the blob was replaced
			// (never nil at a nonzero offset, so replacement detection keeps working).
			prog.Offset = newOffset
			prog.Fingerprint = newFingerprint
			if errors.Is(err, errSealedBlobUnparseable) {
				// The residue past the last record didn't parse. Keep the blob (skip the delete)
				// and quarantine its entry so it is ignored until a later LastModified revives it.
				r.logger.Error("Sealed blob content did not parse; keeping blob and ignoring it while unchanged", zap.String("blob", name))
				prog.Quarantined = true
				r.saveProgress(name, prog)
				continue
			}
			if err != nil {
				// Re-track for a later retry and skip the delete.
				r.retrackOrQuarantine(name, prog, "flush", err)
				continue
			}
		}
		if r.cfg.DeleteOnRead {
			// A not-found error means the blob is already gone, so the entry (dropped by
			// AgedOutProgress) stays dropped. Any other error means the blob still exists:
			// re-track for a later delete retry, or quarantine once it keeps failing, rather
			// than leaking it or retrying forever.
			if err := r.azureClient.DeleteBlob(ctx, r.cfg.Container, name); err != nil && !azureblob.IsBlobNotFound(err) {
				r.retrackOrQuarantine(name, prog, "delete", err)
			}
			continue
		}
		// Not deleting: keep the aged entry (marked Sealed) so the mtime gate skips it rather
		// than re-reading it every poll while its path-time keeps it listed. An incremental blob
		// keeps its final flushed offset so a resume reads only a later append; a gzip blob (read
		// whole, not flushed) keeps its whole-consumed offset. AgedOutProgress no longer
		// re-flushes a Sealed entry, and it prunes at the horizon.
		prog.Sealed = true
		r.saveProgress(name, prog)
	}
}

// retrackOrQuarantine re-tracks prog for a later finalize retry. Every real failure increments
// FinalizeFailures so AgedOutProgress can prune a persistently-failing entry; a failure that can
// never self-resolve also quarantines after maxFinalizeFailures to stop retrying every poll.
func (r *pollingReceiver) retrackOrQuarantine(name string, prog BlobProgress, phase string, err error) {
	// A shutdown cancellation is an interruption, not a blob failure: re-track without counting.
	if errors.Is(err, context.Canceled) {
		r.saveProgress(name, prog)
		return
	}
	prog.FinalizeFailures++
	// A delete failure (tail already emitted) and a permanent error can't self-resolve, so they
	// quarantine; a transient flush failure only re-tracks so a still-readable tail isn't stranded.
	quarantinable := phase == "delete" || azureblob.IsPermanentError(err)
	if quarantinable && prog.FinalizeFailures >= maxFinalizeFailures {
		prog.Quarantined = true
		r.logger.Error("Sealed blob repeatedly failed to "+phase+"; quarantining it until it changes",
			zap.String("blob", name), zap.Int("failures", prog.FinalizeFailures), zap.Error(err))
	} else {
		r.logger.Error("Error during sealed blob "+phase+"; will retry", zap.String("blob", name), zap.Error(err))
	}
	r.saveProgress(name, prog)
}

// flushSealedBlob emits whatever remains past the stored offset of a now-sealed
// append-growable blob so a final append is never stranded, and returns the offset reads
// should resume from (old offset plus bytes emitted, or 0 on a replaced blob) plus the
// fingerprint to store (unchanged, or re-established from the replacement) so a failed delete
// retry does not re-emit the tail. A read at/past the end (invalid range) or of a vanished blob
// (not found) means nothing to flush, not an error, so the aged-blob retry path doesn't loop
// on it forever.
func (r *pollingReceiver) flushSealedBlob(ctx context.Context, name string, prog BlobProgress) (int64, []byte, error) {
	offset := prog.Offset
	fingerprint := prog.Fingerprint
	// Verify identity before reading the tail, the same guard processBlobIncremental uses on
	// resume: a blob replaced since we last tracked it (its path-time already left the listing
	// window, so the replacement was never observed) no longer matches the stored fingerprint,
	// and reading its stale offset would emit mid-file bytes as records. On a mismatch, re-read
	// the replacement from byte 0. An absent fingerprint (a seal-time reset dropped it) or
	// assume_append_only skips the identity read and resumes at the stored offset.
	head, needsReset, err := r.verifyResumeIdentity(ctx, name, offset, fingerprint)
	if err != nil {
		if azureblob.IsRangeNotSatisfiable(err) || azureblob.IsBlobNotFound(err) {
			return offset, fingerprint, nil // now empty (416 on the byte-0 head read), or gone: nothing to flush
		}
		return offset, fingerprint, fmt.Errorf("fingerprint read: %w", err)
	}
	if needsReset {
		r.logger.Debug("Sealed blob's stored fingerprint no longer matches; re-reading the replacement from the start",
			zap.String("blob", name))
		offset = 0
		// Re-establish the identity fingerprint from the replacement's leading bytes so a
		// later replacement of the same name is still detected. Dropping it to nil would
		// leave the entry unverifiable forever; processBlobIncremental normalizes this to the
		// committed prefix on its next read of a still-growing blob.
		fingerprint = blobconsume.Fingerprint(head, r.fpSize())
	}

	tail, _, err := r.azureClient.DownloadBlobRange(ctx, r.cfg.Container, name, offset, 0)
	if err != nil {
		if azureblob.IsRangeNotSatisfiable(err) {
			return offset, fingerprint, nil // offset already at end: nothing left to flush
		}
		if azureblob.IsBlobNotFound(err) {
			return offset, fingerprint, nil // blob already gone: nothing left to flush
		}
		return offset, fingerprint, fmt.Errorf("download tail: %w", err)
	}
	if len(bytes.TrimSpace(tail)) == 0 {
		return offset, fingerprint, nil
	}
	// records-json reframes the remaining complete records; json validates its final line;
	// text emits the whole tail. A consume failure returns a plain (non-permanent) error, so
	// retrackOrQuarantine retries it rather than counting it toward quarantine.
	if r.cfg.BlobFormat == BlobFormatRecordsJSON {
		bytesConsumed, _, err := r.consumeDelta(ctx, tail, offset == 0)
		if err != nil {
			return offset, fingerprint, fmt.Errorf("seal consume: %w", err)
		}
		// The bytes past the last consumed record must be a clean seal (the array close and
		// any envelope trailer) before we delete the blob. Validate that residue in every
		// case, not just when nothing was consumed: garbage, a partial record, or complete records
		// stranded after a non-object element would otherwise be deleted under
		// delete_on_read. Anything unclean is quarantined and kept instead. Return the advanced
		// offset even here: consumeDelta already emitted the records before the residue, so the
		// caller must persist past them or a later resume would re-emit that prefix.
		if !validSealTail(tail[bytesConsumed:], offset == 0 && bytesConsumed == 0) {
			return offset + int64(bytesConsumed), fingerprint, errSealedBlobUnparseable
		}
		return offset + int64(bytesConsumed), fingerprint, nil
	}
	if r.cfg.BlobFormat == BlobFormatJSON {
		// Emit the complete lines; validate the trailing newline-less line. A truncated final
		// record (a writer that crashed mid-append) is kept and quarantined, not silently
		// dropped and deleted like the legacy path, so a later completed write is re-read.
		complete, bytesConsumed := blobconsume.SplitLineDelta(tail)
		if bytesConsumed > 0 {
			if err := r.consumeContent(ctx, complete); err != nil {
				return offset, fingerprint, fmt.Errorf("seal consume: %w", err)
			}
		}
		trailing := bytes.TrimSpace(tail[bytesConsumed:])
		if len(trailing) == 0 {
			return offset + int64(bytesConsumed), fingerprint, nil
		}
		// The NDJSON consumer requires a JSON object per line, so gate on that (not merely
		// json.Valid): a valid-but-non-object final line (e.g. 42 or ["a"]) is unparseable as a
		// record and is kept+quarantined rather than dropped-and-deleted.
		if !blobconsume.IsJSONObject(trailing) {
			return offset + int64(bytesConsumed), fingerprint, errSealedBlobUnparseable
		}
		if err := r.consumeContent(ctx, trailing); err != nil {
			return offset + int64(bytesConsumed), fingerprint, fmt.Errorf("seal consume: %w", err)
		}
		return offset + int64(len(tail)), fingerprint, nil
	}
	// text has no parse-failure mode, so the whole tail is emitted.
	if err := r.consumeContent(ctx, tail); err != nil {
		return offset, fingerprint, fmt.Errorf("seal consume: %w", err)
	}
	return offset + int64(len(tail)), fingerprint, nil
}

// errSealedBlobUnparseable signals that a sealed blob's tail held content that could not be
// parsed (a records-json residue that is not a clean seal, or a truncated final json line),
// so it must be kept rather than deleted.
var errSealedBlobUnparseable = errors.New("sealed blob content did not parse")

// validSealTail reports whether the tail past the sealed offset of a records-json blob
// is well-formed. At blob start (atStart) the tail is a whole document. On a resumed
// blob the tail begins just after the last consumed record — at the array close and any
// envelope trailer — so it is validated by reconstructing the enclosing document
// ({"records":[ prepended). A clean seal (array close, optional trailer) reconstructs to
// valid JSON; a corrupted or truncated tail does not.
func validSealTail(tail []byte, atStart bool) bool {
	if atStart {
		return blobconsume.ValidRecordsJSONDoc(tail)
	}
	return blobconsume.ValidRecordsJSONDoc(append([]byte(`{"records":[`), tail...))
}

func (r *pollingReceiver) conditionallyDeleteBlob(ctx context.Context, blob *azureblob.BlobInfo) error {
	if !r.cfg.DeleteOnRead {
		return nil
	}
	return r.azureClient.DeleteBlob(ctx, r.cfg.Container, blob.Name)
}

// trimMatchedRoot returns blobName with the longest matching root prefix from
// r.expandedRoots removed (along with one leading "/"), and a bool indicating
// whether a root was trimmed. If no root matches, blobName is returned unchanged
// and the bool is false.
func (r *pollingReceiver) trimMatchedRoot(blobName string) (string, bool) {
	var matched string
	for _, root := range r.expandedRoots {
		if root == "" {
			continue
		}
		if strings.HasPrefix(blobName, root) && len(root) > len(matched) {
			matched = root
		}
	}
	if matched == "" {
		return blobName, false
	}
	trimmed := strings.TrimPrefix(blobName, matched)
	return strings.TrimPrefix(trimmed, "/"), true
}
