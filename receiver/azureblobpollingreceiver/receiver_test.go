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
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/observiq/bindplane-otel-contrib/internal/azureblob"
	"github.com/observiq/bindplane-otel-contrib/internal/blobconsume"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pipeline"
	"go.uber.org/goleak"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// procErr drops processBlobIncremental's processed-bool return so a test that only cares about
// the error can wrap the two-value call in require.NoError.
func procErr(_ bool, err error) error { return err }

func TestIncrementalHorizonStart(t *testing.T) {
	base := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
	window := 2 * time.Hour
	t.Run("anchors to lastPollTime when it is in the past", func(t *testing.T) {
		got := incrementalHorizonStart(base, base, base.Add(time.Minute), window)
		require.Equal(t, base.Add(-window), got)
	})
	t.Run("clock stepped back: anchors to now, not the future lastPollTime", func(t *testing.T) {
		now, last := base, base.Add(time.Hour)
		require.Equal(t, now.Add(-window), incrementalHorizonStart(last, last, now, window))
	})
	t.Run("first poll (zero lastPollTime) anchors to now", func(t *testing.T) {
		require.Equal(t, base.Add(-window), incrementalHorizonStart(base.Add(-5*time.Minute), time.Time{}, base, window))
	})
	t.Run("no widening when startingTime is already earlier", func(t *testing.T) {
		start := base.Add(-3 * time.Hour)
		require.Equal(t, start, incrementalHorizonStart(start, base, base.Add(time.Minute), window))
	})
}

func TestPollingReceiver_runPoll(t *testing.T) {
	t.Run("First poll uses initial lookback", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:       "test-container",
			PollInterval:    1 * time.Minute,
			InitialLookback: 5 * time.Minute,
		}

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		// Verify that checkpoint has zero LastPollTime before first poll
		require.True(t, receiver.checkpoint.LastPollTime.IsZero())

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Mock StreamBlobs to immediately send done signal
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				doneChan := args.Get(5).(chan struct{})
				close(doneChan)
			})

		receiver.runPoll(ctx)

		// Verify checkpoint was updated with poll time after first poll
		require.False(t, receiver.checkpoint.LastPollTime.IsZero())

		mockClient.AssertExpectations(t)
	})

	t.Run("Subsequent polls use last poll time", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:       "test-container",
			PollInterval:    1 * time.Minute,
			InitialLookback: 5 * time.Minute,
		}

		mockClient := new(azureblob.MockBlobClient)
		checkpoint := NewPollingCheckpoint()
		lastPollTime := time.Now().UTC().Add(-2 * time.Minute)
		checkpoint.UpdatePollTime(lastPollTime)

		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      checkpoint,
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		// Verify checkpoint has the previous poll time
		require.Equal(t, lastPollTime, receiver.checkpoint.LastPollTime)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Mock StreamBlobs to immediately send done signal
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				doneChan := args.Get(5).(chan struct{})
				close(doneChan)
			})

		receiver.runPoll(ctx)

		// Verify checkpoint was updated with new poll time
		require.True(t, receiver.checkpoint.LastPollTime.After(lastPollTime))

		mockClient.AssertExpectations(t)
	})

	t.Run("Updates checkpoint after processing blobs", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:       "test-container",
			PollInterval:    1 * time.Minute,
			InitialLookback: 5 * time.Minute,
		}

		mockClient := new(azureblob.MockBlobClient)
		checkpoint := NewPollingCheckpoint()

		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      checkpoint,
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		now := time.Now().UTC()

		// Mock StreamBlobs to send some test blobs
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				blobChan := args.Get(4).(chan []*azureblob.BlobInfo)
				doneChan := args.Get(5).(chan struct{})

				// Send a batch of blobs
				blobs := []*azureblob.BlobInfo{
					{
						Name:         "year=2024/month=03/day=15/hour=14/test1.json",
						Size:         100,
						LastModified: now.Add(-1 * time.Hour),
					},
					{
						Name:         "year=2024/month=03/day=15/hour=15/test2.json",
						Size:         200,
						LastModified: now.Add(-30 * time.Minute),
					},
				}
				blobChan <- blobs
				close(doneChan)
			})

		beforePoll := receiver.checkpoint.LastPollTime
		receiver.runPoll(ctx)

		// Verify checkpoint poll time was updated
		require.True(t, receiver.checkpoint.LastPollTime.After(beforePoll))

		mockClient.AssertExpectations(t)
	})

	t.Run("Handles context cancellation", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:       "test-container",
			PollInterval:    1 * time.Minute,
			InitialLookback: 5 * time.Minute,
		}

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Mock StreamBlobs to detect when called, then return from doneChan
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				doneChan := args.Get(5).(chan struct{})
				close(doneChan)
			})

		receiver.runPoll(ctx)

		mockClient.AssertExpectations(t)
	})

	t.Run("Handles error during poll", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:       "test-container",
			PollInterval:    1 * time.Minute,
			InitialLookback: 5 * time.Minute,
		}

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Mock StreamBlobs to send an error
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				errChan := args.Get(3).(chan error)
				errChan <- context.Canceled
			})

		receiver.runPoll(ctx)

		mockClient.AssertExpectations(t)
	})

	t.Run("Does not leak the producer goroutine when a poll errors", func(t *testing.T) {
		// StreamBlobs signals a poll error by sending to errChan and returning without closing
		// its done channel (the real client does exactly this on a pager error). processBlobsLoop
		// then stops draining, so the pullBlobs producer would block forever on the prefix-done
		// wait unless the poll cancels its own scope on return. The context here is never
		// cancelled by the test, so only that self-cancellation can free the goroutine. Regression
		// guard for a pre-existing leak: one stranded goroutine per errored poll until Shutdown.
		defer goleak.VerifyNone(t)

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          zap.NewNop(),
			cfg:             &Config{Container: "test-container", PollInterval: time.Minute, InitialLookback: 5 * time.Minute},
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			pollInterval:    time.Minute,
			initialLookback: 5 * time.Minute,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				errChan := args.Get(3).(chan error)
				errChan <- errors.New("pager boom") // no close(doneChan): mirrors the real error path
			})

		receiver.runPoll(context.Background())

		mockClient.AssertExpectations(t)
	})
}

func TestPollingReceiver_GlobExpansion(t *testing.T) {
	t.Run("Glob root_folder expands to matched directories", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:              "test-container",
			RootFolder:             "linux/*",
			PollInterval:           1 * time.Minute,
			InitialLookback:        5 * time.Minute,
			UseTimePatternAsPrefix: true,
			TimePattern:            "{year}/{month}/{day}/{hour}",
		}

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		ctx := context.Background()

		// Mock ListPrefixes to return subdirectories
		mockClient.On("ListPrefixes", mock.Anything, "test-container", "linux/").
			Return([]string{"linux/auditd", "linux/logb", "linux/logc"}, nil)

		// Mock StreamBlobs for each matched directory's time prefixes
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				doneChan := args.Get(5).(chan struct{})
				close(doneChan)
			})

		receiver.runPoll(ctx)

		mockClient.AssertCalled(t, "ListPrefixes", mock.Anything, "test-container", "linux/")
		mockClient.AssertExpectations(t)
	})

	t.Run("No glob skips ListPrefixes API call", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:       "test-container",
			RootFolder:      "linux/auditd",
			PollInterval:    1 * time.Minute,
			InitialLookback: 5 * time.Minute,
		}

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		ctx := context.Background()

		// Only StreamBlobs should be called, NOT ListPrefixes
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				doneChan := args.Get(5).(chan struct{})
				close(doneChan)
			})

		receiver.runPoll(ctx)

		mockClient.AssertNotCalled(t, "ListPrefixes", mock.Anything, mock.Anything, mock.Anything)
		mockClient.AssertExpectations(t)
	})

	t.Run("Glob with zero matches scans nothing", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:       "test-container",
			RootFolder:      "nonexistent/*",
			PollInterval:    1 * time.Minute,
			InitialLookback: 5 * time.Minute,
		}

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		ctx := context.Background()

		// ListPrefixes returns empty — no directories match
		mockClient.On("ListPrefixes", mock.Anything, "test-container", "nonexistent/").
			Return([]string{}, nil)

		receiver.runPoll(ctx)

		// StreamBlobs should NOT be called — zero matches means scan nothing
		mockClient.AssertCalled(t, "ListPrefixes", mock.Anything, "test-container", "nonexistent/")
		mockClient.AssertNotCalled(t, "StreamBlobs", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		mockClient.AssertExpectations(t)
	})

	t.Run("ListPrefixes error with fallback_on_glob_failure=true falls back to static prefix", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:             "test-container",
			RootFolder:            "linux/*",
			PollInterval:          1 * time.Minute,
			InitialLookback:       5 * time.Minute,
			FallbackOnGlobFailure: true,
		}

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		ctx := context.Background()

		// ListPrefixes returns error
		mockClient.On("ListPrefixes", mock.Anything, "test-container", "linux/").
			Return([]string(nil), errors.New("network error"))

		// StreamBlobs should be called with the static prefix as fallback
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				prefix := args.Get(2).(*string)
				require.NotNil(t, prefix)
				require.Equal(t, "linux/", *prefix)
				doneChan := args.Get(5).(chan struct{})
				close(doneChan)
			})

		receiver.runPoll(ctx)

		mockClient.AssertExpectations(t)
	})

	t.Run("ListPrefixes error with default (no fallback) scans nothing", func(t *testing.T) {
		logger := zap.NewNop()
		cfg := &Config{
			Container:       "test-container",
			RootFolder:      "linux/*",
			PollInterval:    1 * time.Minute,
			InitialLookback: 5 * time.Minute,
			// FallbackOnGlobFailure is intentionally left at default (false)
		}

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		ctx := context.Background()

		mockClient.On("ListPrefixes", mock.Anything, "test-container", "linux/").
			Return([]string(nil), errors.New("network error"))

		receiver.runPoll(ctx)

		// StreamBlobs must NOT be called — without opt-in, listing nothing is
		// safer than potentially scanning the entire container.
		mockClient.AssertCalled(t, "ListPrefixes", mock.Anything, "test-container", "linux/")
		mockClient.AssertNotCalled(t, "StreamBlobs", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		mockClient.AssertExpectations(t)
	})

	t.Run("ListPrefixes failure does not finalize the poll (no checkpoint advance)", func(t *testing.T) {
		// A sustained glob-expansion failure (fallback off) must not look like a legitimately
		// empty poll. Finalizing here advances LastPollTime and lets AgedOutProgress seal and
		// delete still-growing blobs under delete_on_read (data loss). The failure must route
		// through errChan and return before finalizePoll, the same as a mid-poll listing error.
		logger := zap.NewNop()
		cfg := &Config{
			Container:             "test-container",
			RootFolder:            "linux/*",
			PollInterval:          1 * time.Minute,
			InitialLookback:       5 * time.Minute,
			BlobFormat:            BlobFormatJSON,
			EnableIncrementalRead: true,
			DeleteOnRead:          true,
			// FallbackOnGlobFailure left at default (false)
		}

		mockClient := new(azureblob.MockBlobClient)
		receiver := &pollingReceiver{
			logger:          logger,
			cfg:             cfg,
			azureClient:     mockClient,
			checkpoint:      NewPollingCheckpoint(),
			checkpointStore: storageclient.NewNopStorage(),
			pollInterval:    cfg.PollInterval,
			initialLookback: cfg.InitialLookback,
			revisitWindow:   defaultIncrementalRevisitWindow,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}

		mockClient.On("ListPrefixes", mock.Anything, "test-container", "linux/").
			Return([]string(nil), errors.New("network error"))

		receiver.runPoll(context.Background())

		require.True(t, receiver.checkpoint.LastPollTime.IsZero(),
			"a glob-expansion failure must not advance the checkpoint watermark (finalizePoll must be skipped)")
		mockClient.AssertNotCalled(t, "StreamBlobs", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})
}

func TestPollingReceiver_InitialLookback(t *testing.T) {
	t.Run("Uses InitialLookback when configured", func(t *testing.T) {
		cfg := &Config{
			ConnectionString: "DefaultEndpointsProtocol=https;AccountName=test;AccountKey=dGVzdA==;EndpointSuffix=core.windows.net",
			Container:        "test-container",
			PollInterval:     1 * time.Minute,
			InitialLookback:  10 * time.Minute,
			BatchSize:        100,
			PageSize:         1000,
		}

		// Override the newAzureBlobClient function for testing
		originalNewAzureBlobClient := newAzureBlobClient
		defer func() { newAzureBlobClient = originalNewAzureBlobClient }()

		mockClient := new(azureblob.MockBlobClient)
		newAzureBlobClient = func(_ string, _ int, _ int, _ *zap.Logger) (azureblob.BlobClient, error) {
			return mockClient, nil
		}

		receiver, err := newLogsReceiver(
			component.MustNewID("azureblobpolling"),
			zap.NewNop(),
			cfg,
			consumertest.NewNop(),
		)
		require.NoError(t, err)
		require.NotNil(t, receiver)
		require.Equal(t, 10*time.Minute, receiver.initialLookback)
	})

	t.Run("Defaults to PollInterval when InitialLookback not set", func(t *testing.T) {
		cfg := &Config{
			ConnectionString: "DefaultEndpointsProtocol=https;AccountName=test;AccountKey=dGVzdA==;EndpointSuffix=core.windows.net",
			Container:        "test-container",
			PollInterval:     1 * time.Minute,
			InitialLookback:  0, // Not set
			BatchSize:        100,
			PageSize:         1000,
		}

		// Override the newAzureBlobClient function for testing
		originalNewAzureBlobClient := newAzureBlobClient
		defer func() { newAzureBlobClient = originalNewAzureBlobClient }()

		mockClient := new(azureblob.MockBlobClient)
		newAzureBlobClient = func(_ string, _ int, _ int, _ *zap.Logger) (azureblob.BlobClient, error) {
			return mockClient, nil
		}

		receiver, err := newLogsReceiver(
			component.MustNewID("azureblobpolling"),
			zap.NewNop(),
			cfg,
			consumertest.NewNop(),
		)
		require.NoError(t, err)
		require.NotNil(t, receiver)
		require.Equal(t, cfg.PollInterval, receiver.initialLookback)
	})
}

func TestNewLogsReceiver_BlobFormat(t *testing.T) {
	originalNewAzureBlobClient := newAzureBlobClient
	defer func() { newAzureBlobClient = originalNewAzureBlobClient }()

	mockClient := new(azureblob.MockBlobClient)
	newAzureBlobClient = func(_ string, _ int, _ int, _ *zap.Logger) (azureblob.BlobClient, error) {
		return mockClient, nil
	}

	baseCfg := func(format BlobFormat) *Config {
		return &Config{
			ConnectionString: "DefaultEndpointsProtocol=https;AccountName=test;AccountKey=dGVzdA==;EndpointSuffix=core.windows.net",
			Container:        "test-container",
			PollInterval:     1 * time.Minute,
			BatchSize:        100,
			PageSize:         1000,
			BlobFormat:       format,
		}
	}

	t.Run("Default format uses LogsConsumer", func(t *testing.T) {
		r, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), baseCfg(""), consumertest.NewNop())
		require.NoError(t, err)
		require.IsType(t, &blobconsume.LogsConsumer{}, r.consumer)
	})

	t.Run("OTLP format uses LogsConsumer", func(t *testing.T) {
		r, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), baseCfg(BlobFormatOTLP), consumertest.NewNop())
		require.NoError(t, err)
		require.IsType(t, &blobconsume.LogsConsumer{}, r.consumer)
	})

	t.Run("JSON format uses NDJSONLogsConsumer", func(t *testing.T) {
		r, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), baseCfg(BlobFormatJSON), consumertest.NewNop())
		require.NoError(t, err)
		require.IsType(t, &blobconsume.NDJSONLogsConsumer{}, r.consumer)
	})

	t.Run("Text format uses RawTextLogsConsumer", func(t *testing.T) {
		r, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), baseCfg(BlobFormatText), consumertest.NewNop())
		require.NoError(t, err)
		require.IsType(t, &blobconsume.RawTextLogsConsumer{}, r.consumer)
	})

	t.Run("Text format with enable_per_line_text uses LineTextLogsConsumer", func(t *testing.T) {
		cfg := baseCfg(BlobFormatText)
		cfg.EnableIncrementalRead = true
		cfg.EnablePerLineText = true
		r, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), cfg, consumertest.NewNop())
		require.NoError(t, err)
		require.IsType(t, &blobconsume.LineTextLogsConsumer{}, r.consumer)
	})

	t.Run("RecordsJSON format uses RecordsJSONLogsConsumer", func(t *testing.T) {
		r, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), baseCfg(BlobFormatRecordsJSON), consumertest.NewNop())
		require.NoError(t, err)
		require.IsType(t, &blobconsume.RecordsJSONLogsConsumer{}, r.consumer)
	})

	t.Run("Shutdown without a successful Start does not panic on the incremental path", func(t *testing.T) {
		// A collector-graph shutdown after another component's Start failure calls Shutdown
		// without this receiver's Start ever running, leaving r.checkpoint nil. makeCheckpoint
		// must treat that as nothing-to-save rather than dereferencing the nil checkpoint.
		cfg := baseCfg(BlobFormatRecordsJSON)
		cfg.EnableIncrementalRead = true
		r, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), cfg, consumertest.NewNop())
		require.NoError(t, err)
		require.NotPanics(t, func() {
			require.NoError(t, r.Shutdown(context.Background()))
		})
	})
}

func TestNewLogsReceiver_RejectsUnsupportedFormat(t *testing.T) {
	originalNewAzureBlobClient := newAzureBlobClient
	defer func() { newAzureBlobClient = originalNewAzureBlobClient }()

	mockClient := new(azureblob.MockBlobClient)
	newAzureBlobClient = func(_ string, _ int, _ int, _ *zap.Logger) (azureblob.BlobClient, error) {
		return mockClient, nil
	}

	cfg := &Config{
		ConnectionString: "DefaultEndpointsProtocol=https;AccountName=test;AccountKey=dGVzdA==;EndpointSuffix=core.windows.net",
		Container:        "test-container",
		PollInterval:     1 * time.Minute,
		BatchSize:        100,
		PageSize:         1000,
		BlobFormat:       "invalid_format",
	}

	_, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), cfg, consumertest.NewNop())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported blob_format")
}

func TestNewMetricsReceiver_RejectsNonOTLP(t *testing.T) {
	originalNewAzureBlobClient := newAzureBlobClient
	defer func() { newAzureBlobClient = originalNewAzureBlobClient }()

	mockClient := new(azureblob.MockBlobClient)
	newAzureBlobClient = func(_ string, _ int, _ int, _ *zap.Logger) (azureblob.BlobClient, error) {
		return mockClient, nil
	}

	cfg := &Config{
		ConnectionString: "DefaultEndpointsProtocol=https;AccountName=test;AccountKey=dGVzdA==;EndpointSuffix=core.windows.net",
		Container:        "test-container",
		PollInterval:     1 * time.Minute,
		BatchSize:        100,
		PageSize:         1000,
		BlobFormat:       BlobFormatJSON,
	}

	_, err := newMetricsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), cfg, consumertest.NewNop())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported for metrics pipelines")
}

