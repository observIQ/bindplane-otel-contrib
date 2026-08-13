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
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/worker"
)

// TestTruncatedArrayRoutesWholeObjectToDLQ drives a JSON array that ends mid-record
// through the worker. A truncated object is a dead-letter condition: redelivering it
// reads the same bytes and fails the same way, so the worker resets the message
// visibility to hand it to the SQS redrive policy rather than deleting it. This can't
// be exercised by the drain-loop table in TestProcessMessage, which only handles
// objects that ack and delete, so the fake SQS (no redrive policy) would redeliver
// forever.
func TestTruncatedArrayRoutesWholeObjectToDLQ(t *testing.T) {
	ctx := context.Background()

	mockSQS := &mocks.MockSQSClient{}
	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockClient.EXPECT().S3().Return(mockS3)

	validS3Event := `{"Records":[{"eventName":"s3:ObjectCreated:Put","s3":{"bucket":{"name":"mybucket"},"object":{"key":"mykey1","size":40}}}]}`

	// The exact object that produced the original redelivery hang: a JSON array that
	// ends part way through, with no closing ']'.
	truncated, err := os.ReadFile("testdata/logs_array_fragment.json")
	require.NoError(t, err)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).Return(&s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(truncated)),
		ContentLength: aws.Int64(int64(len(truncated))),
	}, nil)

	// The object routes to the dead-letter queue: visibility reset to 0, never deleted.
	mockSQS.EXPECT().ChangeMessageVisibility(mock.Anything, mock.MatchedBy(func(input *sqs.ChangeMessageVisibilityInput) bool {
		return input.VisibilityTimeout == 0
	})).Return(&sqs.ChangeMessageVisibilityOutput{}, nil)

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

	// The message must not be deleted (DeleteMessage is never expected on the mock),
	// and the visibility reset must have been requested.
	mockSQS.AssertExpectations(t)
	mockS3.AssertExpectations(t)

	// A truncated object is handled all-or-nothing: the worker returns as soon as it
	// hits the cut, before the pending batch is flushed, so the complete records read
	// before the cut are not delivered. The whole object goes to the dead-letter queue
	// instead, where an operator recovers it.
	require.Equal(t, 0, sink.LogRecordCount(), "a truncated object delivers nothing and routes the whole object to the DLQ")
}
