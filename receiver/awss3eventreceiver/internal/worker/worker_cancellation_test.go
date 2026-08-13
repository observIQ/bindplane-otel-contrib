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

package worker_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/worker"
)

// memStorage is an in-memory StorageClient that obeys context cancellation. A test can
// then tell whether a checkpoint ran on a live context.
type memStorage struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemStorage() *memStorage {
	return &memStorage{data: map[string][]byte{}}
}

func (m *memStorage) SaveStorageData(ctx context.Context, key string, data storageclient.StorageData) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := data.Marshal()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = b
	return nil
}

func (m *memStorage) LoadStorageData(ctx context.Context, key string, data storageclient.StorageData) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil
	}
	return data.Unmarshal(b)
}

func (m *memStorage) DeleteStorageData(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memStorage) Close(context.Context) error { return nil }

func (m *memStorage) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok
}

// cancelOnExhaustReader serves body and then cancels the context and reports the
// cancellation, modelling a config push that lands while an object is being streamed.
type cancelOnExhaustReader struct {
	body   *strings.Reader
	cancel context.CancelFunc
}

func (r *cancelOnExhaustReader) Read(p []byte) (int, error) {
	if r.body.Len() > 0 {
		return r.body.Read(p)
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	return 0, context.Canceled
}

func (r *cancelOnExhaustReader) Close() error { return nil }

const cancelTestEvent = `{"Records":[{"eventName":"s3:ObjectCreated:Put","s3":{"bucket":{"name":"mybucket"},"object":{"key":"mykey","size":9}}}]}`

// objectLines builds n newline-terminated lines and returns both the encoded bytes and
// the lines they should parse into. Lines are padded so a modest count still exceeds the
// content-detection window, which the parser peeks before any record is produced.
func objectLines(start, n int) (body string, lines []string) {
	var sb strings.Builder
	for i := start; i < start+n; i++ {
		line := fmt.Sprintf("line-%06d-padding-to-keep-the-object-comfortably-larger-than-the-detection-window", i)
		lines = append(lines, line)
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String(), lines
}

type cancelTestHarness struct {
	worker      *worker.Worker
	message     types.Message
	sink        *consumertest.LogsSink
	storage     *memStorage
	logs        *observer.ObservedLogs
	visibility  *[]int32
	deleteCalls *int
}

// newCancelTestHarness builds a worker whose S3 object body is served by bodyFn and whose
// SQS calls are recorded rather than performed.
func newCancelTestHarness(t *testing.T, maxLogsEmitted int, store *memStorage, bodyFn func() io.ReadCloser) *cancelTestHarness {
	t.Helper()

	mockSQS := &mocks.MockSQSClient{}
	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockClient.EXPECT().S3().Return(mockS3)

	visibility := &[]int32{}
	deleteCalls := new(int)
	var mu sync.Mutex

	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: bodyFn()}, nil
		})
	mockSQS.EXPECT().ChangeMessageVisibility(mock.Anything, mock.Anything).RunAndReturn(
		func(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			*visibility = append(*visibility, in.VisibilityTimeout)
			return &sqs.ChangeMessageVisibilityOutput{}, nil
		})
	mockSQS.EXPECT().DeleteMessage(mock.Anything, mock.Anything).RunAndReturn(
		func(ctx context.Context, _ *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			*deleteCalls++
			return &sqs.DeleteMessageOutput{}, nil
		})

	core, recorded := observer.New(zap.DebugLevel)
	set := componenttest.NewNopTelemetrySettings()
	set.Logger = zap.New(core)

	tb, err := metadata.NewTelemetryBuilder(set)
	require.NoError(t, err)

	params := receivertest.NewNopSettings(metadata.Type)
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             params.ID,
		Transport:              "http",
		ReceiverCreateSettings: params,
	})
	require.NoError(t, err)

	sink := new(consumertest.LogsSink)
	// Downstream consumers reject a cancelled context, so the sink must also reject it.
	next, err := consumer.NewLogs(func(ctx context.Context, ld plog.Logs) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return sink.ConsumeLogs(ctx, ld)
	})
	require.NoError(t, err)

	// A long visibility timeout keeps the extension goroutine out of the way.
	w := worker.New(set, next, mockClient, obsrecv, 4096, maxLogsEmitted,
		300*time.Second, 300*time.Second, 6*time.Hour, worker.WithTelemetryBuilder(tb))
	w.SetOffsetStorage(store)

	return &cancelTestHarness{
		worker: w,
		message: types.Message{
			Body:          aws.String(cancelTestEvent),
			MessageId:     aws.String("cancel-test"),
			ReceiptHandle: aws.String("receipt-handle"),
		},
		sink:        sink,
		storage:     store,
		logs:        recorded,
		visibility:  visibility,
		deleteCalls: deleteCalls,
	}
}