func TestNewTracesReceiver_RejectsNonOTLP(t *testing.T) {
	originalNewAzureBlobClient := newAzureBlobClient
	defer func() { newAzureBlobClient = originalNewAzureBlobClient }()

	mockClient := new(azureblob.MockBlobClient)
	newAzureBlobClient = func(_ string, _ int, _ int, _ *zap.Logger) (azureblob.BlobClient, error) {
		return mockClient, nil
	}

	cfg := &Config{
		ConnectionString: "DefaultEndpointsProtocol=https;AccountName=test;AccountKey=dGVzdA==;EndpointSuffix=core.windows.net",
		Container:        "test-container",
		PollInterval:     1 * time.Minute,
		BatchSize:        100,
		PageSize:         1000,
		BlobFormat:       BlobFormatText,
	}

	_, err := newTracesReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), cfg, consumertest.NewNop())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported for traces pipelines")
}

func TestPollingReceiver_MultiBatchNoDataLoss(t *testing.T) {
	t.Run("All blobs across multiple batches are processed when batch 1 has latest timestamp", func(t *testing.T) {
		logger := zap.NewNop()
		now := time.Now().UTC()

		cfg := &Config{
			Container:       "test-container",
			PollInterval:    1 * time.Minute,
			InitialLookback: 5 * time.Minute,
			UseLastModified: true,
		}

		mockClient := new(azureblob.MockBlobClient)
		checkpoint := NewPollingCheckpoint()

		receiver := &pollingReceiver{
			logger:             logger,
			cfg:                cfg,
			azureClient:        mockClient,
			checkpoint:         checkpoint,
			checkpointStore:    storageclient.NewNopStorage(),
			pollInterval:       cfg.PollInterval,
			initialLookback:    cfg.InitialLookback,
			supportedTelemetry: pipeline.SignalLogs,
			consumer:           blobconsume.NewLogsConsumer(consumertest.NewNop()),
			mut:                &sync.Mutex{},
			wg:                 &sync.WaitGroup{},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Simulate 3 batches where batch 1 contains the blob with the latest timestamp.
		// Before the fix, the per-batch checkpoint would advance LastTs after batch 1,
		// causing blobs in batches 2 and 3 to be skipped.
		batch1 := []*azureblob.BlobInfo{
			{Name: "blob-latest.json", Size: 100, LastModified: now.Add(-1 * time.Second)}, // latest timestamp
			{Name: "blob-early1.json", Size: 100, LastModified: now.Add(-10 * time.Second)},
		}
		batch2 := []*azureblob.BlobInfo{
			{Name: "blob-early2.json", Size: 100, LastModified: now.Add(-15 * time.Second)},
			{Name: "blob-early3.json", Size: 100, LastModified: now.Add(-20 * time.Second)},
		}
		batch3 := []*azureblob.BlobInfo{
			{Name: "blob-early4.json", Size: 100, LastModified: now.Add(-25 * time.Second)},
			{Name: "blob-early5.json", Size: 100, LastModified: now.Add(-30 * time.Second)},
		}

		// Track which blobs were downloaded (i.e. processed)
		downloadedBlobs := make(map[string]bool)
		downloadMu := sync.Mutex{}

		// Valid minimal OTLP JSON logs payload
		validJSON := []byte(`{"resourceLogs":[]}`)

		// otlp uses the buffered download path.
		mockClient.On("DownloadBlob", mock.Anything, "test-container", mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				blobName := args.Get(2).(string)
				buf := args.Get(3).([]byte)
				copy(buf, validJSON)
				downloadMu.Lock()
				downloadedBlobs[blobName] = true
				downloadMu.Unlock()
			}).
			Return(int64(len(validJSON)), nil)

		// Mock StreamBlobs to send 3 batches sequentially
		mockClient.On("StreamBlobs", mock.Anything, "test-container", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				blobChan := args.Get(4).(chan []*azureblob.BlobInfo)
				doneChan := args.Get(5).(chan struct{})

				blobChan <- batch1
				blobChan <- batch2
				blobChan <- batch3
				close(doneChan)
			})

		receiver.runPoll(ctx)

		// All 6 blobs should have been downloaded
		downloadMu.Lock()
		defer downloadMu.Unlock()

		allBlobs := []string{
			"blob-latest.json", "blob-early1.json",
			"blob-early2.json", "blob-early3.json",
			"blob-early4.json", "blob-early5.json",
		}
		for _, name := range allBlobs {
			require.True(t, downloadedBlobs[name], "blob %s should have been processed but was skipped", name)
		}
		require.Equal(t, 6, len(downloadedBlobs), "all 6 blobs should be processed")

		// Checkpoint LastTs should be set to the latest timestamp (blob-latest.json)
		require.NotNil(t, receiver.lastBlobTime)
		require.Equal(t, now.Add(-1*time.Second), *receiver.lastBlobTime)
	})
}

func TestPollingReceiver_processBlob_DownloadsFullContentDespiteStaleSize(t *testing.T) {
	// Azure grows an hourly flow-log blob all hour via PutBlock, so the size
	// reported by the listing (blob.Size) is stale and understates the blob's
	// current content. processBlob must stream the whole current blob rather
	// than pre-sizing a buffer from the stale size, which the SDK overflows
	// ("not enough space for all bytes"). The stream download ignores blob.Size
	// entirely, so a stale (too-small) size no longer drops content.
	logger := zap.NewNop()
	sink := new(consumertest.LogsSink)

	// Two records; content (29 bytes) far exceeds the stale listed size.
	fullContent := []byte(`{"records":[{"a":1},{"b":2}]}`)
	const staleSize = 5

	mockClient := new(azureblob.MockBlobClient)
	mockClient.EXPECT().
		DownloadBlobStream(mock.Anything, "test-container", "flow/PT1H.json").
		Return(fullContent, nil)

	receiver := &pollingReceiver{
		logger:      logger,
		cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatRecordsJSON},
		azureClient: mockClient,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, logger),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}

	_, err := receiver.processBlob(context.Background(), &azureblob.BlobInfo{
		Name: "flow/PT1H.json",
		Size: staleSize,
	})
	require.NoError(t, err)
	require.Equal(t, 2, sink.LogRecordCount(), "both records from the full blob content should be consumed")
	mockClient.AssertExpectations(t)
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := gw.Write(data)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	return buf.Bytes()
}

func TestPollingReceiver_useIncremental_gzipCaseInsensitive(t *testing.T) {
	// A gzip blob cannot be range-tailed, so it must take the whole-tracked (decompressing)
	// path regardless of extension case. A case-sensitive check would route an uppercase
	// .GZ blob to the incremental path, where it is read as raw compressed bytes.
	r := &pollingReceiver{cfg: &Config{BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true}}
	require.False(t, r.useIncremental("flow/PT1H.json.gz"), "lowercase .gz is not incremental")
	require.False(t, r.useIncremental("flow/PT1H.json.GZ"), "uppercase .GZ is gzip too; not incremental")
	require.False(t, r.useIncremental("flow/PT1H.json.Gz"), "mixed-case .Gz is gzip too; not incremental")
	require.True(t, r.useIncremental("flow/PT1H.json"), "a non-gzip blob is incremental")
}

func TestPollingReceiver_processBlob(t *testing.T) {
	records := []byte(`{"records":[{"a":1},{"b":2}]}`)

	t.Run("uppercase .GZ blob is decompressed and consumed", func(t *testing.T) {
		sink := new(consumertest.LogsSink)
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().
			DownloadBlobStream(mock.Anything, "test-container", "flow/PT1H.json.GZ").
			Return(gzipBytes(t, records), nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mockClient,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlob(context.Background(), &azureblob.BlobInfo{Name: "flow/PT1H.json.GZ"})
		require.NoError(t, err, "an uppercase .GZ blob is gunzipped, not read as raw bytes")
		require.Equal(t, 2, sink.LogRecordCount())
	})

	t.Run("download error is returned", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().
			DownloadBlobStream(mock.Anything, "test-container", "flow/PT1H.json").
			Return(nil, errors.New("network error"))

		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mockClient,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlob(context.Background(), &azureblob.BlobInfo{Name: "flow/PT1H.json"})
		require.ErrorContains(t, err, "blob download failed")
	})

	t.Run("gzipped blob is decompressed and consumed", func(t *testing.T) {
		sink := new(consumertest.LogsSink)
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().
			DownloadBlobStream(mock.Anything, "test-container", "flow/PT1H.json.gz").
			Return(gzipBytes(t, records), nil)

		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mockClient,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlob(context.Background(), &azureblob.BlobInfo{Name: "flow/PT1H.json.gz"})
		require.NoError(t, err)
		require.Equal(t, 2, sink.LogRecordCount())
	})

	t.Run("corrupt gzip returns error", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().
			DownloadBlobStream(mock.Anything, "test-container", "flow/PT1H.json.gz").
			Return([]byte("not gzip"), nil)

		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mockClient,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlob(context.Background(), &azureblob.BlobInfo{Name: "flow/PT1H.json.gz"})
		require.ErrorContains(t, err, "gzip")
	})

	t.Run("unsupported extension returns error", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().
			DownloadBlobStream(mock.Anything, "test-container", "flow/data.txt").
			Return(records, nil)

		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mockClient,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlob(context.Background(), &azureblob.BlobInfo{Name: "flow/data.txt"})
		require.ErrorContains(t, err, "unsupported file type")
	})

	t.Run("text format accepts any extension on the whole-blob path", func(t *testing.T) {
		// The text format is content-agnostic, so a .txt (or .log, extensionless) blob is
		// read whole as one record with the flag off — matching the per-line text path and
		// making enable_per_line_text round-trippable.
		sink := new(consumertest.LogsSink)
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().
			DownloadBlobStream(mock.Anything, "test-container", "flow/data.txt").
			Return([]byte("line one\nline two\n"), nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatText},
			azureClient: mockClient,
			consumer:    blobconsume.NewRawTextLogsConsumer(sink),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlob(context.Background(), &azureblob.BlobInfo{Name: "flow/data.txt"})
		require.NoError(t, err)
		require.Equal(t, 1, sink.LogRecordCount(), "the whole text blob becomes one record")
	})

	t.Run("consumer error is returned", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().
			DownloadBlobStream(mock.Anything, "test-container", "flow/PT1H.json").
			Return([]byte("{not valid json"), nil)

		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mockClient,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlob(context.Background(), &azureblob.BlobInfo{Name: "flow/PT1H.json"})
		require.ErrorContains(t, err, "consume")
	})

	t.Run("otlp uses the buffered (parallel, exact-size) download, not the stream", func(t *testing.T) {
		// otlp blobs are written once with an accurate listed size, so they keep the
		// SDK's parallel buffered download; the stream path is reserved for the
		// append-growable formats whose listed size is stale.
		otlp := []byte(`{"resourceLogs":[]}`)
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().
			DownloadBlob(mock.Anything, "test-container", "flow/otlp.json", mock.Anything).
			RunAndReturn(func(_ context.Context, _, _ string, buf []byte) (int64, error) {
				return int64(copy(buf, otlp)), nil
			})
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatOTLP},
			azureClient: mockClient,
			consumer:    blobconsume.NewLogsConsumer(new(consumertest.LogsSink)),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlob(context.Background(), &azureblob.BlobInfo{Name: "flow/otlp.json", Size: int64(len(otlp))})
		require.NoError(t, err)
		mockClient.AssertExpectations(t)
		mockClient.AssertNotCalled(t, "DownloadBlobStream")
	})

	t.Run("otlp buffered download error is returned", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().
			DownloadBlob(mock.Anything, "test-container", "flow/otlp.json", mock.Anything).
			Return(int64(0), errors.New("network error"))
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "test-container", BlobFormat: BlobFormatOTLP},
			azureClient: mockClient,
			consumer:    blobconsume.NewLogsConsumer(new(consumertest.LogsSink)),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlob(context.Background(), &azureblob.BlobInfo{Name: "flow/otlp.json", Size: 10})
		// The otlp path is not tracked/quarantined, so it does not carry the transient
		// sentinel (only the append-growable branch does); the download error surfaces plainly.
		require.ErrorContains(t, err, "download: network error")
		require.NotErrorIs(t, err, errBlobDownloadTransient)
	})
}

// invalidRangeErr mimics the Azure "invalid range" (HTTP 416) response returned
// when a range read starts at or past the blob's current length.
func invalidRangeErr() error {
	return &azcore.ResponseError{ErrorCode: string(bloberror.InvalidRange)}
}

// blobNotFoundErr mimics the Azure "blob not found" (HTTP 404) response returned
// when the target blob no longer exists.
func blobNotFoundErr() error {
	return &azcore.ResponseError{ErrorCode: string(bloberror.BlobNotFound)}
}

// rangeOf models Azure's ranged Get Blob for the mock: a read whose start offset is
// past byte 0 and at or past the blob length is rejected with InvalidRange (rather than
// returning empty bytes), so the code's end-of-blob handling is exercised as it would be
// against real Azure. A whole-blob read at offset 0 of an empty blob returns 200 with
// empty bytes (not 416), matching real Azure.
// rangeOf models DownloadBlobRange over content: the ranged bytes plus the blob's full size, so a
// mocked identity read reports a realistic total.
func rangeOf(content []byte, offset, count int64) ([]byte, int64, error) {
	total := int64(len(content))
	if offset > 0 && offset >= total {
		return nil, total, invalidRangeErr()
	}
	end := total
	if count > 0 && offset+count < end {
		end = offset + count
	}
	return append([]byte(nil), content[offset:end]...), total, nil
}

func TestPollingReceiver_processBlobIncremental_TwoPollNoDupNoMiss(t *testing.T) {
	sink := new(consumertest.LogsSink)
	checkpoint := NewPollingCheckpoint()
	mockClient := new(azureblob.MockBlobClient)

	var mu sync.Mutex
	content := []byte(`{"records":[{"a":1},{"b":2}`) // poll 1: two records, no closing yet
	mockClient.EXPECT().
		DownloadBlobRange(mock.Anything, "c", "PT1H.json", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, _ string, offset, count int64) ([]byte, int64, error) {
			mu.Lock()
			defer mu.Unlock()
			return rangeOf(content, offset, count)
		})

	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
		azureClient: mockClient,
		checkpoint:  checkpoint,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	blob := func(lm time.Time) *azureblob.BlobInfo {
		mu.Lock()
		defer mu.Unlock()
		return &azureblob.BlobInfo{Name: "PT1H.json", Size: int64(len(content)), LastModified: lm}
	}
	lm := time.Date(2026, 8, 31, 14, 1, 0, 0, time.UTC)

	// Poll 1.
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), blob(lm))))
	require.Equal(t, 2, sink.LogRecordCount())
	prog, _ := checkpoint.ProgressFor("PT1H.json")
	require.Equal(t, int64(len(`{"records":[{"a":1},{"b":2}`)), prog.Offset,
		"offset stops after the last complete record; the footer is never consumed")

	// Poll 2: two more records appended and the array is now closed.
	mu.Lock()
	content = []byte(`{"records":[{"a":1},{"b":2},{"c":3},{"d":4}]}`)
	mu.Unlock()
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), blob(lm.Add(time.Minute)))))
	require.Equal(t, 4, sink.LogRecordCount(), "each record is emitted exactly once across the two polls")
	prog, _ = checkpoint.ProgressFor("PT1H.json")
	require.Equal(t, int64(len(`{"records":[{"a":1},{"b":2},{"c":3},{"d":4}`)), prog.Offset,
		"offset stops after the last record; the closing ]} stays unconsumed")
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestPollingReceiver_Incremental_ConcurrentBlobsNoRace(t *testing.T) {
	// processBlobs spawns one goroutine per blob, all touching the shared Progress
	// map. Run under -race to catch unsynchronized access.
	sink := new(consumertest.LogsSink)
	mockClient := new(azureblob.MockBlobClient)
	now := time.Now().UTC()

	r := &pollingReceiver{
		logger:             zap.NewNop(),
		cfg:                &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, UseLastModified: true, EnableIncrementalRead: true},
		azureClient:        mockClient,
		checkpoint:         NewPollingCheckpoint(),
		supportedTelemetry: pipeline.SignalLogs,
		consumer:           blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:                &sync.Mutex{},
		wg:                 &sync.WaitGroup{},
	}
	// Wire real telemetry so the per-goroutine record path (consume -> counter Add) runs under -race.
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })
	require.NoError(t, r.initTelemetry(tt.NewTelemetrySettings()))
	mockClient.EXPECT().
		DownloadBlobRange(mock.Anything, "c", mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, name string, _, _ int64) ([]byte, int64, error) {
			return []byte(`{"records":[{"n":"` + name + `"}]}`), -1, nil
		})

	const n = 40
	blobs := make([]*azureblob.BlobInfo, 0, n)
	for i := 0; i < n; i++ {
		blobs = append(blobs, &azureblob.BlobInfo{Name: fmt.Sprintf("b%d.json", i), Size: 100, LastModified: now})
	}

	processed, _, _ := r.processBlobs(context.Background(), blobs, now.Add(-time.Hour), now.Add(time.Hour))
	require.Equal(t, n, processed)
	require.Equal(t, n, sink.LogRecordCount())
	for i := 0; i < n; i++ {
		prog, ok := r.checkpoint.ProgressFor(fmt.Sprintf("b%d.json", i))
		require.True(t, ok)
		require.Greater(t, prog.Offset, int64(0), "each blob's offset advanced")
	}
}

func TestPollingReceiver_processBlobIncremental_FingerprintGrowsWhenOffsetOutrunsHead(t *testing.T) {
	// After assume_append_only froze a short fingerprint and was turned off, the stored offset can
	// outrun the readable head (offset > len(head)). The grown fingerprint then falls back to the
	// full head rather than splicing an unreadable gap.
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	content := []byte(`{"records":[{"a":111},{"b":2}]}`)
	const fpSize = 16
	off := int64(len(`{"records":[{"a":111}`)) // 21, past the readable head (16)

	mockClient := new(azureblob.MockBlobClient)
	mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ string, offset, count int64) ([]byte, int64, error) {
			return rangeOf(content, offset, count)
		})
	cp := NewPollingCheckpoint()
	// A short stored fingerprint (< fpSize) that is a prefix of the head, so identity verifies.
	cp.UpdateProgress("b.json", BlobProgress{Offset: off, Fingerprint: content[:4], LastModified: lm})
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, FingerprintSize: fpSize},
		azureClient: mockClient,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}

	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: lm.Add(time.Minute)})))

	prog, _ := cp.ProgressFor("b.json")
	require.Equal(t, blobconsume.Fingerprint(content, fpSize), prog.Fingerprint, "the short fingerprint grows to the full head when the offset outruns it")
	require.Len(t, prog.Fingerprint, fpSize)
	require.Greater(t, prog.Offset, off, "and the offset advanced")
}

