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

package worker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
)

// newFlushFailWorker builds a Worker whose S3 object serves body and whose next consumer
// rejects every batch, so a flush inside consumeLogsFromS3Object fails.
func newFlushFailWorker(t *testing.T, body string, maxLogsEmitted int, consumeErr error) (*Worker, events.S3EventRecord) {
	t.Helper()

	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
		})

	w := &Worker{
		client:         mockClient,
		nextConsumer:   consumertest.NewErr(consumeErr),
		offsetStorage:  errStorage{},
		obsrecv:        newTestObsReport(t),
		maxLogSize:     4096,
		maxLogsEmitted: maxLogsEmitted,
	}

	record := events.S3EventRecord{
		AWSRegion: "us-east-1",
		EventTime: time.Now(),
		S3: events.S3Entity{
			Bucket: events.S3Bucket{Name: "mybucket"},
			Object: events.S3Object{Key: "mykey", Size: int64(len(body))},
		},
	}
	return w, record
}

// TestConsume_TrailingFlushFailureFailsObject asserts that when the final batch is
// rejected by the next consumer, consumeLogsFromS3Object returns the error so the object
// is not acked (the message nacks and redelivers).
func TestConsume_TrailingFlushFailureFailsObject(t *testing.T) {
	t.Parallel()

	consumeErr := errors.New("pipeline backpressure")
	// Fewer records than maxLogsEmitted, so only the trailing flush runs.
	w, record := newFlushFailWorker(t, "line1\nline2\n", 1000, consumeErr)

	err := w.consumeLogsFromS3Object(context.Background(), record, "mykey", false, zap.NewNop())
	require.ErrorIs(t, err, consumeErr)
	require.ErrorContains(t, err, "consume logs")
}

// TestConsume_MidLoopFlushFailureFailsObject asserts that when a mid-object batch (the
// one emitted once maxLogsEmitted is reached) is rejected, consumeLogsFromS3Object
// returns the error immediately rather than continuing to read.
func TestConsume_MidLoopFlushFailureFailsObject(t *testing.T) {
	t.Parallel()

	consumeErr := errors.New("pipeline backpressure")
	// maxLogsEmitted of 1 forces a flush after the first record, mid-loop.
	w, record := newFlushFailWorker(t, "line1\nline2\nline3\n", 1, consumeErr)

	err := w.consumeLogsFromS3Object(context.Background(), record, "mykey", false, zap.NewNop())
	require.ErrorIs(t, err, consumeErr)
	require.ErrorContains(t, err, "consume logs")
}
