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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadatatest"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/worker"
)

func brokenStreamBody() io.ReadCloser {
	var prefix bytes.Buffer
	prefix.WriteByte('[')
	for i := 0; i < 200; i++ {
		if i > 0 {
			prefix.WriteByte(',')
		}
		prefix.WriteString(`{"host":"h","msg":"padding padding padding padding"}`)
	}
	prefix.WriteString(`,{"host":"h","ms`) // incomplete final record
	return io.NopCloser(&errAfterPrefixReader{prefix: prefix.Bytes(), err: errors.New("connection reset by peer")})
}

// TestTruncatedObjectInNackedMessageIsNotCounted asserts that when a message carries a
// truncated object followed by an object whose download breaks, the whole message nacks
// (no ack) and the truncation is NOT counted. Counting it before the ack would double-
// count the truncation every time the nacked message redelivers.
func TestTruncatedObjectInNackedMessageIsNotCounted(t *testing.T) {
	ctx := context.Background()

	mockSQS := &mocks.MockSQSClient{}
	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockClient.EXPECT().S3().Return(mockS3)

	event := `{"Records":[` +
		`{"eventName":"s3:ObjectCreated:Put","s3":{"bucket":{"name":"mybucket"},"object":{"key":"truncated","size":40}}},` +
		`{"eventName":"s3:ObjectCreated:Put","s3":{"bucket":{"name":"mybucket"},"object":{"key":"broken","size":100000}}}` +
		`]}`

	truncated, err := os.ReadFile("testdata/logs_array_fragment.json")
	require.NoError(t, err)

	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			if *in.Key == "truncated" {
				return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(truncated)), ContentLength: aws.Int64(int64(len(truncated)))}, nil
			}
			return &s3.GetObjectOutput{Body: brokenStreamBody(), ContentLength: aws.Int64(100000)}, nil
		})

	// No DeleteMessage / ChangeMessageVisibility expected: a broken stream leaves the
	// message for retry, so the whole message nacks by lapsing.

	tt := componenttest.NewTelemetry()
	defer func() { require.NoError(t, tt.Shutdown(context.Background())) }()
	set := metadatatest.NewSettings(tt).TelemetrySettings
	sink := new(consumertest.LogsSink)
	tb, err := metadata.NewTelemetryBuilder(set)
	require.NoError(t, err)

	params := receivertest.NewNopSettings(metadata.Type)
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             params.ID,
		Transport:              "http",
		ReceiverCreateSettings: params,
	})
	require.NoError(t, err)
	w := worker.New(set, sink, mockClient, obsrecv, 4096, 1000, 100*time.Millisecond, 300*time.Second, 6*time.Hour, worker.WithTelemetryBuilder(tb))

	msg := types.Message{
		Body:          aws.String(event),
		MessageId:     aws.String("123"),
		ReceiptHandle: aws.String("receipt-handle"),
	}

	done := make(chan struct{})
	w.ProcessMessage(ctx, msg, "myqueue", func() { close(done) })
	<-done

	mockSQS.AssertNotCalled(t, "DeleteMessage", mock.Anything, mock.Anything)
	require.Positive(t, sink.LogRecordCount(), "the truncated object's records are still delivered")
	// The message nacked, so the truncation must not be counted: the counter is never
	// touched (counting before the ack would double-count on every redelivery).
	_, err = tt.GetMetric("otelcol_s3event.truncated_objects")
	require.Error(t, err, "a nacked message must not record a truncated object")
}

// TestTruncatedObjectClearsSavedOffset asserts that once a truncated object is delivered
// and acked, its saved resume offset is cleared. A later re-upload under the same key
// then reprocesses from the beginning rather than resuming past the earlier cut, since
// duplicating a few records is preferable to silently dropping the re-uploaded tail.
func TestTruncatedObjectClearsSavedOffset(t *testing.T) {
	ctx := context.Background()

	mockSQS := &mocks.MockSQSClient{}
	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockClient.EXPECT().S3().Return(mockS3)

	validS3Event := `{"Records":[{"eventName":"s3:ObjectCreated:Put","s3":{"bucket":{"name":"mybucket"},"object":{"key":"mykey1","size":40}}}]}`
	truncated, err := os.ReadFile("testdata/logs_array_fragment.json")
	require.NoError(t, err)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).Return(&s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(truncated)),
		ContentLength: aws.Int64(int64(len(truncated))),
	}, nil)
	mockSQS.EXPECT().DeleteMessage(mock.Anything, mock.Anything).Return(&sqs.DeleteMessageOutput{}, nil)

	set := componenttest.NewNopTelemetrySettings()
	sink := new(consumertest.LogsSink)
	tb, err := metadata.NewTelemetryBuilder(set)
	require.NoError(t, err)

	params := receivertest.NewNopSettings(metadata.Type)
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             params.ID,
		Transport:              "http",
		ReceiverCreateSettings: params,
	})
	require.NoError(t, err)
	w := worker.New(set, sink, mockClient, obsrecv, 4096, 1000, 100*time.Millisecond, 300*time.Second, 6*time.Hour, worker.WithTelemetryBuilder(tb))
	store := newMemStorage()
	w.SetOffsetStorage(store)

	msg := types.Message{
		Body:          aws.String(validS3Event),
		MessageId:     aws.String("123"),
		ReceiptHandle: aws.String("receipt-handle"),
	}

	done := make(chan struct{})
	w.ProcessMessage(ctx, msg, "myqueue", func() { close(done) })
	<-done

	require.Equal(t, 1, sink.LogRecordCount(), "the record read before the cut is delivered")
	offsetKey := fmt.Sprintf("%s_%s", worker.OffsetStorageKey, "mykey1")
	require.False(t, store.has(offsetKey), "the truncated object's saved offset must be cleared on ack")
}