func TestPollingReceiver_processBlobIncremental_Branches(t *testing.T) {
	newReceiver := func(sink *consumertest.LogsSink, cp *PollingCheckPoint, format BlobFormat, client azureblob.BlobClient) *pollingReceiver {
		var consumer blobconsume.Consumer
		switch format {
		case BlobFormatJSON:
			consumer = blobconsume.NewNDJSONLogsConsumer(sink, zap.NewNop())
		case BlobFormatText:
			consumer = blobconsume.NewLineTextLogsConsumer(sink)
		default:
			consumer = blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop())
		}
		return &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: format, EnableIncrementalRead: true, EnablePerLineText: format == BlobFormatText},
			azureClient: client,
			checkpoint:  cp,
			consumer:    consumer,
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
	}
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	serve := func(content *[]byte) func(context.Context, string, string, int64, int64) ([]byte, int64, error) {
		return func(_ context.Context, _ string, _ string, offset, count int64) ([]byte, int64, error) {
			return rangeOf(*content, offset, count)
		}
	}

	t.Run("download error at offset 0 is returned and the blob is tracked as never-read", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
			Return(nil, -1, errors.New("range boom"))
		cp := NewPollingCheckpoint()
		r := newReceiver(new(consumertest.LogsSink), cp, BlobFormatRecordsJSON, mockClient)

		_, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 10, LastModified: lm})
		require.ErrorContains(t, err, "download blob range")
		prog, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "tracked so its drop is logged if it ages out still never read")
		require.Equal(t, int64(0), prog.Offset)
		require.NotEmpty(t, prog.LastReadError)
	})

	t.Run("partial delta with no complete record does not advance", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
			Return([]byte(`{"records":[{"a":`), -1, nil)
		cp := NewPollingCheckpoint()
		sink := new(consumertest.LogsSink)
		r := newReceiver(sink, cp, BlobFormatRecordsJSON, mockClient)

		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 17, LastModified: lm})))
		require.Equal(t, 0, sink.LogRecordCount())
		prog, _ := cp.ProgressFor("b.json")
		require.Equal(t, int64(0), prog.Offset, "offset does not advance past an incomplete record")
	})

	t.Run("terminal records-json envelope is quarantined on the first read, not silently re-read until seal", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
			Return([]byte(`[{"a":1}]`), -1, nil) // a top-level array can never become a {"records":[...]} envelope
		cp := NewPollingCheckpoint()
		sink := new(consumertest.LogsSink)
		r := newReceiver(sink, cp, BlobFormatRecordsJSON, mockClient)

		processed, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 9, LastModified: lm})
		require.NoError(t, err)
		require.False(t, processed)
		require.Equal(t, 0, sink.LogRecordCount())
		prog, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the blob is tracked so the mtime gate skips it while unchanged")
		require.True(t, prog.Quarantined, "and quarantined rather than left to re-read until seal")
	})

	t.Run("only the closing footer left advances nothing", func(t *testing.T) {
		content := []byte(`{"records":[{"a":1}]}`)
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
			RunAndReturn(serve(&content))
		cp := NewPollingCheckpoint()
		off := int64(len(`{"records":[{"a":1}`))
		cp.UpdateProgress("b.json", BlobProgress{Offset: off, Fingerprint: blobconsume.Fingerprint(content, defaultFingerprintSize), LastModified: lm})
		sink := new(consumertest.LogsSink)
		r := newReceiver(sink, cp, BlobFormatRecordsJSON, mockClient)

		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: lm.Add(time.Minute)})))
		require.Equal(t, 0, sink.LogRecordCount())
		prog, _ := cp.ProgressFor("b.json")
		require.Equal(t, off, prog.Offset, "the footer is never consumed, so the offset stays put")
	})

	t.Run("consumer error is returned", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
			Return([]byte(`{"records":[{"a":1}]}`), -1, nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mockClient,
			checkpoint:  NewPollingCheckpoint(),
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(consumertest.NewErr(errors.New("downstream boom")), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 21, LastModified: lm})
		require.ErrorContains(t, err, "consume")
	})

	t.Run("replaced blob resets its offset", func(t *testing.T) {
		content := []byte(`{"records":[{"z":9}]}`) // different first record than the stored fingerprint
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
			RunAndReturn(serve(&content))
		cp := NewPollingCheckpoint()
		cp.UpdateProgress("b.json", BlobProgress{Offset: 15, Fingerprint: []byte(`{"records":[{"a":1}`), LastModified: lm})
		sink := new(consumertest.LogsSink)
		r := newReceiver(sink, cp, BlobFormatRecordsJSON, mockClient)

		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: lm.Add(time.Minute)})))
		require.Equal(t, 1, sink.LogRecordCount(), "the replaced blob is re-read from the start")
		prog, _ := cp.ProgressFor("b.json")
		require.Equal(t, int64(len(`{"records":[{"z":9}`)), prog.Offset)
	})

	t.Run("ndjson blob is read incrementally by line", func(t *testing.T) {
		content := []byte("{\"a\":1}\n{\"b\":2}\n{\"c\"")
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
			RunAndReturn(serve(&content))
		cp := NewPollingCheckpoint()
		sink := new(consumertest.LogsSink)
		r := newReceiver(sink, cp, BlobFormatJSON, mockClient)

		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: lm})))
		require.Equal(t, 2, sink.LogRecordCount())
		prog, _ := cp.ProgressFor("b.json")
		require.Equal(t, int64(len("{\"a\":1}\n{\"b\":2}\n")), prog.Offset)

		content = []byte("{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n")
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: lm.Add(time.Minute)})))
		require.Equal(t, 3, sink.LogRecordCount(), "each line emitted exactly once")
	})

	t.Run("text blob emits one record per line", func(t *testing.T) {
		content := []byte("line one\nline two\n")
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.txt", mock.Anything, mock.Anything).
			RunAndReturn(serve(&content))
		cp := NewPollingCheckpoint()
		sink := new(consumertest.LogsSink)
		r := newReceiver(sink, cp, BlobFormatText, mockClient)

		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.txt", Size: int64(len(content)), LastModified: lm})))
		require.Equal(t, 2, sink.LogRecordCount(), "one record per line, not one per blob")
	})
}

func TestPollingReceiver_Incremental_HorizonRevisitAndMtimeGate(t *testing.T) {
	sink := new(consumertest.LogsSink)
	mockClient := new(azureblob.MockBlobClient)
	clock := &fakeClock{t: time.Date(2026, 8, 31, 14, 30, 0, 0, time.UTC)}
	blobName := "2026/08/31/14/PT1H.json" // path-time 14:00, fixed at the hour start

	var mu sync.Mutex
	content := []byte(`{"records":[{"a":1}`) // poll 1: one record, unsealed
	lastMod := clock.Now()
	downloads := 0

	r := &pollingReceiver{
		logger: zap.NewNop(),
		cfg: &Config{
			Container:             "c",
			BlobFormat:            BlobFormatRecordsJSON,
			TimePattern:           "{year}/{month}/{day}/{hour}",
			TelemetryType:         "logs",
			PollInterval:          time.Minute,
			InitialLookback:       5 * time.Minute,
			EnableIncrementalRead: true,
		},
		azureClient:        mockClient,
		checkpoint:         NewPollingCheckpoint(),
		checkpointStore:    storageclient.NewNopStorage(),
		supportedTelemetry: pipeline.SignalLogs,
		consumer:           blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:                &sync.Mutex{},
		wg:                 &sync.WaitGroup{},
		nowFn:              clock.Now,
		revisitWindow:      defaultIncrementalRevisitWindow, // resolved by newPollingReceiver in production
	}

	mockClient.EXPECT().
		StreamBlobs(mock.Anything, "c", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ string, _ *string, _ chan error, blobChan chan []*azureblob.BlobInfo, doneChan chan struct{}) {
			mu.Lock()
			info := azureblob.BlobInfo{Name: blobName, Size: int64(len(content)), LastModified: lastMod}
			mu.Unlock()
			blobChan <- []*azureblob.BlobInfo{&info}
			close(doneChan)
		})
	mockClient.EXPECT().
		DownloadBlobRange(mock.Anything, "c", blobName, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, _ string, offset, count int64) ([]byte, int64, error) {
			mu.Lock()
			defer mu.Unlock()
			downloads++
			return rangeOf(content, offset, count)
		})

	ctx := context.Background()

	// Poll 1 at 14:30 — first poll, one record so far.
	r.runPoll(ctx)
	require.Equal(t, 1, sink.LogRecordCount())
	prog, ok := r.checkpoint.ProgressFor(blobName)
	require.True(t, ok)
	require.Greater(t, prog.Offset, int64(0))

	// A second record is appended (blob modified). Poll 2 an hour later. The narrow
	// poll window [14:30,15:30] would exclude the blob's 14:00 path-time (the
	// permanent-loss bug), but the revisit horizon includes it.
	clock.Advance(1 * time.Hour) // 15:30
	mu.Lock()
	content = []byte(`{"records":[{"a":1},{"b":2}]}`)
	lastMod = clock.Now()
	mu.Unlock()
	r.runPoll(ctx)
	require.Equal(t, 2, sink.LogRecordCount(), "the mid-hour append is not lost once the poll window advances")

	// Poll 3: the blob has not changed (LastModified frozen). The mtime gate skips
	// it with no download.
	before := downloads
	clock.Advance(1 * time.Minute) // 15:31
	r.runPoll(ctx)
	require.Equal(t, 2, sink.LogRecordCount(), "an unchanged blob is not re-read")
	require.Equal(t, before, downloads, "no download for a blob whose LastModified is unchanged")
}

func TestPollingReceiver_processBlobWholeTracked(t *testing.T) {
	// A gzip blob in an append-growable config is read whole (gzip can't be
	// range-tailed) and recorded as fully consumed so the mtime gate skips it.
	mockClient := new(azureblob.MockBlobClient)
	sink := new(consumertest.LogsSink)
	gz := gzipBytes(t, []byte(`{"records":[{"a":1}]}`))
	mockClient.EXPECT().DownloadBlobStream(mock.Anything, "c", "b.json.gz").Return(gz, nil)

	cp := NewPollingCheckpoint()
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
		azureClient: mockClient,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	processed, err := r.processBlobWholeTracked(context.Background(),
		&azureblob.BlobInfo{Name: "b.json.gz", Size: int64(len(gz)), LastModified: lm})
	require.NoError(t, err)
	require.True(t, processed, "records emitted, so counted")
	require.Equal(t, 1, sink.LogRecordCount())
	prog, ok := cp.ProgressFor("b.json.gz")
	require.True(t, ok)
	require.Equal(t, int64(len(gz)), prog.Offset, "recorded as fully consumed")
	require.Equal(t, lm, prog.LastModified)

	t.Run("gzip with only malformed lines is recorded consumed but not counted as processed", func(t *testing.T) {
		// Every line fails to parse, so the NDJSON consumer emits nothing and returns
		// (attempted, 0, nil). The whole-tracked path must report processed=false: counting a
		// zero-record read inflates total_processed and suppresses the poll-summary "nothing
		// parsed" warn (self-telemetry must stay accurate). The blob is still recorded consumed
		// so the mtime gate does not re-read it.
		mc := new(azureblob.MockBlobClient)
		sink2 := new(consumertest.LogsSink)
		gzBad := gzipBytes(t, []byte("not json\nstill not json\n"))
		mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "bad.json.gz").Return(gzBad, nil)
		cp2 := NewPollingCheckpoint()
		r2 := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON},
			azureClient: mc,
			checkpoint:  cp2,
			consumer:    blobconsume.NewNDJSONLogsConsumer(sink2, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		processed, err := r2.processBlobWholeTracked(context.Background(),
			&azureblob.BlobInfo{Name: "bad.json.gz", Size: int64(len(gzBad)), LastModified: lm})
		require.NoError(t, err)
		require.False(t, processed, "no records emitted, so not counted as processed")
		require.Equal(t, 0, sink2.LogRecordCount())
		prog, ok := cp2.ProgressFor("bad.json.gz")
		require.True(t, ok, "still recorded consumed so the mtime gate skips it")
		require.Equal(t, int64(len(gzBad)), prog.Offset)
	})

	t.Run("a grown gzip blob re-emits already-consumed records (at-least-once; gzip cannot be range-tailed)", func(t *testing.T) {
		// Documented limitation: a gzip blob grown in place (here, rewritten with more records)
		// can't be range-tailed, so an mtime change re-reads and re-decompresses the whole blob
		// and the already-delivered records are emitted again.
		mc := new(azureblob.MockBlobClient)
		sink2 := new(consumertest.LogsSink)
		gen1 := gzipBytes(t, []byte(`{"records":[{"a":1},{"b":2}]}`))                  // 2 records
		grown := gzipBytes(t, []byte(`{"records":[{"a":1},{"b":2},{"c":3},{"d":4}]}`)) // same 2 + 2 new
		cp2 := NewPollingCheckpoint()
		r2 := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mc, checkpoint: cp2,
			consumer: blobconsume.NewRecordsJSONLogsConsumer(sink2, zap.NewNop()),
			mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
		}
		lm2 := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
		mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "g.json.gz").Return(gen1, nil).Once()
		_, err := r2.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "g.json.gz", Size: int64(len(gen1)), LastModified: lm2})
		require.NoError(t, err)
		require.Equal(t, 2, sink2.LogRecordCount(), "first read emits the two records")

		mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "g.json.gz").Return(grown, nil).Once()
		_, err = r2.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "g.json.gz", Size: int64(len(grown)), LastModified: lm2.Add(time.Minute)})
		require.NoError(t, err)
		require.Equal(t, 6, sink2.LogRecordCount(), "the whole blob is re-read: the two original records are re-emitted plus the two new ones")
	})

	t.Run("download error is propagated and the blob is tracked as never-read", func(t *testing.T) {
		mc := new(azureblob.MockBlobClient)
		mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "bad.json.gz").Return(nil, errors.New("gz boom"))
		cp2 := NewPollingCheckpoint()
		r2 := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mc,
			checkpoint:  cp2,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		processed, err := r2.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "bad.json.gz", Size: 5, LastModified: lm})
		require.Error(t, err)
		require.False(t, processed)
		prog, ok := cp2.ProgressFor("bad.json.gz")
		require.True(t, ok, "tracked so its drop is logged if it ages out never read")
		require.Equal(t, int64(0), prog.Offset)
		require.NotEmpty(t, prog.LastReadError)
	})

	t.Run("permanent content error quarantines the blob and does not re-error", func(t *testing.T) {
		// A .gz that downloads but can't be decompressed is a permanent content error. It
		// must be quarantined (progress recorded) rather than re-errored every poll, which
		// would re-download the same unparseable blob forever.
		mc := new(azureblob.MockBlobClient)
		mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "corrupt.json.gz").Return([]byte("not gzip at all"), nil)
		cp2 := NewPollingCheckpoint()
		r2 := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mc,
			checkpoint:  cp2,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		processed, err := r2.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "corrupt.json.gz", Size: 15, LastModified: lm})
		require.NoError(t, err, "a permanent content error is swallowed, not re-errored")
		require.False(t, processed, "a quarantined blob emitted nothing, so it is not counted")
		prog, ok := cp2.ProgressFor("corrupt.json.gz")
		require.True(t, ok, "the bad blob is tracked so the mtime gate skips it")
		require.True(t, prog.Quarantined, "and quarantined")
		require.Equal(t, lm, prog.LastModified, "with the blob's mtime so a later change re-reads it")
	})

	t.Run("transient downstream consume error is propagated and the blob is tracked as never-read", func(t *testing.T) {
		// A valid gzip blob that parses fine but whose downstream fails transiently is tracked as
		// never-read (so its drop is logged at age-out) and retried via the LastReadError bypass;
		// quarantining it would strand a valid blob's records (a gzip mtime never re-triggers).
		mc := new(azureblob.MockBlobClient)
		mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "backpressure.json.gz").
			Return(gzipBytes(t, []byte(`{"records":[{"a":1}]}`)), nil)
		cp2 := NewPollingCheckpoint()
		r2 := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mc,
			checkpoint:  cp2,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(consumertest.NewErr(errors.New("sending queue is full")), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		processed, err := r2.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "backpressure.json.gz", Size: 20, LastModified: lm})
		require.Error(t, err, "a transient downstream failure is surfaced, not swallowed")
		require.False(t, processed)
		prog, ok := cp2.ProgressFor("backpressure.json.gz")
		require.True(t, ok, "tracked as never-read so its drop is logged if it ages out")
		require.NotEmpty(t, prog.LastReadError)
	})
}

func TestPollingReceiver_processBlobWholeTracked_textGzip(t *testing.T) {
	// The gzip whole-tracked path is also reachable for text + enable_per_line_text; exercise it so
	// the per-line text consumer's handling on this path is covered, not only records-json/json.
	mc := new(azureblob.MockBlobClient)
	sink := new(consumertest.LogsSink)
	gz := gzipBytes(t, []byte("line1\nline2\nline3\n"))
	mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "b.txt.gz").Return(gz, nil)
	cp := NewPollingCheckpoint()
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatText, EnablePerLineText: true, EnableIncrementalRead: true},
		azureClient: mc,
		checkpoint:  cp,
		consumer:    blobconsume.NewLineTextLogsConsumer(sink),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	processed, err := r.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "b.txt.gz", Size: int64(len(gz)), LastModified: lm})
	require.NoError(t, err)
	require.True(t, processed)
	require.Equal(t, 3, sink.LogRecordCount(), "each text line becomes one record on the gzip whole-tracked path")
	prog, ok := cp.ProgressFor("b.txt.gz")
	require.True(t, ok)
	require.Equal(t, int64(len(gz)), prog.Offset)
}

func TestPollingReceiver_neverReadFailureTrackedAndLoggedAtAgeOut(t *testing.T) {
	// A blob whose every read/consume fails at offset 0 would otherwise be untracked and, once it
	// leaves the revisit window, drop silently. It must be tracked with the failure reason and its
	// drop logged at age-out.
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	core, logs := observer.New(zap.WarnLevel)

	mockClient := new(azureblob.MockBlobClient)
	mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		Return([]byte(`{"records":[{"a":1}]}`), -1, nil) // reads fine; the downstream consumer is what fails
	cp := NewPollingCheckpoint()
	r := &pollingReceiver{
		logger:      zap.New(core),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true, DeleteOnRead: true},
		azureClient: mockClient,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(consumertest.NewErr(errors.New("sending queue is full")), zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}

	_, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 21, LastModified: lm})
	require.Error(t, err)
	prog, ok := cp.ProgressFor("b.json")
	require.True(t, ok, "a never-read blob is tracked so its drop is not silent")
	require.Equal(t, int64(0), prog.Offset)
	require.NotEmpty(t, prog.LastReadError, "the failure reason is recorded")

	r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": prog})
	mockClient.AssertNotCalled(t, "DeleteBlob") // never read, so never delete
	warnings := logs.FilterMessageSnippet("aged out without a successful read").All()
	require.Len(t, warnings, 1, "the drop is logged once")
	require.NotEmpty(t, warnings[0].ContextMap()["last_error"], "with the failure reason")
}

func TestPollingReceiver_trackNeverReadFailurePreservesQuarantine(t *testing.T) {
	// Keep an existing Quarantined flag, but reset the stale prior offset/fingerprint.
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	cp := NewPollingCheckpoint()
	cp.UpdateProgress("b.json", BlobProgress{Quarantined: true, Offset: 42, Fingerprint: []byte("old")})
	r := &pollingReceiver{checkpoint: cp, mut: &sync.Mutex{}}

	r.trackNeverReadFailure(&azureblob.BlobInfo{Name: "b.json", LastModified: lm}, errors.New("throttled"))

	prog, ok := cp.ProgressFor("b.json")
	require.True(t, ok)
	require.True(t, prog.Quarantined, "quarantine survives a transient never-read failure")
	require.Equal(t, int64(0), prog.Offset, "stale offset reset to the never-read state")
	require.Empty(t, prog.Fingerprint, "stale fingerprint cleared")
	require.NotEmpty(t, prog.LastReadError, "failure reason recorded")
	require.Equal(t, lm, prog.LastModified)
}

func TestPollingReceiver_processBlobsSkipsFilenameMismatch(t *testing.T) {
	// A blob whose filename fails the configured pattern is skipped without a read.
	r := &pollingReceiver{
		logger:        zap.NewNop(),
		wg:            &sync.WaitGroup{},
		cfg:           &Config{FilenamePattern: `\.json$`},
		filenameRegex: regexp.MustCompile(`\.json$`),
	}
	blobs := []*azureblob.BlobInfo{{Name: "a.txt"}, {Name: "b.log"}}

	processed, skipped, parseFailed := r.processBlobs(context.Background(), blobs, time.Time{}, time.Time{})
	require.Equal(t, 0, processed)
	require.Equal(t, len(blobs), skipped, "filename-mismatched blobs are skipped")
	require.Equal(t, 0, parseFailed)
}

