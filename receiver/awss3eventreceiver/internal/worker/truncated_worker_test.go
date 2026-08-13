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
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadatatest"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/worker"
)

// TestTruncatedArrayDeliversRecordsThenAcks drives a JSON array that ends mid-record
// through the worker. The records read before the cut are delivered and the message is
// acked (deleted): the missing tail was never written, so the dead-letter queue (which
// would hold the same truncated bytes) could not recover it, and a retry reads the same
// object. The truncation is surfaced through a counter rather than by requeuing.
func TestTruncatedArrayDeliversRecordsThenAcks(t *testing.T) {
	ctx := context.Background()

	mockSQS := &mocks.MockSQSClient{}
	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockClient.EXPECT().S3().Return(mockS3)

	validS3Event := `{"Records":[{"eventName":"s3:ObjectCreated:Put","s3":{"bucket":{"name":"mybucket"},"object":{"key":"mykey1","size":40}}}]}`

	// A JSON array that ends part way through, with no closing ']'.
	truncated, err := os.ReadFile("testdata/logs_array_fragment.json")
	require.NoError(t, err)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).Return(&s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(truncated)),
		ContentLength: aws.Int64(int64(len(truncated))),
	}, nil)

	// The object is acked: the message is deleted, not routed to the DLQ. A stray
	// ChangeMessageVisibility would fail the mock, since none is set up.
	mockSQS.EXPECT().DeleteMessage(mock.Anything, mock.Anything).Return(&sqs.DeleteMessageOutput{}, nil)

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
		Body:          aws.String(validS3Event),
		MessageId:     aws.String("123"),
		ReceiptHandle: aws.String("receipt-handle"),
	}

	done := make(chan struct{})
	w.ProcessMessage(ctx, msg, "myqueue", func() { close(done) })
	<-done

	mockSQS.AssertExpectations(t)
	mockS3.AssertExpectations(t)

	// The one complete record read before the cut is delivered.
	require.Equal(t, 1, sink.LogRecordCount(), "the records read before the cut are delivered")

	// The truncation is surfaced through the counter (incremented once, after the ack).
	metadatatest.AssertEqualS3eventTruncatedObjects(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 1}}, metricdatatest.IgnoreTimestamp())
}

// errAfterPrefixReader serves prefix, then fails every read with err (a broken stream).
type errAfterPrefixReader struct {
	prefix []byte
	pos    int
	err    error
}

func (r *errAfterPrefixReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.prefix) {
		return 0, r.err
	}
	n := copy(p, r.prefix[r.pos:])
	r.pos += n
	return n, nil
}

// TestInterruptedDownloadIsRetriedNotAckedOrDLQd asserts an object whose download breaks
// mid-read (a broken stream) is preserved for retry: the message is neither deleted (acked)
// nor visibility-reset (DLQ), so SQS redelivers it. This is the retry leg, distinct from the
// deliver+ack (truncation) and DLQ (corrupt) legs.
func TestInterruptedDownloadIsRetriedNotAckedOrDLQd(t *testing.T) {
	ctx := context.Background()

	mockSQS := &mocks.MockSQSClient{}
	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockClient.EXPECT().S3().Return(mockS3)

	validS3Event := `{"Records":[{"eventName":"s3:ObjectCreated:Put","s3":{"bucket":{"name":"mybucket"},"object":{"key":"mykey1","size":100000}}}]}`

	// A JSON array prefix large enough to pass detection, ending inside an incomplete
	// record so the break lands mid-decode, then a broken connection.
	var prefix bytes.Buffer
	prefix.WriteByte('[')
	for i := 0; i < 200; i++ {
		if i > 0 {
			prefix.WriteByte(',')
		}
		prefix.WriteString(`{"host":"h","msg":"padding padding padding padding"}`)
	}
	prefix.WriteString(`,{"host":"h","ms`) // incomplete final record
	body := &errAfterPrefixReader{prefix: prefix.Bytes(), err: errors.New("connection reset by peer")}
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).Return(&s3.GetObjectOutput{
		Body:          io.NopCloser(body),
		ContentLength: aws.Int64(100000),
	}, nil)

	// No DeleteMessage and no ChangeMessageVisibility are set up: the mock fails if the
	// worker acks or routes to the DLQ. The broken stream must simply be left for retry.

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

	msg := types.Message{
		Body:          aws.String(validS3Event),
		MessageId:     aws.String("123"),
		ReceiptHandle: aws.String("receipt-handle"),
	}

	done := make(chan struct{})
	w.ProcessMessage(ctx, msg, "myqueue", func() { close(done) })
	<-done

	mockSQS.AssertExpectations(t)
	mockS3.AssertExpectations(t)
	mockSQS.AssertNotCalled(t, "DeleteMessage", mock.Anything, mock.Anything)
	mockSQS.AssertNotCalled(t, "ChangeMessageVisibility", mock.Anything, mock.Anything)
}
