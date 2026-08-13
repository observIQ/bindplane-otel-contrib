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

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadatatest"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/worker"
)

// TestTruncatedObjectNotCountedWhenDeleteFails asserts that when a truncated object is
// delivered but the SQS delete (the ack) fails, the truncation is NOT counted. The message
// redelivers and will be reprocessed, so counting it now would double-count on each retry.
// This matches the GCS worker, which gates the same counter on ack success.
func TestTruncatedObjectNotCountedWhenDeleteFails(t *testing.T) {
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

	// The ack (delete) fails, so the message will redeliver.
	mockSQS.EXPECT().DeleteMessage(mock.Anything, mock.Anything).
		Return(&sqs.DeleteMessageOutput{}, errors.New("delete failed"))

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

	require.Equal(t, 1, sink.LogRecordCount(), "the record read before the cut is still delivered")
	_, err = tt.GetMetric("otelcol_s3event.truncated_objects")
	require.Error(t, err, "a failed delete must not record a truncated object")
}