func TestPollingReceiver_finalizePollLogsGiveUpDrop(t *testing.T) {
	// A finalize entry past its retry budget is dropped by AgedOutProgress; the drop must be logged.
	core, logs := observer.New(zap.WarnLevel)
	start := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
	cp := NewPollingCheckpoint()
	cp.UpdateProgress("stuck.json", BlobProgress{Offset: 5, LastModified: start.Add(-quarantineRetention - 48*time.Hour), FinalizeFailures: 3})
	r := &pollingReceiver{
		logger:     zap.New(core),
		cfg:        &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
		checkpoint: cp,
		mut:        &sync.Mutex{},
	}

	r.finalizePoll(context.Background(), start, start)

	dropped := logs.FilterMessageSnippet("exhausting finalize retries").All()
	require.Len(t, dropped, 1, "the give-up drop is logged once")
	require.Equal(t, "stuck.json", dropped[0].ContextMap()["blob"])
}

func TestPollingReceiver_processBlobsCountsUnvisitedOnCancel(t *testing.T) {
	// A cancelled poll must count the unvisited blobs as skipped so listed == processed + skipped.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &pollingReceiver{logger: zap.NewNop(), wg: &sync.WaitGroup{}, cfg: &Config{}}
	blobs := []*azureblob.BlobInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}}

	processed, skipped, parseFailed := r.processBlobs(ctx, blobs, time.Time{}, time.Time{})
	require.Equal(t, 0, processed)
	require.Equal(t, len(blobs), skipped, "all unvisited blobs are counted as skipped on cancel")
	require.Equal(t, 0, parseFailed)
}

func TestPollingReceiver_neverReadGzipTrackedAndLoggedAtAgeOut(t *testing.T) {
	// The whole-tracked gzip path must not silently drop a never-read blob either: a transiently
	// failing gzip is tracked (so the drop is logged at age-out) and, because its mtime never
	// changes on its own, shouldParseBlob's LastReadError bypass is what keeps it retrying.
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	core, logs := observer.New(zap.WarnLevel)

	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "b.json.gz").Return(nil, errors.New("throttled")) // transient
	cp := NewPollingCheckpoint()
	r := &pollingReceiver{
		logger:      zap.New(core),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mc,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}

	processed, err := r.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "b.json.gz", Size: 20, LastModified: lm})
	require.Error(t, err)
	require.False(t, processed)
	prog, ok := cp.ProgressFor("b.json.gz")
	require.True(t, ok, "gzip never-read failure is tracked, not silent")
	require.Equal(t, int64(0), prog.Offset)
	require.NotEmpty(t, prog.LastReadError)

	// shouldParseBlob keeps retrying it despite the unchanged mtime.
	require.True(t, r.shouldParseBlob(&azureblob.BlobInfo{Name: "b.json.gz", LastModified: lm}, lm),
		"a never-read gzip is retried, not skipped by the mtime gate")

	r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json.gz": prog})
	require.Equal(t, 1, logs.FilterMessageSnippet("aged out without a successful read").Len(), "the drop is logged")
}

func TestPollingReceiver_recordsJSONNonObjectElementQuarantinedNotStalled(t *testing.T) {
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)

	t.Run("mid-array non-object across polls: emit before, then quarantine (not stall until seal)", func(t *testing.T) {
		content := []byte(`{"records":[{"a":1},42,{"b":2}]}`)
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
			RunAndReturn(func(_ context.Context, _ string, _ string, offset, count int64) ([]byte, int64, error) {
				return rangeOf(content, offset, count)
			})
		cp := NewPollingCheckpoint()
		sink := new(consumertest.LogsSink)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		// Poll 1: emits the record before the non-object element; not yet quarantined.
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: lm})))
		require.Equal(t, 1, sink.LogRecordCount())
		prog, _ := cp.ProgressFor("b.json")
		require.False(t, prog.Quarantined)
		require.Greater(t, prog.Offset, int64(0))

		// Poll 2: the offset is parked at ",42,..."; quarantine now rather than stall forever.
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: lm.Add(time.Minute)})))
		require.Equal(t, 1, sink.LogRecordCount(), "no records emitted after the non-object element")
		prog, _ = cp.ProgressFor("b.json")
		require.True(t, prog.Quarantined, "the stuck blob is quarantined, not silently stalled until seal")
	})

	t.Run("leading non-object element at offset 0 is quarantined", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
			Return([]byte(`{"records":[42,{"a":1}]}`), -1, nil)
		cp := NewPollingCheckpoint()
		sink := new(consumertest.LogsSink)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 24, LastModified: lm})))
		require.Equal(t, 0, sink.LogRecordCount())
		prog, ok := cp.ProgressFor("b.json")
		require.True(t, ok)
		require.True(t, prog.Quarantined)
	})
}

func TestPollingReceiver_gzipTransientFailureDoesNotClobberSuccess(t *testing.T) {
	// A gzip blob read successfully (Offset>0), then rewritten and re-read with a transient failure,
	// must keep its successful entry rather than being clobbered into a never-read one.
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	cp := NewPollingCheckpoint()
	cp.UpdateProgress("b.json.gz", BlobProgress{Offset: 100, LastModified: lm})
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "b.json.gz").Return(nil, errors.New("throttled")) // transient
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mc,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	_, err := r.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "b.json.gz", Size: 20, LastModified: lm.Add(time.Minute)})
	require.Error(t, err)
	prog, ok := cp.ProgressFor("b.json.gz")
	require.True(t, ok)
	require.Equal(t, int64(100), prog.Offset, "a prior successful entry is not clobbered by a transient failure")
	require.Empty(t, prog.LastReadError, "and not marked never-read")
}

func TestTruncateError(t *testing.T) {
	require.Equal(t, "boom", truncateError(errors.New("boom")), "a short error is unchanged")
	long := make([]byte, maxLastReadErrorLen+50)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateError(errors.New(string(long)))
	require.Equal(t, maxLastReadErrorLen+len("..."), len(got), "a long error is bounded")
	require.Equal(t, "...", got[len(got)-3:], "and marked as truncated")

	// A multi-byte rune straddling the byte cut is backed off to a rune boundary, never split.
	straddle := make([]byte, maxLastReadErrorLen-1)
	for i := range straddle {
		straddle[i] = 'x'
	}
	straddle = append(straddle, []byte("世")...) // 3 bytes; the cut lands mid-rune
	gotRune := truncateError(errors.New(string(straddle)))
	require.True(t, utf8.ValidString(gotRune), "the truncated string is valid UTF-8, not a split rune")
	require.Equal(t, "...", gotRune[len(gotRune)-3:])

	// Input invalid before the cut: the back-off gives up after UTFMax-1 tries, no infinite loop.
	preInvalid := make([]byte, maxLastReadErrorLen+50)
	for i := range preInvalid {
		preInvalid[i] = 'x'
	}
	preInvalid[10] = 0xFF // a stray invalid byte well before the cut
	gotInvalid := truncateError(errors.New(string(preInvalid)))
	require.LessOrEqual(t, len(gotInvalid), maxLastReadErrorLen+len("..."), "still bounded")
	require.Equal(t, "...", gotInvalid[len(gotInvalid)-3:], "marked truncated")
	require.False(t, utf8.ValidString(gotInvalid), "pre-existing invalid bytes are left as-is")
}