func (h *cancelTestHarness) bodyStrings(t *testing.T) []string {
	t.Helper()

	var got []string
	for _, ld := range h.sink.AllLogs() {
		for i := 0; i < ld.ResourceLogs().Len(); i++ {
			sls := ld.ResourceLogs().At(i).ScopeLogs()
			for j := 0; j < sls.Len(); j++ {
				lrs := sls.At(j).LogRecords()
				for k := 0; k < lrs.Len(); k++ {
					got = append(got, lrs.At(k).Body().Str())
				}
			}
		}
	}
	return got
}

func (h *cancelTestHarness) logged(msg string) bool {
	for _, e := range h.logs.All() {
		if e.Message == msg {
			return true
		}
	}
	return false
}

// TestProcessMessage_CancelledMidObject asserts the wind-down contract: batches completed
// before the stream was cut are delivered and checkpointed, the message is nacked for
// redelivery rather than deleted, and the cancellation is not treated as a DLQ condition.
func TestProcessMessage_CancelledMidObject(t *testing.T) {
	const batchSize = 100
	body, lines := objectLines(0, 250)
	store := newMemStorage()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newCancelTestHarness(t, batchSize, store, func() io.ReadCloser {
		return &cancelOnExhaustReader{body: strings.NewReader(body), cancel: cancel}
	})

	done := make(chan struct{})
	go h.worker.ProcessMessage(ctx, h.message, "myqueue", func() { close(done) })
	<-done

	require.Equal(t, lines, h.bodyStrings(t),
		"records read before the stream was cut are delivered, including the partial batch")

	require.True(t, store.has(worker.OffsetStorageKey+"_mykey"),
		"a cancelled object should leave a checkpoint behind")

	require.Contains(t, *h.visibility, int32(0),
		"a cancelled message should be nacked so it redelivers immediately")

	require.Zero(t, *h.deleteCalls, "a partially read object must not be deleted from the queue")

	require.False(t, h.logged("DLQ condition triggered, resetting visibility for DLQ processing"),
		"cancellation must never route an object to the DLQ")
}

// TestProcessMessage_ResumesFromCheckpointAfterCancellation asserts that the checkpoint
// written before the cancellation is what redelivery resumes from, so the two runs
// together cover the object exactly once.
func TestProcessMessage_ResumesFromCheckpointAfterCancellation(t *testing.T) {
	const batchSize = 100
	body, lines := objectLines(0, 250)
	store := newMemStorage()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newCancelTestHarness(t, batchSize, store, func() io.ReadCloser {
		return &cancelOnExhaustReader{body: strings.NewReader(body), cancel: cancel}
	})

	done := make(chan struct{})
	go first.worker.ProcessMessage(ctx, first.message, "myqueue", func() { close(done) })
	<-done
	delivered := first.bodyStrings(t)
	require.NotEmpty(t, delivered)

	// Redelivery: the same object, now read to completion on a live context.
	second := newCancelTestHarness(t, batchSize, store, func() io.ReadCloser {
		return io.NopCloser(strings.NewReader(body))
	})

	done2 := make(chan struct{})
	go second.worker.ProcessMessage(context.Background(), second.message, "myqueue", func() { close(done2) })
	<-done2

	require.Equal(t, lines, append(delivered, second.bodyStrings(t)...),
		"the two runs together should cover the object exactly once")
	require.Equal(t, 1, *second.deleteCalls, "a fully read object should be deleted from the queue")
}