func TestPollingReceiver_processBlobsLoopCancelledPollDoesNotFinalize(t *testing.T) {
	// pullBlobs closes doneChan on its ctx-abort too, so a cancelled poll can hit the doneChan
	// arm. It must NOT finalize (that would advance LastPollTime past never-listed blobs). Loop so
	// the pseudorandom select reliably exercises the doneChan arm's cancel guard; blobChan is closed
	// too so the loop terminates via either arm (the blobChan arm never finalizes).
	for i := 0; i < 50; i++ {
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{logger: zap.NewNop(), cfg: &Config{Container: "c"}, checkpoint: cp, mut: &sync.Mutex{}, wg: &sync.WaitGroup{}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		doneChan := make(chan struct{})
		close(doneChan)
		blobChan := make(chan []*azureblob.BlobInfo)
		close(blobChan)
		r.processBlobsLoop(ctx, doneChan, make(chan error), blobChan, time.Now(), time.Now(), time.Now())
		require.True(t, cp.LastPollTime.IsZero(), "a cancelled poll must not advance the poll-time watermark")
	}
}

func TestPollingReceiver_processBlobsLoopClosedBlobChanReturns(t *testing.T) {
	// Production never closes blobChan; if a future change ever does, the loop returns WITHOUT
	// finalizing (fail-safe: never advance the watermark on a poll the producer didn't complete).
	cp := NewPollingCheckpoint()
	r := &pollingReceiver{logger: zap.NewNop(), cfg: &Config{Container: "c"}, checkpoint: cp, mut: &sync.Mutex{}, wg: &sync.WaitGroup{}}
	blobChan := make(chan []*azureblob.BlobInfo)
	close(blobChan)
	var returned atomic.Bool
	go func() {
		r.processBlobsLoop(context.Background(), make(chan struct{}), make(chan error), blobChan, time.Now(), time.Now(), time.Now())
		returned.Store(true)
	}()
	require.Eventually(t, returned.Load, 2*time.Second, 5*time.Millisecond, "loop returns on a closed blobChan")
	r.mut.Lock()
	got := cp.LastPollTime
	r.mut.Unlock()
	require.True(t, got.IsZero(), "returns without finalizing, so the watermark is not advanced")
}

func TestPollingReceiver_finalizeAgedBlobs(t *testing.T) {
	t.Run("json: flushes an unterminated final line and deletes when configured", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		sink := new(consumertest.LogsSink)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(10), int64(0)).
			Return([]byte(`{"final":true}`), -1, nil) // final line, no trailing newline
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			consumer:    blobconsume.NewNDJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 10}})
		require.Equal(t, 1, sink.LogRecordCount(), "the unterminated final line is flushed at seal")
		mockClient.AssertExpectations(t)
	})

	t.Run("json: a truncated final record is kept and quarantined, not silently dropped and deleted", func(t *testing.T) {
		// A writer that crashes mid-append leaves a truncated final JSON object. The legacy
		// whole-blob path would skip it (warn) and, under delete_on_read, delete the blob,
		// losing it silently. The incremental seal must not carry that forward: it emits the
		// complete lines, keeps the blob (no delete), and quarantines it so a later completed
		// write is picked up. Mirrors the records-json protection.
		mockClient := new(azureblob.MockBlobClient)
		sink := new(consumertest.LogsSink)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(10), int64(0)).
			Return([]byte(`{"a":1}`+"\n"+`{"c":3`), -1, nil) // one complete line + a truncated final object
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 10}})
		require.Equal(t, 1, sink.LogRecordCount(), "the complete line before the truncation is emitted")
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the blob is kept, not dropped")
		require.True(t, got.Quarantined, "and quarantined so a later completed write is re-read")
		require.Equal(t, int64(10+len(`{"a":1}`+"\n")), got.Offset, "offset advances only past the complete line")
	})

	t.Run("json: a valid non-object final line is kept and quarantined, not dropped and deleted", func(t *testing.T) {
		// A newline-less final line that is valid JSON but not an object (e.g. 42) cannot become
		// a record (the NDJSON consumer requires an object). It must be kept+quarantined like a
		// truncated record, not emitted-as-nothing and then deleted under delete_on_read.
		mockClient := new(azureblob.MockBlobClient)
		sink := new(consumertest.LogsSink)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(10), int64(0)).
			Return([]byte(`{"a":1}`+"\n"+`42`), -1, nil) // one complete object line + a valid non-object final line
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient, checkpoint: cp,
			consumer: blobconsume.NewNDJSONLogsConsumer(sink, zap.NewNop()),
			mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 10}})
		require.Equal(t, 1, sink.LogRecordCount(), "the complete object line is emitted")
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the blob is kept, not dropped")
		require.True(t, got.Quarantined, "a valid non-object final line is treated as unparseable, not dropped")
	})

	t.Run("json: a downstream consume failure on the final line re-tracks, does not delete", func(t *testing.T) {
		// The trailing valid line consumes downstream; a transient failure there must re-track
		// (retry), not delete, so the tail isn't lost.
		mc := new(azureblob.MockBlobClient)
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return([]byte(`{"b":2}`), -1, nil) // single valid line, no trailing newline
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mc,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(consumertest.NewErr(errors.New("queue full")), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 0, LastModified: time.Unix(100, 0)}})
		mc.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "re-tracked for retry, not deleted")
		require.False(t, got.Quarantined)
	})

	t.Run("text: flushes an unterminated final line per line at seal", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		sink := new(consumertest.LogsSink)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.txt", int64(10), int64(0)).
			Return([]byte("last line, no trailing newline"), -1, nil)
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.txt").Return(nil).Maybe()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatText, EnableIncrementalRead: true, EnablePerLineText: true},
			azureClient: mockClient,
			checkpoint:  NewPollingCheckpoint(),
			consumer:    blobconsume.NewLineTextLogsConsumer(sink),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.txt": {Offset: 10, LastModified: time.Unix(100, 0)}})
		require.Equal(t, 1, sink.LogRecordCount(), "the unterminated final line is flushed as one record")
	})

	t.Run("text: a downstream consume failure at seal re-tracks, does not delete", func(t *testing.T) {
		mc := new(azureblob.MockBlobClient)
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.txt", int64(0), int64(0)).
			Return([]byte("a line\n"), -1, nil)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatText, DeleteOnRead: true, EnableIncrementalRead: true, EnablePerLineText: true},
			azureClient: mc,
			checkpoint:  cp,
			consumer:    blobconsume.NewLineTextLogsConsumer(consumertest.NewErr(errors.New("queue full"))),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.txt": {Offset: 0, LastModified: time.Unix(100, 0)}})
		mc.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.txt")
		require.True(t, ok, "re-tracked for retry, not deleted")
		require.False(t, got.Quarantined)
	})

	t.Run("records-json: flushes records appended past the offset at seal", func(t *testing.T) {
		// records-json is re-read at seal, so a final record the mtime gate could not
		// re-trigger (a last write in the same clock second as the read that reached
		// the stored offset) is still emitted before the blob is retired.
		mockClient := new(azureblob.MockBlobClient)
		sink := new(consumertest.LogsSink)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
			Return([]byte(`,{"c":3}]}`), -1, nil) // one more record plus the closing footer
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 19}})
		require.Equal(t, 1, sink.LogRecordCount(), "the final same-second record is recovered at seal")
		mockClient.AssertExpectations(t)
	})

	t.Run("range at end of blob is tolerated, not logged as an error", func(t *testing.T) {
		// A well-terminated blob is fully consumed (offset == size), so the seal
		// re-read starts at EOF and Azure returns InvalidRange. That means "no new
		// bytes", not a failure: nothing is emitted and the delete still proceeds.
		mockClient := new(azureblob.MockBlobClient)
		sink := new(consumertest.LogsSink)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(42), int64(0)).
			Return(nil, -1, invalidRangeErr())
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			consumer:    blobconsume.NewNDJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 42}})
		require.Equal(t, 0, sink.LogRecordCount())
		mockClient.AssertExpectations(t)
	})

	t.Run("gzip blob is not range-flushed at seal", func(t *testing.T) {
		// Gzip blobs are read whole and never range-tailed, so seal must not issue a
		// range read (which would return compressed bytes); only the delete runs.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json.gz").Return(nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json.gz": {Offset: 100}})
		mockClient.AssertNotCalled(t, "DownloadBlobRange")
		mockClient.AssertExpectations(t)
	})

	t.Run("gzip blob with delete_on_read off is kept as Sealed, not dropped", func(t *testing.T) {
		// A gzip blob is read whole (never range-tailed). With delete off it must be re-tracked
		// as Sealed with its LastModified so the mtime gate skips it; otherwise, while its
		// path-time keeps it listed, every poll finds no progress entry and re-downloads and
		// re-emits the whole blob.
		mockClient := new(azureblob.MockBlobClient)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: false, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json.gz": {Offset: 100, LastModified: time.Unix(100, 0)}})
		mockClient.AssertNotCalled(t, "DownloadBlobRange")
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json.gz")
		require.True(t, ok, "the gzip entry is kept, not dropped")
		require.True(t, got.Sealed, "and marked Sealed so the mtime gate skips it")
		require.Equal(t, int64(100), got.Offset)
		require.Equal(t, time.Unix(100, 0), got.LastModified, "LastModified retained so the mtime gate matches")
	})

	t.Run("no delete when delete_on_read is off; the sealed entry is kept for resume", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(42), int64(0)).
			Return([]byte("   "), -1, nil) // whitespace-only tail, nothing to flush
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: false, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 42, LastModified: time.Unix(100, 0)}})
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the sealed blob's entry is kept, not dropped")
		require.True(t, got.Sealed, "and marked Sealed so a later resume reads from its offset")
		require.Equal(t, int64(42), got.Offset)
	})

	t.Run("flush error skips the delete and re-tracks the blob for retry", func(t *testing.T) {
		// A transient tail-read error must not delete the blob: doing so under
		// delete_on_read would drop the unread tail permanently. The blob's progress
		// is re-tracked so a later poll retries the flush.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(5), int64(0)).
			Return(nil, -1, errors.New("tail boom"))
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  NewPollingCheckpoint(),
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		prog := BlobProgress{Offset: 5, LastModified: time.Now().UTC()}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": prog})
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := r.checkpoint.ProgressFor("b.json")
		require.True(t, ok, "the flush-failed blob is re-tracked for a later retry")
		require.False(t, got.Quarantined, "a single failure re-tracks rather than quarantines")
		require.Equal(t, 1, got.FinalizeFailures, "a transient tail-read failure re-tracks (counts as an attempt, does not quarantine)")
	})

	t.Run("delete error after a successful flush re-tracks for a later delete retry", func(t *testing.T) {
		// The tail was ingested but DeleteBlob failed, so the blob still exists. It must be
		// re-tracked (not dropped from the checkpoint) so a later poll retries the delete;
		// otherwise the undeleted blob leaks.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return([]byte("   "), -1, nil) // whitespace-only tail: flush succeeds with nothing to emit
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(errors.New("delete boom"))
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		prog := BlobProgress{Offset: 0, LastModified: time.Unix(100, 0)}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": prog})
		mockClient.AssertExpectations(t)
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "a failed delete re-tracks the blob for retry")
		require.False(t, got.Quarantined, "a single failure re-tracks rather than quarantines")
		require.Equal(t, 1, got.FinalizeFailures, "a delete failure counts toward quarantine (it can't lose data)")
	})

	t.Run("delete of an already-gone blob is dropped, not retried", func(t *testing.T) {
		// The blob was deleted externally between flush and delete, so DeleteBlob returns 404.
		// Re-tracking would re-age and re-404 it every poll forever; drop the entry instead.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return([]byte("   "), -1, nil)
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(blobNotFoundErr())
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 0, LastModified: time.Unix(100, 0)}})
		_, ok := cp.ProgressFor("b.json")
		require.False(t, ok, "an already-gone blob is dropped, not re-tracked for a doomed retry")
	})

	t.Run("records-json: complete records stranded after a non-object element are quarantined, not deleted", func(t *testing.T) {
		// The tail frames {"a":1} then halts at the non-object element; the complete {"b":2}
		// after it is stranded. Validating only the consumed==0 case would let the blob be
		// deleted and drop {"b":2}, so the residue is checked here and the blob is kept.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
			Return([]byte(`,{"a":1},"scalar",{"b":2}]}`), -1, nil)
		cp := NewPollingCheckpoint()
		sink := new(consumertest.LogsSink)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 19, LastModified: time.Unix(100, 0)}})
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the blob with stranded records is kept")
		require.True(t, got.Quarantined, "and quarantined rather than deleted")
		require.Equal(t, 1, sink.LogRecordCount(), "the record before the corruption is still emitted")
		require.Equal(t, int64(27), got.Offset,
			"offset advances past the emitted record so a later resume of this blob won't re-emit it")
	})

	t.Run("a seal that resets to a replacement then hits an unparseable residue re-establishes the fingerprint", func(t *testing.T) {
		// The identity head no longer matches (blob replaced), so the flush resets to 0 and re-reads
		// the replacement from the start. Its tail frames {"a":1} then halts at a non-object element,
		// so the residue is unparseable: the emitted record still advances the offset, and the reset
		// must re-establish the fingerprint from the replacement's leading bytes (not drop it to nil)
		// so a later replacement of the same name is still detected.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return([]byte(`{"records":[{"new":1}`), -1, nil) // head mismatches the stored fingerprint -> reset
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return([]byte(`{"records":[{"a":1},"scalar"]}`), -1, nil) // re-read from 0: valid prefix, then junk
		cp := NewPollingCheckpoint()
		sink := new(consumertest.LogsSink)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: false, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 50, Fingerprint: []byte("STALE-OLD-FINGERPRINT"), LastModified: time.Unix(100, 0)}})
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the blob with an unparseable residue is kept")
		require.True(t, got.Quarantined, "and quarantined")
		require.Equal(t, []byte(`{"records":[{"new":1}`), got.Fingerprint, "the fingerprint is re-established from the replacement's leading bytes, not dropped to nil")
		require.Equal(t, 1, sink.LogRecordCount(), "the valid record before the junk is still emitted")
		require.Greater(t, got.Offset, int64(0), "offset advanced past the emitted prefix")
		require.Less(t, got.Offset, int64(50), "and was reset to a replacement offset, not the stale 50")
	})

	t.Run("a permanently failing sealed blob is quarantined after repeated retries", func(t *testing.T) {
		// A permanent per-blob error (a 403 after an ACL change) is re-tracked for retry, but
		// after maxFinalizeFailures polls it is quarantined so it stops erroring every poll.
		permErr := &azcore.ResponseError{ErrorCode: string(bloberror.AuthorizationPermissionMismatch)}
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(7), int64(0)).
			Return(nil, -1, permErr).Times(maxFinalizeFailures)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		prog := BlobProgress{Offset: 7, LastModified: time.Unix(100, 0)}
		for i := 1; i <= maxFinalizeFailures; i++ {
			r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": prog})
			got, ok := cp.ProgressFor("b.json")
			require.True(t, ok)
			if i < maxFinalizeFailures {
				require.False(t, got.Quarantined, "not quarantined before the threshold")
				require.Equal(t, i, got.FinalizeFailures)
			} else {
				require.True(t, got.Quarantined, "quarantined once the threshold is reached")
			}
			prog = got // feed the re-tracked progress into the next poll
		}
		mockClient.AssertNotCalled(t, "DeleteBlob")
	})

	t.Run("a transient blob-op outage never quarantines the still-readable tail", func(t *testing.T) {
		// A throttling / network blob-op failure is transient, not a blob problem. Even when it
		// spans more than maxFinalizeFailures polls it must never quarantine: the tail is still
		// readable, and quarantining would strand it permanently once the blob leaves the listing
		// window (a sealed gzip/records blob's mtime never changes to re-trigger the read).
		transientErr := &azcore.ResponseError{ErrorCode: string(bloberror.ServerBusy)}
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(7), int64(0)).
			Return(nil, -1, transientErr)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		prog := BlobProgress{Offset: 7, LastModified: time.Unix(100, 0)}
		for i := 1; i <= maxFinalizeFailures+1; i++ {
			r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": prog})
			got, ok := cp.ProgressFor("b.json")
			require.True(t, ok, "re-tracked for a later retry")
			require.False(t, got.Quarantined, "a transient outage never quarantines the blob")
			require.Equal(t, i, got.FinalizeFailures, "counts as an attempt each poll but never quarantines")
			prog = got
		}
		mockClient.AssertNotCalled(t, "DeleteBlob")
	})

	t.Run("a persistently failing delete is bounded and quarantines", func(t *testing.T) {
		// A failing DeleteBlob cannot lose data (the tail was already emitted), so unlike a
		// tail-read outage a persistent non-permanent delete error must not retry forever and
		// grow the map: it counts toward quarantine and stops. Scenario: delete_on_read where
		// DeleteBlob keeps failing on a non-permanent error (a leased blob, a 409).
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return([]byte("   "), -1, nil).Times(maxFinalizeFailures) // whitespace: flush succeeds, nothing to emit
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").
			Return(errors.New("lease id missing")).Times(maxFinalizeFailures)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		prog := BlobProgress{Offset: 0, LastModified: time.Unix(100, 0)}
		for i := 1; i <= maxFinalizeFailures; i++ {
			r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": prog})
			got, ok := cp.ProgressFor("b.json")
			require.True(t, ok)
			if i < maxFinalizeFailures {
				require.False(t, got.Quarantined, "not quarantined before the threshold")
				require.Equal(t, i, got.FinalizeFailures, "a delete failure counts toward quarantine")
			} else {
				require.True(t, got.Quarantined, "a persistent delete failure is bounded, not retried forever")
			}
			prog = got
		}
	})

	t.Run("a shutdown-cancelled finalize does not count toward quarantine", func(t *testing.T) {
		// A finalize interrupted by Shutdown fails with context.Canceled; that is not a blob
		// problem, so it re-tracks for the next run without incrementing the failure counter.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(7), int64(0)).
			Return(nil, -1, context.Canceled)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 7, LastModified: time.Unix(100, 0)}})
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "re-tracked for the next run")
		require.False(t, got.Quarantined)
		require.Equal(t, 0, got.FinalizeFailures, "a cancellation is not a blob failure")
	})

	t.Run("a downstream consume error at seal re-tracks and never quarantines", func(t *testing.T) {
		// A failing downstream (exporter outage, full sending queue) makes the seal flush's consume
		// fail. That is a downstream problem, not a blob problem, so it re-tracks for a later retry
		// and never QUARANTINES (phase flush, non-permanent), keeping the still-readable tail. Each
		// attempt does increment FinalizeFailures, so a long-enough outage still ends in the logged
		// give-up drop (settled #19); it is only the immediate quarantine that is avoided.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
			Return([]byte(`,{"c":3}]}`), -1, nil)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(consumertest.NewErr(errors.New("downstream outage")), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		prog := BlobProgress{Offset: 19, LastModified: time.Unix(100, 0)}
		for i := 1; i <= maxFinalizeFailures+1; i++ {
			r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": prog})
			got, ok := cp.ProgressFor("b.json")
			require.True(t, ok, "re-tracked for a later retry")
			require.False(t, got.Quarantined, "a downstream outage never quarantines the blob")
			require.Equal(t, i, got.FinalizeFailures, "counts as an attempt each poll but never quarantines")
			prog = got
		}
		mockClient.AssertNotCalled(t, "DeleteBlob")
	})

	t.Run("a seal-flush offset reset re-establishes the fingerprint from the replacement", func(t *testing.T) {
		// The blob was replaced: the identity head mismatches the stored fingerprint, so the
		// flush resets to 0 and re-reads the replacement. The stale fingerprint is replaced with
		// one from the replacement's own bytes, so a later retry identity-matches it (no re-emit)
		// and a further replacement is still detected.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return([]byte(`{"records":[{"new":1}`), -1, nil) // head mismatches the stored fingerprint
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return([]byte(`{"records":[{"new":1}]}`), -1, nil) // re-read from 0
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(errors.New("delete boom"))
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 50, Fingerprint: []byte("STALE-OLD-FINGERPRINT"), LastModified: time.Unix(100, 0)}})
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the failed delete re-tracks the blob")
		require.Equal(t, []byte(`{"records":[{"new":1}`), got.Fingerprint, "the fingerprint is re-established from the replacement, not dropped to nil")
	})

	t.Run("delete failure after a non-empty flush advances the offset so the retry does not re-emit", func(t *testing.T) {
		// The seal flush emits a trailing record, then DeleteBlob fails. The re-tracked entry
		// must advance past the flushed bytes; otherwise a later retry poll re-reads and
		// re-emits the same tail. Offset 19, tail ",{\"c\":3}]}" emits {"c":3} (8 bytes) -> 27.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
			Return([]byte(`,{"c":3}]}`), -1, nil)
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(errors.New("delete boom"))
		cp := NewPollingCheckpoint()
		sink := new(consumertest.LogsSink)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 19, LastModified: time.Unix(100, 0)}})
		require.Equal(t, 1, sink.LogRecordCount(), "the trailing record is emitted once")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the failed delete re-tracks for retry")
		require.Equal(t, int64(27), got.Offset, "offset advanced past the flushed tail so a retry won't re-emit it")
	})

	t.Run("assume_append_only skips the seal-time identity read", func(t *testing.T) {
		// With assume_append_only the seal flush does not issue the [0,fpSize) identity read
		// even when a fingerprint is stored; only the tail read at the stored offset happens.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
			Return([]byte(`]}`), -1, nil) // footer only: clean seal, nothing to emit
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true, AssumeAppendOnly: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		// Fingerprint present, but AssumeAppendOnly means the identity read is skipped; an
		// unexpected DownloadBlobRange(0, fpSize) would panic the mock.
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 19, Fingerprint: []byte(`{"records":[{"a":1}`), LastModified: time.Unix(100, 0)}})
		mockClient.AssertExpectations(t)
		_, ok := cp.ProgressFor("b.json")
		require.False(t, ok, "a clean seal deletes")
	})

	t.Run("blob deleted out from under the seal read is tolerated, then deleted", func(t *testing.T) {
		// A blob that vanished before the seal read returns "blob not found", which
		// means "nothing to flush" rather than a retryable failure, so the delete
		// (a harmless no-op on an already-gone blob) still runs.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(7), int64(0)).
			Return(nil, -1, blobNotFoundErr())
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  NewPollingCheckpoint(),
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 7}})
		_, ok := r.checkpoint.ProgressFor("b.json")
		require.False(t, ok, "a not-found blob is not re-tracked")
		mockClient.AssertExpectations(t)
	})

	t.Run("flag off: aged entries are neither flushed nor deleted", func(t *testing.T) {
		// With the incremental opt-in off the progress map holds only stale entries
		// from a prior run; they are dropped without any range read or delete, so a
		// flag-off legacy config never deletes a blob it did not read incrementally.
		mockClient := new(azureblob.MockBlobClient)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true}, // EnableIncrementalRead: false
			azureClient: mockClient,
			checkpoint:  NewPollingCheckpoint(),
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 5}})
		mockClient.AssertNotCalled(t, "DownloadBlobRange")
		mockClient.AssertNotCalled(t, "DeleteBlob")
	})

	t.Run("unparseable sealed records-json is kept and quarantined, not deleted", func(t *testing.T) {
		// A blob whose content never parses as records-json (here a top-level array)
		// must not be silently deleted; it is kept and quarantined so it is ignored
		// while unchanged.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return([]byte(`[{"a":1},{"b":2}]`), -1, nil)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 0}})
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the unparseable blob is kept, not dropped")
		require.True(t, got.Quarantined, "and marked quarantined")
	})

	t.Run("records-json: a footer-only sealed tail is a clean flush and deletes", func(t *testing.T) {
		// All records were consumed in earlier polls; only the "]}" footer remains at
		// seal. That is a clean seal (nothing unparsed), so the blob is deleted normally.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
			Return([]byte(`]}`), -1, nil)
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 19}})
		mockClient.AssertExpectations(t)
		_, ok := cp.ProgressFor("b.json")
		require.False(t, ok, "a cleanly sealed blob is not re-tracked")
	})

	t.Run("records-json: a valid blob with envelope keys after the array is not quarantined", func(t *testing.T) {
		// {"records":[...],"resourceId":...} is valid (Azure diagnostic exports). Its
		// records were consumed earlier (offset > 0); the tail is the array close plus the
		// envelope trailer, which must not be mistaken for unparseable content.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return([]byte(`{"records":[{"a":1}`), -1, nil) // head matches the stored fingerprint
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
			Return([]byte(`],"resourceId":"xyz","count":2}`), -1, nil)
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		fp := blobconsume.Fingerprint([]byte(`{"records":[{"a":1}`), defaultFingerprintSize)
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 19, Fingerprint: fp}})
		mockClient.AssertExpectations(t)
		_, ok := cp.ProgressFor("b.json")
		require.False(t, ok, "a valid envelope-trailer blob is deleted, not quarantined")
	})

	t.Run("a reset then a transient tail-read failure re-tracks with the replacement fingerprint", func(t *testing.T) {
		// The identity read no longer matches (blob replaced -> reset to 0), then the re-read
		// from 0 fails transiently. The retrack keeps the fingerprint re-established from the
		// replacement's head, so a later retry identity-matches it instead of re-mismatching.
		transientErr := &azcore.ResponseError{ErrorCode: string(bloberror.ServerBusy)}
		mc := new(azureblob.MockBlobClient)
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return([]byte(`{"new":"content"}`), -1, nil) // differs from the stored fingerprint -> reset
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return(nil, -1, transientErr) // re-read from 0 fails transiently
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mc,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		staleFp := blobconsume.Fingerprint([]byte(`{"old":"content"}`), defaultFingerprintSize)
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 10, Fingerprint: staleFp, LastModified: time.Unix(100, 0)}})
		mc.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "re-tracked for retry")
		require.Equal(t, []byte(`{"new":"content"}`), got.Fingerprint, "the fingerprint is re-established from the replacement's head, not dropped to nil")
		require.Equal(t, int64(0), got.Offset, "offset reset to 0")
	})

	t.Run("records-json: an offset>0 seal with a corrupted tail is quarantined, not deleted", func(t *testing.T) {
		// Regression: a resumed blob (offset > 0) whose tail past the offset is not a clean
		// array-close-plus-trailer but corrupted content must be kept and quarantined, not
		// sealed as clean and deleted under delete_on_read (which would drop trailing records).
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
			Return([]byte(`,GARBAGE-NOT-JSON`), -1, nil)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 19}})
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "the corrupted-tail blob is kept, not dropped")
		require.True(t, got.Quarantined, "and marked quarantined")
	})

	t.Run("records-json: an offset-0 seal of a valid empty-records blob is clean, not quarantined", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return([]byte(`{"records":[]}`), -1, nil)
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 0}})
		mockClient.AssertExpectations(t)
		_, ok := cp.ProgressFor("b.json")
		require.False(t, ok, "a valid (empty) records-json blob is deleted, not quarantined")
	})

	t.Run("records-json: a consumer error during seal flush re-tracks and skips delete", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
			Return([]byte(`,{"c":3}]}`), -1, nil) // a real record: consumeDelta will call Consume, which errors
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(consumertest.NewErr(errors.New("downstream boom")), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 19}})
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "a transient flush error re-tracks for retry")
		require.False(t, got.Quarantined, "a transient error does not quarantine")
	})

	t.Run("json: a consumer error during seal flush re-tracks and skips delete", func(t *testing.T) {
		// The line-format seal path emits the tail via consume; a downstream error
		// must re-track (transient) and skip the delete, not drop the tail.
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(7), int64(0)).
			Return([]byte(`{"k":1}`+"\n"), -1, nil) // valid NDJSON so the erroring consumer is invoked
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(consumertest.NewErr(errors.New("downstream boom")), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 7}})
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "a transient flush error re-tracks for retry")
		require.False(t, got.Quarantined, "a transient error does not quarantine")
	})

	t.Run("seal identity mismatch re-reads the replacement from byte 0, not the stale offset", func(t *testing.T) {
		// The blob at the stored offset was replaced (its path-time left the window, so
		// the change was never observed). The head no longer matches the stored
		// fingerprint, so the seal reads the replacement from byte 0 rather than emitting
		// stale mid-file bytes as records.
		mockClient := new(azureblob.MockBlobClient)
		sink := new(consumertest.LogsSink)
		newContent := []byte("{\"new\":1}\n{\"new\":2}\n")
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return(newContent, -1, nil) // head of the replacement, won't match the stored fingerprint
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return(newContent, -1, nil) // re-read from 0 after the mismatch
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  NewPollingCheckpoint(),
			consumer:    blobconsume.NewNDJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{
			"b.json": {Offset: 50, Fingerprint: []byte("stale-fingerprint-of-the-old-blob")},
		})
		require.Equal(t, 2, sink.LogRecordCount(), "the replacement's real records are emitted, not stale mid-file bytes")
		mockClient.AssertExpectations(t)
	})

	t.Run("seal identity read returning not-found is tolerated, then deletes", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return(nil, -1, blobNotFoundErr())
		mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  NewPollingCheckpoint(),
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": {Offset: 50, Fingerprint: []byte("fp")}})
		mockClient.AssertExpectations(t)
	})

	t.Run("seal identity read hard error re-tracks and skips delete", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return(nil, -1, errors.New("head boom"))
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON, DeleteOnRead: true, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		prog := BlobProgress{Offset: 50, Fingerprint: []byte("fp")}
		r.finalizeAgedBlobs(context.Background(), map[string]BlobProgress{"b.json": prog})
		mockClient.AssertNotCalled(t, "DeleteBlob")
		got, ok := cp.ProgressFor("b.json")
		require.True(t, ok, "a transient identity-read error re-tracks for retry")
		require.False(t, got.Quarantined, "a single failure re-tracks rather than quarantines")
		require.Equal(t, 1, got.FinalizeFailures, "a transient identity-read failure re-tracks (counts as an attempt, does not quarantine)")
	})
}

func TestPollingReceiver_finalizePoll_sealedEntrySurvivesLongRevisitWindow(t *testing.T) {
	// With a revisit window longer than the 7-day quarantine retention, a Sealed entry must
	// not be pruned while its blob is still within the listing window. Pruning it drops the
	// stored offset, so the next poll that still lists the blob re-reads it whole from byte 0
	// (duplicate delivery), re-seals, and repeats. The prune cutoff must be at least the
	// revisit window.
	cp := NewPollingCheckpoint()
	endingTime := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// Sealed 8 days ago: past the 7-day quarantineRetention, but well inside a 10-day window.
	cp.UpdateProgress("b.json", BlobProgress{Offset: 100, LastModified: endingTime.Add(-8 * 24 * time.Hour), Sealed: true})
	r := &pollingReceiver{
		logger:          zap.NewNop(),
		cfg:             &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		checkpoint:      cp,
		checkpointStore: storageclient.NewNopStorage(),
		revisitWindow:   10 * 24 * time.Hour,
		mut:             &sync.Mutex{},
		wg:              &sync.WaitGroup{},
	}
	r.finalizePoll(context.Background(), endingTime.Add(-r.revisitWindow), endingTime)
	_, ok := cp.ProgressFor("b.json")
	require.True(t, ok, "the sealed entry is retained while its blob is still within the revisit window")
}

func TestPollingReceiver_finalizePoll_prunesSealedPastListingHorizon(t *testing.T) {
	endingTime := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// Sealed 3h ago: past the 2h revisit window but far younger than the 7-day retention.
	sealedAt := endingTime.Add(-3 * time.Hour)
	newR := func(useLastModified bool) *pollingReceiver {
		cp := NewPollingCheckpoint()
		cp.UpdateProgress("b.json", BlobProgress{Offset: 100, LastModified: sealedAt, Sealed: true})
		return &pollingReceiver{
			logger:          zap.NewNop(),
			cfg:             &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true, UseLastModified: useLastModified},
			checkpoint:      cp,
			checkpointStore: storageclient.NewNopStorage(),
			revisitWindow:   2 * time.Hour,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}
	}

	t.Run("path-time mode prunes it: the blob can never be re-listed", func(t *testing.T) {
		r := newR(false)
		r.finalizePoll(context.Background(), endingTime.Add(-r.revisitWindow), endingTime)
		_, ok := r.checkpoint.ProgressFor("b.json")
		require.False(t, ok, "a sealed blob past the listing horizon is pruned in path-time mode")
	})

	t.Run("use_last_modified keeps it as a resume anchor within retention", func(t *testing.T) {
		r := newR(true)
		r.finalizePoll(context.Background(), endingTime.Add(-r.revisitWindow), endingTime)
		_, ok := r.checkpoint.ProgressFor("b.json")
		require.True(t, ok, "use_last_modified keeps the sealed entry: a changed mtime can re-list the blob")
	})
}

func TestPollingReceiver_processBlobIncremental_NonObjectRecordDoesNotWedge(t *testing.T) {
	// A records-json blob with a non-object element must not error (an error would leave
	// no progress entry and re-read the blob from 0 every poll). Consumption halts at the
	// scalar with no error; progress is recorded so the mtime gate throttles it.
	sink := new(consumertest.LogsSink)
	cp := NewPollingCheckpoint()
	content := []byte(`{"records":[123,{"a":1}]}`)
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).Return(content, -1, nil)
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mc,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: time.Now().UTC()})))
	require.Equal(t, 0, sink.LogRecordCount(), "nothing emitted; the leading scalar halts consumption")
	_, ok := cp.ProgressFor("b.json")
	require.True(t, ok, "progress is recorded so the blob is throttled, not re-read every poll")
}

func TestPollingReceiver_processBlobIncremental_ErrorPaths(t *testing.T) {
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)

	t.Run("fingerprint read error is returned", func(t *testing.T) {
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return(nil, -1, errors.New("head boom"))
		cp := NewPollingCheckpoint()
		cp.UpdateProgress("b.json", BlobProgress{Offset: 5, Fingerprint: []byte("fp"), LastModified: lm})
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		_, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 20, LastModified: lm.Add(time.Minute)})
		require.ErrorContains(t, err, "fingerprint read")
	})

	for _, tc := range []struct {
		name    string
		format  BlobFormat
		content []byte
		newCons func(consumertest.Consumer) blobconsume.Consumer
	}{
		{"ndjson consumer error", BlobFormatJSON, []byte("{\"a\":1}\n"), func(c consumertest.Consumer) blobconsume.Consumer {
			return blobconsume.NewNDJSONLogsConsumer(c, zap.NewNop())
		}},
		{"text consumer error", BlobFormatText, []byte("a line\n"), func(c consumertest.Consumer) blobconsume.Consumer {
			return blobconsume.NewLineTextLogsConsumer(c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := new(azureblob.MockBlobClient)
			mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
				Return(tc.content, -1, nil)
			r := &pollingReceiver{
				logger:      zap.NewNop(),
				cfg:         &Config{Container: "c", BlobFormat: tc.format, EnableIncrementalRead: true, EnablePerLineText: tc.format == BlobFormatText},
				azureClient: mockClient,
				checkpoint:  NewPollingCheckpoint(),
				consumer:    tc.newCons(consumertest.NewErr(errors.New("downstream boom"))),
				mut:         &sync.Mutex{},
				wg:          &sync.WaitGroup{},
			}
			_, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(tc.content)), LastModified: lm})
			require.ErrorContains(t, err, "consume")
		})
	}
}

func TestPollingReceiver_processBlobIncremental_RangeNotSatisfiableTolerated(t *testing.T) {
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)

	t.Run("delta read past a fully consumed blob is a no-op that advances LastModified", func(t *testing.T) {
		// The blob was consumed to its end (offset == size). A metadata-only touch
		// bumps LastModified without adding bytes, so the delta read at EOF returns
		// InvalidRange. That means "no new bytes": nothing is emitted, the offset is
		// unchanged, and LastModified advances so the mtime gate does not re-fire.
		content := []byte(`{"records":[{"a":1}]}`)
		fp := blobconsume.Fingerprint(content, defaultFingerprintSize)
		cp := NewPollingCheckpoint()
		cp.UpdateProgress("b.json", BlobProgress{Offset: int64(len(content)), Fingerprint: fp, LastModified: lm})

		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return(content, -1, nil) // fingerprint read: the blob is not gone
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(len(content)), int64(0)).
			Return(nil, -1, invalidRangeErr()) // delta read at EOF

		sink := new(consumertest.LogsSink)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		newMod := lm.Add(time.Minute)
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: newMod})))
		require.Equal(t, 0, sink.LogRecordCount(), "no new bytes means nothing emitted")
		prog, _ := cp.ProgressFor("b.json")
		require.Equal(t, int64(len(content)), prog.Offset, "offset is unchanged")
		require.Equal(t, newMod, prog.LastModified, "LastModified advances so the next poll's mtime gate skips the blob")
	})

	t.Run("fingerprint read returning InvalidRange resets the offset to zero", func(t *testing.T) {
		// The blob was truncated or emptied below the stored offset, so the fingerprint
		// read past its end returns InvalidRange. Its empty head cannot match the stored
		// fingerprint, so the blob restarts from byte 0 and its current content is read.
		cp := NewPollingCheckpoint()
		cp.UpdateProgress("b.json", BlobProgress{Offset: 100, Fingerprint: []byte("old-fingerprint"), LastModified: lm})

		newContent := []byte(`{"records":[{"z":9}]}`)
		mockClient := new(azureblob.MockBlobClient)
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return(nil, -1, invalidRangeErr())
		mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return(newContent, -1, nil) // delta read after the reset

		sink := new(consumertest.LogsSink)
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(newContent)), LastModified: lm.Add(time.Minute)})))
		require.Equal(t, 1, sink.LogRecordCount(), "the replacement blob is read from the start")
		prog, _ := cp.ProgressFor("b.json")
		// records-json never consumes the closing "]}" footer, so the offset lands just
		// after the last record, not at the blob's full length.
		require.Equal(t, int64(len(newContent)-len("]}")), prog.Offset)
	})
}

func TestPollingReceiver_processBlobIncremental_QuarantinesNonJSONExtension(t *testing.T) {
	// The JSON formats expect .json blobs; a non-.json blob is quarantined (no download,
	// no error, ignored while unchanged) rather than re-erroring every poll.
	for _, format := range []BlobFormat{BlobFormatRecordsJSON, BlobFormatJSON} {
		mockClient := new(azureblob.MockBlobClient)
		cp := NewPollingCheckpoint()
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: format, EnableIncrementalRead: true},
			azureClient: mockClient,
			checkpoint:  cp,
			consumer:    blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
		lm := time.Now().UTC()
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "data.csv", Size: 10, LastModified: lm})))
		mockClient.AssertNotCalled(t, "DownloadBlobRange")
		got, ok := cp.ProgressFor("data.csv")
		require.True(t, ok, "the unsupported blob is tracked so the mtime gate throttles it")
		require.True(t, got.Quarantined)
		require.Equal(t, lm, got.LastModified)
	}
}

func TestPollingReceiver_processBlobIncremental_UppercaseJSONExtensionAccepted(t *testing.T) {
	// The .json allowlist is case-insensitive (matching useIncremental and the whole-blob path):
	// an uppercase .JSON blob is read, not quarantined as an unsupported type.
	content := []byte(`{"records":[{"a":1}]}`)
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "flow/PT1H.JSON", int64(0), int64(0)).Return(content, -1, nil)
	cp := NewPollingCheckpoint()
	sink := new(consumertest.LogsSink)
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mc, checkpoint: cp,
		consumer: blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
	}
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "flow/PT1H.JSON", Size: int64(len(content)), LastModified: time.Now().UTC()})))
	require.Equal(t, 1, sink.LogRecordCount(), "the uppercase-extension blob is ingested, not quarantined")
	got, _ := cp.ProgressFor("flow/PT1H.JSON")
	require.False(t, got.Quarantined, "an uppercase .JSON is a supported type on the incremental path")
}

func TestPollingReceiver_processBlobIncremental_QuarantineWarnsOncePerBlob(t *testing.T) {
	// A growing excluded blob is re-listed with a new mtime every poll, so it re-enters the
	// quarantine path each time. The warn must fire only on the transition into quarantine,
	// not on every poll, or the log fills with the same line.
	core, logs := observer.New(zapcore.WarnLevel)
	mockClient := new(azureblob.MockBlobClient)
	cp := NewPollingCheckpoint()
	r := &pollingReceiver{
		logger:      zap.New(core),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mockClient,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	lm := time.Now().UTC()
	for i := 0; i < 3; i++ {
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(),
			&azureblob.BlobInfo{Name: "data.csv", Size: int64(10 + i), LastModified: lm.Add(time.Duration(i) * time.Minute)})))
	}
	require.Equal(t, 1, logs.FilterMessage("Ignoring unsupported file type on the incremental path").Len(),
		"the unsupported-type warning fires once, not once per poll")
	got, ok := cp.ProgressFor("data.csv")
	require.True(t, ok)
	require.True(t, got.Quarantined, "the blob stays quarantined across polls")
}

func TestPollingReceiver_processBlobGoRoutine_ProcessedCount(t *testing.T) {
	// The poll summary's processed count should reflect blobs that actually emitted records,
	// not no-ops: a quarantined wrong-extension blob is handled but must not be counted.
	base := func(mc azureblob.BlobClient) *pollingReceiver {
		return &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
			azureClient: mc,
			checkpoint:  NewPollingCheckpoint(),
			consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:         &sync.Mutex{},
			wg:          &sync.WaitGroup{},
		}
	}

	t.Run("a blob that emits a record is counted", func(t *testing.T) {
		content := `{"records":[{"a":1}]}`
		mc := new(azureblob.MockBlobClient)
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).Return([]byte(content), -1, nil)
		r := base(mc)
		var count atomic.Int64
		bt := time.Unix(100, 0)
		r.wg.Add(1)
		r.processBlobGoRoutine(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: bt}, &bt, &count)
		require.Equal(t, int64(1), count.Load())
	})

	t.Run("a quarantined wrong-extension blob is not counted", func(t *testing.T) {
		mc := new(azureblob.MockBlobClient)
		r := base(mc)
		var count atomic.Int64
		bt := time.Unix(100, 0)
		r.wg.Add(1)
		r.processBlobGoRoutine(context.Background(), &azureblob.BlobInfo{Name: "data.csv", Size: 10, LastModified: bt}, &bt, &count)
		require.Equal(t, int64(0), count.Load(), "a no-op blob is handled but not counted as processed")
		mc.AssertNotCalled(t, "DownloadBlobRange")
	})
}

func TestPollingReceiver_shouldParseBlob_skipsZeroLastModified(t *testing.T) {
	r := &pollingReceiver{
		logger:     zap.NewNop(),
		cfg:        &Config{BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		checkpoint: NewPollingCheckpoint(),
		mut:        &sync.Mutex{},
	}
	require.False(t, r.shouldParseBlob(&azureblob.BlobInfo{Name: "b.json"}, time.Time{}),
		"a zero LastModified blob is skipped on the incremental path")
	require.True(t, r.shouldParseBlob(&azureblob.BlobInfo{Name: "b.json", LastModified: time.Now().UTC()}, time.Time{}),
		"a blob with a real LastModified is parsed")
}

func TestPollingReceiver_processBlobIncremental_SmallRecordsJSONNoDuplicate(t *testing.T) {
	// A small records-json blob (< fingerprint size) must not re-emit its earlier
	// records when it grows: the fingerprint covers only the committed prefix, never
	// the "]}" footer the writer rewrites on each append.
	sink := new(consumertest.LogsSink)
	cp := NewPollingCheckpoint()
	var mu sync.Mutex
	content := []byte(`{"records":[{"a":1}]}`)
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ string, offset, count int64) ([]byte, int64, error) {
			mu.Lock()
			defer mu.Unlock()
			return rangeOf(content, offset, count)
		})
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mc,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: lm})))
	require.Equal(t, 1, sink.LogRecordCount())

	mu.Lock()
	content = []byte(`{"records":[{"a":1},{"b":2}]}`)
	mu.Unlock()
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: lm.Add(time.Minute)})))
	require.Equal(t, 2, sink.LogRecordCount(), "the first record is not re-emitted when the small blob grows")
}

func TestPollingReceiver_processBlobIncremental_StaleHeadNoDuplicate(t *testing.T) {
	// When the writer appends between a poll's head read and its delta read, the head
	// is stale (still shows the old "]}" footer). Growing the fingerprint from
	// head[:newOffset] would bake that stale footer into the committed prefix, so the
	// next poll's identity read mismatches, resets the offset, and re-emits every record.
	// The fingerprint must instead grow from head[:offset] (stable committed prefix) plus
	// delta[:consumed] (the real new bytes).
	sink := new(consumertest.LogsSink)
	cp := NewPollingCheckpoint()
	var mu sync.Mutex
	preAppend := []byte(`{"records":[{"a":1}]}`)          // 21 bytes, footer at [19:21]
	postAppend := []byte(`{"records":[{"a":1},{"b":2}]}`) // 29 bytes
	// head/identity reads (count>0) and delta reads (count==0) can see different snapshots
	// within one poll, modeling a mid-poll append.
	head, delta := preAppend, preAppend
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ string, offset, count int64) ([]byte, int64, error) {
			mu.Lock()
			defer mu.Unlock()
			if count > 0 {
				return rangeOf(head, offset, count)
			}
			return rangeOf(delta, offset, count)
		})
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mc,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)

	// Poll 1: fresh blob, offset 0 (delta read only). Emits {"a":1}, offset -> 19.
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(preAppend)), LastModified: lm})))
	require.Equal(t, 1, sink.LogRecordCount())

	// Poll 2: head read sees the stale pre-append snapshot; delta read sees the fresh
	// append. Emits {"b":2}.
	mu.Lock()
	head, delta = preAppend, postAppend
	mu.Unlock()
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(postAppend)), LastModified: lm.Add(time.Minute)})))
	require.Equal(t, 2, sink.LogRecordCount())

	// Poll 3: both reads see the settled post-append blob. A footer-contaminated
	// fingerprint from poll 2 would fail the identity check here, reset the offset, and
	// re-emit {"a":1} and {"b":2}.
	mu.Lock()
	head, delta = postAppend, postAppend
	mu.Unlock()
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(postAppend)), LastModified: lm.Add(2 * time.Minute)})))
	require.Equal(t, 2, sink.LogRecordCount(), "no records are re-emitted after a stale-head poll")
}

func TestPollingReceiver_processBlobIncremental_NoRecordDeltaNotCounted(t *testing.T) {
	// A delta that advances the offset without emitting a real record (a records-json
	// footer, or a whitespace-only line) must report processed=false so the poll summary's
	// blob count excludes it.
	lm := time.Unix(100, 0)
	newR := func(format BlobFormat, cons blobconsume.Consumer) (*pollingReceiver, *azureblob.MockBlobClient) {
		mc := new(azureblob.MockBlobClient)
		cp := NewPollingCheckpoint()
		cp.UpdateProgress("b.json", BlobProgress{Offset: 19, Fingerprint: blobconsume.Fingerprint([]byte(`{"records":[{"a":1}`), defaultFingerprintSize), LastModified: lm})
		r := &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: format, EnableIncrementalRead: true, AssumeAppendOnly: true},
			azureClient: mc, checkpoint: cp, consumer: cons,
			mut: &sync.Mutex{}, wg: &sync.WaitGroup{},
		}
		return r, mc
	}

	t.Run("records-json footer-only delta", func(t *testing.T) {
		r, mc := newR(BlobFormatRecordsJSON, blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()))
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).Return([]byte(`]}`), -1, nil)
		processed, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 21, LastModified: lm.Add(time.Minute)})
		require.NoError(t, err)
		require.False(t, processed, "advancing past the footer emits no record")
	})

	t.Run("whitespace-only line delta", func(t *testing.T) {
		r, mc := newR(BlobFormatJSON, blobconsume.NewNDJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()))
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).Return([]byte("   \n"), -1, nil)
		processed, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 23, LastModified: lm.Add(time.Minute)})
		require.NoError(t, err)
		require.False(t, processed, "a whitespace-only line emits no record")
	})
}

func TestPollingReceiver_processBlobIncremental_NilFingerprintRevivalResumesAtOffset(t *testing.T) {
	// A seal-time reset+quarantine can leave a nonzero offset with a nil fingerprint. When
	// that entry revives through the normal path, it must resume at the stored offset (as
	// flushSealedBlob does with the same guard), not reset to 0 and re-emit delivered records.
	content := []byte(`{"records":[{"a":1},{"b":2}]}`) // committed prefix through {"a":1} is 19 bytes
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ string, offset, count int64) ([]byte, int64, error) {
			return rangeOf(content, offset, count)
		})
	cp := NewPollingCheckpoint()
	cp.UpdateProgress("b.json", BlobProgress{Offset: 19, Fingerprint: nil, LastModified: time.Unix(100, 0), Quarantined: true})
	sink := new(consumertest.LogsSink)
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mc, checkpoint: cp,
		consumer: blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
	}
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: time.Unix(200, 0)})))
	require.Equal(t, 1, sink.LogRecordCount(), "only the record past the stored offset is emitted, no re-read from 0")
	got, _ := cp.ProgressFor("b.json")
	require.Equal(t, int64(27), got.Offset, "offset advances from 19, it is not reset to 0")
}

func TestPollingReceiver_processBlobIncremental_DetectsReplacementAfterSealReset(t *testing.T) {
	// A seal-time reset re-establishes a fingerprint from the replacement (never nil at a
	// nonzero offset). This locks the consequence: a LATER replacement of the same name is still
	// detected — the identity read runs, mismatches, resets to 0, and re-reads the new content —
	// instead of silently resuming at a stale offset (which a nil fingerprint would cause).
	sink := new(consumertest.LogsSink)
	cp := NewPollingCheckpoint()
	firstReplacementFp := blobconsume.Fingerprint([]byte(`{"records":[{"gen":2}`), defaultFingerprintSize)
	cp.UpdateProgress("b.json", BlobProgress{Offset: 40, Fingerprint: firstReplacementFp, LastModified: time.Unix(100, 0), Sealed: true})
	thirdGen := []byte(`{"records":[{"gen":3}]}`) // committed prefix {"records":[{"gen":3} is 21 bytes
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).Return(thirdGen, -1, nil) // identity read
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).Return(thirdGen, -1, nil)                      // re-read from 0 after reset
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mc, checkpoint: cp,
		consumer: blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
	}
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(thirdGen)), LastModified: time.Unix(200, 0)})))
	require.Equal(t, 1, sink.LogRecordCount(), "the further replacement is detected and re-read from 0")
	got, _ := cp.ProgressFor("b.json")
	require.Equal(t, int64(21), got.Offset, "offset reflects the re-read replacement's committed prefix, not the stale 40")
}

func TestPollingReceiver_processBlobIncremental_ShorterReplacementSharedPrefixResets(t *testing.T) {
	// The fingerprint only sees the leading fpSize bytes, so a replacement that shares that prefix
	// but is shorter than the stored offset slips past SameBlob. Without the authoritative size from
	// the identity read, the offset stays parked past the replacement's end and its records drop.
	const fpSize = 16
	replacement := []byte(`{"records":[{"x":9}]}`) // 21 bytes; first 16 match the stored fingerprint
	sink := new(consumertest.LogsSink)
	cp := NewPollingCheckpoint()
	cp.UpdateProgress("b.json", BlobProgress{Offset: 30, Fingerprint: replacement[:fpSize], LastModified: time.Unix(100, 0)})
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ string, offset, count int64) ([]byte, int64, error) {
			return rangeOf(replacement, offset, count)
		})
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true, FingerprintSize: fpSize},
		azureClient: mc, checkpoint: cp,
		consumer: blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
	}
	processed, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: int64(len(replacement)), LastModified: time.Unix(200, 0)})
	require.NoError(t, err)
	require.True(t, processed, "the shorter replacement is re-read from 0, not skipped")
	require.Equal(t, 1, sink.LogRecordCount(), "the replacement's record is emitted")
	got, _ := cp.ProgressFor("b.json")
	require.Equal(t, int64(19), got.Offset, "reset to 0, then advanced over the committed prefix (the ]} seal residue is uncommitted)")
}

func TestPollingReceiver_processBlobIncremental_FingerprintGrowOffsetPastHead(t *testing.T) {
	// Regression: raising fingerprint_size, or turning assume_append_only off, leaves a
	// short stored fingerprint while the offset is already past the head window (head is at
	// most fingerprint_size bytes). Growing the fingerprint must not slice head[:offset] out
	// of bounds — the whole head is committed at that point, so the fingerprint is the head.
	fpSize := 512
	fp64 := bytes.Repeat([]byte("a"), 64)
	head := append(append([]byte{}, fp64...), bytes.Repeat([]byte("b"), fpSize-64)...) // 512 bytes; first 64 == stored fp
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(fpSize)).Return(head, -1, nil)           // identity read
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(600), int64(0)).Return(nil, -1, invalidRangeErr()) // no new bytes past the offset
	cp := NewPollingCheckpoint()
	cp.UpdateProgress("b.json", BlobProgress{Offset: 600, Fingerprint: fp64, LastModified: time.Unix(100, 0)})
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true, FingerprintSize: fpSize},
		azureClient: mc, checkpoint: cp,
		consumer: blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
	}
	require.NotPanics(t, func() {
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 4096, LastModified: time.Unix(200, 0)})))
	})
	got, _ := cp.ProgressFor("b.json")
	require.Equal(t, fpSize, len(got.Fingerprint), "fingerprint grows to full size from the committed head")
	require.Equal(t, head, got.Fingerprint, "fingerprint is the head's committed prefix")
}

func TestPollingReceiver_processBlobIncremental_FingerprintGrowsOnStaleHead(t *testing.T) {
	// Regression (R23): when the blob grows between the head read and the delta read so the head
	// read returns exactly `offset` bytes (offset == len(head)) while the delta still brings new
	// committed bytes, the fingerprint must incorporate delta[:consumed], not just the stale head.
	// The reconstruction is fully possible there (head[:offset] is the whole head), so the grow
	// branch must run rather than falling back to head alone (the offset<len(head) off-by-one).
	sink := new(consumertest.LogsSink)
	storedFP := []byte(`{"records":[{"a":1}`) // 19 bytes, shorter than the default fingerprint size
	mc := new(azureblob.MockBlobClient)
	// Head read at offset 0 sees only the 19 bytes committed so far (no growth observed yet).
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).Return(storedFP, -1, nil)
	// The later delta read sees the append that landed between the two reads.
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).Return([]byte(`,{"b":2}]}`), -1, nil)
	cp := NewPollingCheckpoint()
	cp.UpdateProgress("b.json", BlobProgress{Offset: 19, Fingerprint: storedFP, LastModified: time.Unix(100, 0)})
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mc, checkpoint: cp,
		consumer: blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
	}
	processed, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 29, LastModified: time.Unix(200, 0)})
	require.NoError(t, err)
	require.True(t, processed, "the appended record is emitted")
	got, _ := cp.ProgressFor("b.json")
	require.Greater(t, len(got.Fingerprint), len(storedFP), "fingerprint incorporates the newly-committed delta bytes, not just the stale head")
	require.True(t, bytes.HasPrefix(got.Fingerprint, storedFP), "the grown fingerprint still starts with the committed head")
}

func TestPollingReceiver_legacyPath_DeletesOnRead(t *testing.T) {
	// The legacy (non-incremental) whole-read path deletes the blob right after a successful read
	// when delete_on_read is set, the contract the README documents. The incremental/seal path
	// deletes elsewhere (finalizeAgedBlobs), so this pins the legacy path specifically.
	otlp := []byte(`{"resourceLogs":[]}`)
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlob(mock.Anything, "c", "flow/otlp.json", mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ string, buf []byte) (int64, error) {
			return int64(copy(buf, otlp)), nil
		})
	mc.EXPECT().DeleteBlob(mock.Anything, "c", "flow/otlp.json").Return(nil)
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatOTLP, DeleteOnRead: true},
		azureClient: mc,
		checkpoint:  NewPollingCheckpoint(),
		consumer:    blobconsume.NewLogsConsumer(new(consumertest.LogsSink)),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	blobTime := time.Unix(100, 0)
	var count atomic.Int64
	r.wg.Add(1)
	r.processBlobGoRoutine(context.Background(), &azureblob.BlobInfo{Name: "flow/otlp.json", Size: int64(len(otlp))}, &blobTime, &count)
	mc.AssertExpectations(t) // DeleteBlob was called after the whole read
}

func TestPollingReceiver_warnsWhenNoBlobParses(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	newR := func(logger *zap.Logger, cfg *Config) (*pollingReceiver, *azureblob.MockBlobClient) {
		mc := new(azureblob.MockBlobClient)
		return &pollingReceiver{
			logger:             logger,
			cfg:                cfg,
			azureClient:        mc,
			checkpoint:         NewPollingCheckpoint(),
			checkpointStore:    storageclient.NewNopStorage(),
			supportedTelemetry: pipeline.SignalLogs,
			consumer:           blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:                &sync.Mutex{},
			wg:                 &sync.WaitGroup{},
			nowFn:              func() time.Time { return now },
			initialLookback:    time.Hour,
			revisitWindow:      time.Hour,
		}, mc
	}
	feed := func(mc *azureblob.MockBlobClient, blobs []*azureblob.BlobInfo) {
		mc.EXPECT().StreamBlobs(mock.Anything, "c", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, _ string, _ *string, _ chan error, blobChan chan []*azureblob.BlobInfo, doneChan chan struct{}) {
				blobChan <- blobs
				close(doneChan)
			})
	}

	t.Run("warns in default path mode when blob paths don't match the layout", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		r, mc := newR(zap.New(core), &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON})
		feed(mc, []*azureblob.BlobInfo{{Name: "app.log", LastModified: now}, {Name: "logs/today.log", LastModified: now}})
		r.runPoll(context.Background())
		require.Equal(t, 1, logs.FilterMessageSnippet("parseable time").Len(), "warns about the likely time-source misconfiguration")
	})

	t.Run("warns in time_pattern mode when blob names don't match the pattern", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		r, mc := newR(zap.New(core), &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, TimePattern: "y={year}/m={month}/d={day}/h={hour}"})
		feed(mc, []*azureblob.BlobInfo{{Name: "app.log", LastModified: now}})
		r.runPoll(context.Background())
		require.Equal(t, 1, logs.FilterMessageSnippet("parseable time").Len(), "an unparseable time_pattern is also a misconfiguration")
	})

	t.Run("no warn on an empty poll (nothing parse-failed)", func(t *testing.T) {
		core, logs := observer.New(zap.WarnLevel)
		r, mc := newR(zap.New(core), &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON})
		feed(mc, nil)
		r.runPoll(context.Background())
		require.Zero(t, logs.FilterMessageSnippet("parseable time").Len(), "an empty poll is normal, not a misconfiguration")
	})
}

func TestPollingReceiver_pollSummary_readButEmptyIsNotSkipped(t *testing.T) {
	// A blob that is read incrementally but has no new complete records this poll must not be
	// counted as "skipped" in the poll summary (skipped is for genuinely filtered blobs).
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().StreamBlobs(mock.Anything, "c", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ string, _ *string, _ chan error, blobChan chan []*azureblob.BlobInfo, doneChan chan struct{}) {
			blobChan <- []*azureblob.BlobInfo{{Name: "b.json", Size: 14, LastModified: now}}
			close(doneChan)
		})
	// An empty records array: the blob is read but yields no records to emit.
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		Return([]byte(`{"records":[]}`), -1, nil)
	core, logs := observer.New(zap.InfoLevel)
	r := &pollingReceiver{
		logger:             zap.New(core),
		cfg:                &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true, UseLastModified: true},
		azureClient:        mc,
		checkpoint:         NewPollingCheckpoint(),
		checkpointStore:    storageclient.NewNopStorage(),
		supportedTelemetry: pipeline.SignalLogs,
		consumer:           blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:                &sync.Mutex{},
		wg:                 &sync.WaitGroup{},
		nowFn:              func() time.Time { return now },
		initialLookback:    time.Hour,
		revisitWindow:      time.Hour,
	}
	r.runPoll(context.Background())
	done := logs.FilterMessage("Poll completed").All()
	require.Len(t, done, 1)
	ctx := done[0].ContextMap()
	require.Equal(t, int64(1), ctx["total_listed"])
	require.Equal(t, int64(0), ctx["total_processed"])
	require.Equal(t, int64(0), ctx["total_skipped"], "a read-but-empty blob is neither processed nor skipped")
}

func TestPollingReceiver_processBlobWholeTracked_PermanentDownloadQuarantines(t *testing.T) {
	// A permanent stream-download error (auth/config) on the whole-blob path must quarantine, not
	// be treated as transient and retried every poll (a gzip mtime never changes to re-trigger it).
	// A transient error still surfaces for retry.
	permErr := &azcore.ResponseError{ErrorCode: string(bloberror.AuthorizationPermissionMismatch)}
	transientErr := &azcore.ResponseError{ErrorCode: string(bloberror.ServerBusy)}
	lm := time.Unix(100, 0)
	newR := func() (*pollingReceiver, *azureblob.MockBlobClient) {
		mc := new(azureblob.MockBlobClient)
		return &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON},
			azureClient: mc, checkpoint: NewPollingCheckpoint(),
			consumer: blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
		}, mc
	}
	t.Run("permanent download error quarantines", func(t *testing.T) {
		r, mc := newR()
		mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "b.json.gz").Return(nil, permErr)
		processed, err := r.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "b.json.gz", LastModified: lm})
		require.NoError(t, err)
		require.False(t, processed)
		got, _ := r.checkpoint.ProgressFor("b.json.gz")
		require.True(t, got.Quarantined)
	})
	t.Run("transient download error is retried", func(t *testing.T) {
		r, mc := newR()
		mc.EXPECT().DownloadBlobStream(mock.Anything, "c", "b.json.gz").Return(nil, transientErr)
		processed, err := r.processBlobWholeTracked(context.Background(), &azureblob.BlobInfo{Name: "b.json.gz", LastModified: lm})
		require.Error(t, err)
		require.False(t, processed)
		got, ok := r.checkpoint.ProgressFor("b.json.gz")
		require.False(t, ok && got.Quarantined)
	})
}

func TestPollingReceiver_processBlobIncremental_PermanentReadErrorQuarantines(t *testing.T) {
	// A permanent read error (auth/config) on the normal incremental path would re-error
	// every poll. It must be quarantined (bounded) instead, as the seal path already does,
	// while preserving the committed offset/fingerprint so a revival does not re-emit the
	// whole blob. A transient error must still surface so a later poll retries it.
	lm := time.Unix(100, 0)
	permErr := &azcore.ResponseError{ErrorCode: string(bloberror.AuthorizationPermissionMismatch)}
	transientErr := &azcore.ResponseError{ErrorCode: string(bloberror.ServerBusy)}

	newR := func(cp *PollingCheckPoint) (*pollingReceiver, *azureblob.MockBlobClient) {
		mc := new(azureblob.MockBlobClient)
		return &pollingReceiver{
			logger:      zap.NewNop(),
			cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
			azureClient: mc, checkpoint: cp,
			consumer: blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
		}, mc
	}

	t.Run("permanent error on the delta read quarantines", func(t *testing.T) {
		r, mc := newR(NewPollingCheckpoint())
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).Return(nil, -1, permErr)
		processed, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 10, LastModified: lm})
		require.NoError(t, err, "a permanent read error is absorbed, not surfaced")
		require.False(t, processed)
		got, ok := r.checkpoint.ProgressFor("b.json")
		require.True(t, ok)
		require.True(t, got.Quarantined, "the blob is quarantined so it is not retried every poll")
	})

	t.Run("permanent error on the fingerprint read quarantines", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		fp := []byte(`{"records":[{"a":1}`)
		cp.UpdateProgress("b.json", BlobProgress{Offset: 19, Fingerprint: fp, LastModified: lm})
		r, mc := newR(cp)
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).Return(nil, -1, permErr)
		processed, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 30, LastModified: lm.Add(time.Minute)})
		require.NoError(t, err)
		require.False(t, processed)
		got, _ := r.checkpoint.ProgressFor("b.json")
		require.True(t, got.Quarantined)
		require.Equal(t, int64(19), got.Offset, "the committed offset survives quarantine, so a revival does not re-read from 0")
		require.Equal(t, fp, got.Fingerprint, "the fingerprint survives quarantine")
	})

	t.Run("transient error on the delta read is surfaced, not quarantined", func(t *testing.T) {
		r, mc := newR(NewPollingCheckpoint())
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).Return(nil, -1, transientErr)
		processed, err := r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 10, LastModified: lm})
		require.Error(t, err, "a transient error is retried on a later poll")
		require.False(t, processed)
		got, ok := r.checkpoint.ProgressFor("b.json")
		require.False(t, ok && got.Quarantined, "a transient error does not quarantine")
	})
}

func TestPollingReceiver_processBlobIncremental_BlobNotFoundTolerated(t *testing.T) {
	lm := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)

	t.Run("not found on the fingerprint read is tolerated", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		cp.UpdateProgress("b.json", BlobProgress{Offset: 10, Fingerprint: []byte("fp"), LastModified: lm})
		mc := new(azureblob.MockBlobClient)
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(defaultFingerprintSize)).
			Return(nil, -1, blobNotFoundErr())
		r := &pollingReceiver{
			logger: zap.NewNop(), cfg: &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
			azureClient: mc, checkpoint: cp, consumer: blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut: &sync.Mutex{}, wg: &sync.WaitGroup{},
		}
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 10, LastModified: lm.Add(time.Minute)})))
	})

	t.Run("not found on the delta read is tolerated", func(t *testing.T) {
		mc := new(azureblob.MockBlobClient)
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(0)).
			Return(nil, -1, blobNotFoundErr())
		r := &pollingReceiver{
			logger: zap.NewNop(), cfg: &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
			azureClient: mc, checkpoint: NewPollingCheckpoint(), consumer: blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
			mut: &sync.Mutex{}, wg: &sync.WaitGroup{},
		}
		require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 10, LastModified: lm})))
	})
}

type failingStore struct{ saves *int }

func (s failingStore) SaveStorageData(context.Context, string, storageclient.StorageData) error {
	if s.saves != nil {
		*s.saves++
	}
	return errors.New("save boom")
}
func (failingStore) LoadStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (failingStore) DeleteStorageData(context.Context, string) error { return nil }
func (failingStore) Close(context.Context) error                     { return nil }

// recordingStore counts SaveStorageData calls.
type recordingStore struct{ saves int }

func (s *recordingStore) SaveStorageData(context.Context, string, storageclient.StorageData) error {
	s.saves++
	return nil
}
func (*recordingStore) LoadStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (*recordingStore) DeleteStorageData(context.Context, string) error { return nil }
func (*recordingStore) Close(context.Context) error                     { return nil }

func TestPollingReceiver_makeCheckpoint_gatedByPath(t *testing.T) {
	newR := func(cfg *Config, store storageclient.StorageClient) *pollingReceiver {
		return &pollingReceiver{
			logger:          zap.NewNop(),
			cfg:             cfg,
			checkpoint:      NewPollingCheckpoint(),
			checkpointStore: store,
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}
	}

	t.Run("legacy empty poll persists nothing", func(t *testing.T) {
		// Flags-off path: an empty poll must not persist an advanced watermark, so
		// restart recovery matches pre-incremental behavior.
		store := &recordingStore{}
		r := newR(&Config{Container: "c"}, store) // otlp, flags off, no lastBlob
		require.NoError(t, r.makeCheckpoint(context.Background()))
		require.Equal(t, 0, store.saves)
	})

	t.Run("incremental poll persists every time", func(t *testing.T) {
		// Incremental path must persist so the per-blob progress map and revisit
		// anchor survive restarts, even when no otlp watermark advanced.
		store := &recordingStore{}
		r := newR(&Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true}, store)
		require.NoError(t, r.makeCheckpoint(context.Background()))
		require.Equal(t, 1, store.saves)
	})
}

func TestPollingReceiver_processBlobIncremental_FingerprintGrows(t *testing.T) {
	// A blob first read while shorter than the fingerprint size stores a short
	// fingerprint; once it grows past the fingerprint size a later read extends the
	// stored fingerprint to full size instead of freezing it at the short prefix.
	checkpoint := NewPollingCheckpoint()
	mockClient := new(azureblob.MockBlobClient)
	var mu sync.Mutex
	content := []byte(`{"records":[{"a":1}`) // shorter than defaultFingerprintSize
	mockClient.EXPECT().
		DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ string, offset, count int64) ([]byte, int64, error) {
			mu.Lock()
			defer mu.Unlock()
			return rangeOf(content, offset, count)
		})
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		azureClient: mockClient,
		checkpoint:  checkpoint,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	blob := func() *azureblob.BlobInfo {
		mu.Lock()
		defer mu.Unlock()
		return &azureblob.BlobInfo{Name: "b.json", Size: int64(len(content)), LastModified: time.Now().UTC()}
	}

	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), blob())))
	prog, _ := checkpoint.ProgressFor("b.json")
	require.Less(t, len(prog.Fingerprint), defaultFingerprintSize, "first read is shorter than the fingerprint size")

	// Grow the blob well past the fingerprint size; the next read extends the fingerprint.
	mu.Lock()
	grown := append([]byte(`{"records":[{"a":1},{"b":"`), bytes.Repeat([]byte("x"), 600)...)
	content = append(grown, []byte(`"}]}`)...)
	mu.Unlock()

	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), blob())))
	prog, _ = checkpoint.ProgressFor("b.json")
	require.Equal(t, defaultFingerprintSize, len(prog.Fingerprint), "fingerprint grows to full size on a later read")
}

func TestPollingReceiver_processBlobIncremental_ConfiguredFingerprintSize(t *testing.T) {
	// A configured fingerprint_size drives the identity read's byte count.
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(0), int64(64)).
		Return([]byte(`{"records":[{"a":1}`), -1, nil) // head for the identity check
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
		Return([]byte(`,{"b":2}]}`), -1, nil) // delta past the stored offset
	cp := NewPollingCheckpoint()
	cp.UpdateProgress("b.json", BlobProgress{Offset: 19, Fingerprint: blobconsume.Fingerprint([]byte(`{"records":[{"a":1}`), 64), LastModified: time.Unix(100, 0)})
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true, FingerprintSize: 64},
		azureClient: mc,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 29, LastModified: time.Unix(200, 0)})))
	mc.AssertExpectations(t) // the identity read used the configured count 64
}

func TestPollingReceiver_processBlobIncremental_AssumeAppendOnly(t *testing.T) {
	// With assume_append_only, the per-poll identity read (DownloadBlobRange at offset 0) is
	// skipped; only the delta read at the stored offset happens. An unexpected identity read
	// would panic the mock.
	mc := new(azureblob.MockBlobClient)
	mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(19), int64(0)).
		Return([]byte(`,{"b":2}]}`), -1, nil)
	cp := NewPollingCheckpoint()
	cp.UpdateProgress("b.json", BlobProgress{Offset: 19, Fingerprint: []byte(`{"records":[{"a":1}`), LastModified: time.Unix(100, 0)})
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true, AssumeAppendOnly: true},
		azureClient: mc,
		checkpoint:  cp,
		consumer:    blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 29, LastModified: time.Unix(200, 0)})))
	mc.AssertExpectations(t)
	prog, _ := cp.ProgressFor("b.json")
	require.Equal(t, int64(27), prog.Offset, "offset advanced by the consumed delta without an identity read")
}

// orderedStore records whether any checkpoint save lands after Close, so a test can assert
// Shutdown joins the poll loop before closing the store.
type orderedStore struct {
	mu             sync.Mutex
	saves          int
	closed         bool
	saveAfterClose bool
}

func (s *orderedStore) SaveStorageData(context.Context, string, storageclient.StorageData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.closed {
		s.saveAfterClose = true
	}
	return nil
}
func (*orderedStore) LoadStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (*orderedStore) DeleteStorageData(context.Context, string) error { return nil }
func (s *orderedStore) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
func (s *orderedStore) saveCount() int { s.mu.Lock(); defer s.mu.Unlock(); return s.saves }
func (s *orderedStore) sawSaveAfterClose() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveAfterClose
}

func TestPollingReceiver_Start_FirstPollWithBlobDoesNotDeadlock(t *testing.T) {
	// Regression: the poll-loop join must use a WaitGroup/channel separate from the one
	// processBlobs waits on. If the poll loop is tracked on r.wg, processBlobs' r.wg.Wait()
	// sees the poll loop's own Add(1) as a permanent floor, so the first poll that lists any
	// blob (every format, otlp included) self-deadlocks and Shutdown then times out.
	mc := new(azureblob.MockBlobClient)
	streamed := make(chan struct{})
	mc.On("StreamBlobs", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			blobChan := args.Get(4).(chan []*azureblob.BlobInfo)
			doneChan := args.Get(5).(chan struct{})
			// Unbuffered channel: this send blocks until processBlobsLoop receives the batch,
			// so once it returns processBlobs (and its r.wg.Wait()) is guaranteed to run.
			blobChan <- []*azureblob.BlobInfo{{Name: "old.json", LastModified: time.Unix(0, 0)}}
			close(streamed)
			close(doneChan)
		})
	r := &pollingReceiver{
		logger:          zap.NewNop(),
		cfg:             &Config{Container: "c", BlobFormat: BlobFormatOTLP, PollInterval: time.Minute},
		azureClient:     mc,
		checkpoint:      NewPollingCheckpoint(),
		checkpointStore: storageclient.NewNopStorage(),
		pollInterval:    time.Minute,
		initialLookback: time.Minute,
		mut:             &sync.Mutex{},
		wg:              &sync.WaitGroup{},
	}
	require.NoError(t, r.Start(context.Background(), nil))
	<-streamed // the batch has reached processBlobsLoop; the poll is now in processBlobs
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, r.Shutdown(shutCtx), "shutdown returns without the poll loop deadlocking")
}

func TestPollingReceiver_Shutdown_TimesOutWaitingForPollLoop(t *testing.T) {
	// If the poll loop never exits within the shutdown deadline, Shutdown reports a timeout
	// rather than closing the store from under it.
	r := &pollingReceiver{
		logger:          zap.NewNop(),
		cfg:             &Config{Container: "c"},
		checkpoint:      NewPollingCheckpoint(),
		checkpointStore: storageclient.NewNopStorage(),
		cancelFunc:      func() {},
		pollDone:        make(chan struct{}), // never closed
		mut:             &sync.Mutex{},
		wg:              &sync.WaitGroup{},
	}
	// Wire telemetry so the timeout path exercises the deferred telemetryBuilder.Shutdown();
	// it must run even though Shutdown returns early, so meter registrations don't leak on the
	// reload-while-stuck path.
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })
	require.NoError(t, r.initTelemetry(tt.NewTelemetrySettings()))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorContains(t, r.Shutdown(ctx), "shutdown timeout waiting for poll loop")
}

func TestPollingReceiver_Shutdown_JoinsPollLoop(t *testing.T) {
	// Start runs the poll loop; Shutdown must join it before closing the store, so a
	// per-poll checkpoint save can never land after Close.
	store := &orderedStore{}
	mc := new(azureblob.MockBlobClient)
	mc.On("StreamBlobs", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { close(args.Get(5).(chan struct{})) })
	r := &pollingReceiver{
		logger:          zap.NewNop(),
		cfg:             &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true, PollInterval: time.Millisecond},
		azureClient:     mc,
		checkpoint:      NewPollingCheckpoint(),
		checkpointStore: store,
		pollInterval:    time.Millisecond,
		initialLookback: time.Minute,
		mut:             &sync.Mutex{},
		wg:              &sync.WaitGroup{},
	}
	// Real telemetry so Shutdown exercises the telemetryBuilder.Shutdown() path.
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })
	require.NoError(t, r.initTelemetry(tt.NewTelemetrySettings()))
	require.NoError(t, r.Start(context.Background(), nil))
	require.Eventually(t, func() bool { return store.saveCount() > 0 }, 2*time.Second, time.Millisecond, "the poll loop ran at least one poll")
	require.NoError(t, r.Shutdown(context.Background()))
	require.False(t, store.sawSaveAfterClose(), "no checkpoint save landed after the store was closed")
}

func TestNewLogsReceiver_ResolvesRevisitWindow(t *testing.T) {
	originalNewAzureBlobClient := newAzureBlobClient
	defer func() { newAzureBlobClient = originalNewAzureBlobClient }()
	newAzureBlobClient = func(_ string, _ int, _ int, _ *zap.Logger) (azureblob.BlobClient, error) {
		return new(azureblob.MockBlobClient), nil
	}
	base := func() *Config {
		return &Config{
			ConnectionString: "DefaultEndpointsProtocol=https;AccountName=test;AccountKey=dGVzdA==;EndpointSuffix=core.windows.net",
			Container:        "c",
			PollInterval:     time.Minute,
			BatchSize:        1,
			PageSize:         1,
		}
	}

	t.Run("defaults when unset", func(t *testing.T) {
		r, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), base(), consumertest.NewNop())
		require.NoError(t, err)
		require.Equal(t, defaultIncrementalRevisitWindow, r.revisitWindow)
	})

	t.Run("honors a custom value", func(t *testing.T) {
		cfg := base()
		cfg.IncrementalRevisitWindow = 30 * time.Minute
		r, err := newLogsReceiver(component.MustNewID("azureblobpolling"), zap.NewNop(), cfg, consumertest.NewNop())
		require.NoError(t, err)
		require.Equal(t, 30*time.Minute, r.revisitWindow)
	})
}

func TestPollingReceiver_processBlobs_routesGzAndIncremental(t *testing.T) {
	// In an append-growable config, a gzip blob takes the whole-tracked route and a
	// plain blob the incremental route; both are processed and tracked.
	sink := new(consumertest.LogsSink)
	mockClient := new(azureblob.MockBlobClient)
	now := time.Now().UTC()
	gz := gzipBytes(t, []byte(`{"records":[{"a":1}]}`))
	mockClient.EXPECT().DownloadBlobStream(mock.Anything, "c", "b.json.gz").Return(gz, nil)
	mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		Return([]byte(`{"records":[{"z":9}]}`), -1, nil)

	r := &pollingReceiver{
		logger:             zap.NewNop(),
		cfg:                &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, UseLastModified: true, EnableIncrementalRead: true},
		azureClient:        mockClient,
		checkpoint:         NewPollingCheckpoint(),
		supportedTelemetry: pipeline.SignalLogs,
		consumer:           blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:                &sync.Mutex{},
		wg:                 &sync.WaitGroup{},
	}
	blobs := []*azureblob.BlobInfo{
		{Name: "b.json.gz", Size: int64(len(gz)), LastModified: now},
		{Name: "b.json", Size: 21, LastModified: now},
	}
	processed, _, _ := r.processBlobs(context.Background(), blobs, now.Add(-time.Hour), now.Add(time.Hour))
	require.Equal(t, 2, processed)
	require.Equal(t, 2, sink.LogRecordCount())
	pg, ok := r.checkpoint.ProgressFor("b.json.gz")
	require.True(t, ok)
	require.Equal(t, int64(len(gz)), pg.Offset, "gz blob recorded as fully consumed")
}

func TestPollingReceiver_processBlobs_processingErrorNotCounted(t *testing.T) {
	// A blob whose processing errors is logged and not counted as processed.
	mockClient := new(azureblob.MockBlobClient)
	now := time.Now().UTC()
	mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		Return(nil, -1, errors.New("range boom"))
	r := &pollingReceiver{
		logger:             zap.NewNop(),
		cfg:                &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, UseLastModified: true, EnableIncrementalRead: true},
		azureClient:        mockClient,
		checkpoint:         NewPollingCheckpoint(),
		supportedTelemetry: pipeline.SignalLogs,
		consumer:           blobconsume.NewRecordsJSONLogsConsumer(new(consumertest.LogsSink), zap.NewNop()),
		mut:                &sync.Mutex{},
		wg:                 &sync.WaitGroup{},
	}
	blobs := []*azureblob.BlobInfo{{Name: "b.json", Size: 10, LastModified: now}}
	processed, _, _ := r.processBlobs(context.Background(), blobs, now.Add(-time.Hour), now.Add(time.Hour))
	require.Equal(t, 0, processed, "a failed blob is not counted")
}

func TestPollingReceiver_isIncrementalFormat_gatedByFlag(t *testing.T) {
	// The incremental engine only engages for records-json/json AND only when the
	// enable_incremental_read opt-in is set. Every other combination stays on the
	// legacy whole-blob path.
	cases := []struct {
		format  BlobFormat
		enable  bool
		perLine bool
		want    bool
	}{
		{BlobFormatRecordsJSON, true, false, true},
		{BlobFormatJSON, true, false, true},
		{BlobFormatText, true, false, false}, // text needs the second flag too
		{BlobFormatText, true, true, true},   // ...which turns it on
		{BlobFormatText, false, true, false}, // second flag alone does nothing
		{BlobFormatOTLP, true, false, false},
		{"", true, false, false},
		{BlobFormatRecordsJSON, false, false, false}, // flag off => legacy path
		{BlobFormatJSON, false, false, false},
		{BlobFormatText, false, false, false},
		{BlobFormatOTLP, false, false, false},
	}
	for _, tc := range cases {
		r := &pollingReceiver{cfg: &Config{BlobFormat: tc.format, EnableIncrementalRead: tc.enable, EnablePerLineText: tc.perLine}}
		require.Equalf(t, tc.want, r.isIncrementalFormat(),
			"isIncrementalFormat: format=%q enable=%v perLine=%v", tc.format, tc.enable, tc.perLine)
		// useIncremental adds the gzip carve-out on top of the gate.
		require.Equalf(t, tc.want, r.useIncremental("b.json"),
			"useIncremental(plain): format=%q enable=%v perLine=%v", tc.format, tc.enable, tc.perLine)
		require.False(t, r.useIncremental("b.json.gz"),
			"gzip is never range-tailed regardless of gate")
	}
}

func TestPollingReceiver_processBlobs_flagOffUsesWholePath(t *testing.T) {
	// With enable_incremental_read off, a records-json blob takes the legacy
	// whole-blob path (DownloadBlobStream + whole-buffer consume), not the
	// incremental DownloadBlobRange path. DownloadBlobRange is intentionally not
	// mocked, so routing to it would panic.
	sink := new(consumertest.LogsSink)
	mockClient := new(azureblob.MockBlobClient)
	now := time.Now().UTC()
	mockClient.EXPECT().DownloadBlobStream(mock.Anything, "c", "b.json").
		Return([]byte(`{"records":[{"a":1},{"b":2}]}`), nil)
	mockClient.EXPECT().DeleteBlob(mock.Anything, "c", "b.json").Return(nil).Maybe()

	r := &pollingReceiver{
		logger:             zap.NewNop(),
		cfg:                &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, UseLastModified: true},
		azureClient:        mockClient,
		checkpoint:         NewPollingCheckpoint(),
		supportedTelemetry: pipeline.SignalLogs,
		consumer:           blobconsume.NewRecordsJSONLogsConsumer(sink, zap.NewNop()),
		mut:                &sync.Mutex{},
		wg:                 &sync.WaitGroup{},
	}
	blobs := []*azureblob.BlobInfo{{Name: "b.json", Size: 29, LastModified: now}}
	processed, _, _ := r.processBlobs(context.Background(), blobs, now.Add(-time.Hour), now.Add(time.Hour))
	require.Equal(t, 1, processed)
	require.Equal(t, 2, sink.LogRecordCount(), "whole blob parsed once as records-json")
	_, tracked := r.checkpoint.ProgressFor("b.json")
	require.False(t, tracked, "legacy path does not record a byte-offset progress entry")
}

func TestPollingReceiver_finalizePoll_saveErrorIsLogged(t *testing.T) {
	saves := 0
	// EnableIncrementalRead makes the incremental path persist every poll (the legacy
	// path would early-return before saving when no blob advanced the watermark), so
	// the failing save is actually reached and its error-logging branch runs.
	r := &pollingReceiver{
		logger:          zap.NewNop(),
		cfg:             &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
		checkpoint:      NewPollingCheckpoint(),
		checkpointStore: failingStore{saves: &saves},
		mut:             &sync.Mutex{},
		wg:              &sync.WaitGroup{},
	}
	now := time.Now().UTC()
	// A failing store must not panic the poll; the save error is logged.
	require.NotPanics(t, func() { r.finalizePoll(context.Background(), now.Add(-time.Hour), now) })
	require.Equal(t, 1, saves, "the failing checkpoint save must actually be attempted")
}

func TestPollingReceiver_finalizePoll_skewToleranceRetainsBlob(t *testing.T) {
	// A blob whose mtime falls just before the horizon but within sealSkewTolerance must
	// not be sealed: it is still listed (its path-time is in the window), so sealing it
	// would trigger a no-checkpoint re-read from offset 0 every poll. Beyond the tolerance
	// it ages out as normal.
	now := time.Now().UTC()
	newR := func() (*pollingReceiver, *azureblob.MockBlobClient) {
		mc := new(azureblob.MockBlobClient)
		return &pollingReceiver{
			logger:          zap.NewNop(),
			cfg:             &Config{Container: "c", BlobFormat: BlobFormatRecordsJSON, EnableIncrementalRead: true},
			azureClient:     mc,
			checkpoint:      NewPollingCheckpoint(),
			checkpointStore: storageclient.NewNopStorage(),
			mut:             &sync.Mutex{},
			wg:              &sync.WaitGroup{},
		}, mc
	}

	t.Run("within tolerance: retained, not sealed", func(t *testing.T) {
		r, mc := newR()
		r.checkpoint.UpdateProgress("b.json", BlobProgress{Offset: 10, LastModified: now.Add(-3 * time.Minute)})
		r.finalizePoll(context.Background(), now, now)
		_, ok := r.checkpoint.ProgressFor("b.json")
		require.True(t, ok, "a blob within the skew tolerance stays tracked")
		mc.AssertNotCalled(t, "DownloadBlobRange")
	})

	t.Run("beyond tolerance: aged out and sealed", func(t *testing.T) {
		r, mc := newR()
		// The seal tail read finds nothing past the offset (416), so it is a clean seal.
		mc.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", int64(10), int64(0)).
			Return(nil, -1, invalidRangeErr())
		r.checkpoint.UpdateProgress("b.json", BlobProgress{Offset: 10, LastModified: now.Add(-10 * time.Minute)})
		r.finalizePoll(context.Background(), now, now)
		got, ok := r.checkpoint.ProgressFor("b.json")
		require.True(t, ok, "a blob beyond the skew tolerance is sealed and kept for a later resume")
		require.True(t, got.Sealed, "and marked Sealed (delete_on_read off) rather than dropped")
		mc.AssertExpectations(t)
	})
}

func TestPollingReceiver_consumeDelta_noCompleteLine(t *testing.T) {
	mockClient := new(azureblob.MockBlobClient)
	mockClient.EXPECT().DownloadBlobRange(mock.Anything, "c", "b.json", mock.Anything, mock.Anything).
		Return([]byte(`{"a":1`), -1, nil) // no newline yet
	cp := NewPollingCheckpoint()
	sink := new(consumertest.LogsSink)
	r := &pollingReceiver{
		logger:      zap.NewNop(),
		cfg:         &Config{Container: "c", BlobFormat: BlobFormatJSON},
		azureClient: mockClient,
		checkpoint:  cp,
		consumer:    blobconsume.NewNDJSONLogsConsumer(sink, zap.NewNop()),
		mut:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}
	require.NoError(t, procErr(r.processBlobIncremental(context.Background(), &azureblob.BlobInfo{Name: "b.json", Size: 6, LastModified: time.Now().UTC()})))
	require.Equal(t, 0, sink.LogRecordCount())
	prog, _ := cp.ProgressFor("b.json")
	require.Equal(t, int64(0), prog.Offset, "no complete line yet, offset stays 0")
}

func TestPollingReceiver_trimMatchedRoot(t *testing.T) {
	t.Run("trims longest matching root and reports trimmed=true", func(t *testing.T) {
		r := &pollingReceiver{
			expandedRoots: []string{
				"flowLogResourceID=/SUB_A_RG/NSG_A",
				"flowLogResourceID=/SUB_B_RG/NSG_B",
			},
		}
		got, trimmed := r.trimMatchedRoot("flowLogResourceID=/SUB_A_RG/NSG_A/y=2026/m=05/d=16/h=16/m=00/macAddress=AA/PT1H.json")
		require.True(t, trimmed)
		require.Equal(t, "y=2026/m=05/d=16/h=16/m=00/macAddress=AA/PT1H.json", got)
	})

	t.Run("returns input unchanged and trimmed=false when no root matches", func(t *testing.T) {
		r := &pollingReceiver{
			expandedRoots: []string{"some/other/prefix"},
		}
		input := "flowLogResourceID=/X/Y/y=2026/m=05/d=16/h=16/m=00/PT1H.json"
		got, trimmed := r.trimMatchedRoot(input)
		require.False(t, trimmed)
		require.Equal(t, input, got)
	})

	t.Run("returns input unchanged and trimmed=false when expandedRoots is empty", func(t *testing.T) {
		r := &pollingReceiver{}
		input := "year=2024/month=03/day=15/logs_data.json"
		got, trimmed := r.trimMatchedRoot(input)
		require.False(t, trimmed)
		require.Equal(t, input, got)
	})

	t.Run("ignores empty root entries", func(t *testing.T) {
		r := &pollingReceiver{
			expandedRoots: []string{"", "logs"},
		}
		got, trimmed := r.trimMatchedRoot("logs/2024/03/15/file.json")
		require.True(t, trimmed)
		require.Equal(t, "2024/03/15/file.json", got)
	})
}
