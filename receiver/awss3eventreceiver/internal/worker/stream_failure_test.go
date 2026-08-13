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
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/worker"
)

// brokenBody serves head and then fails, standing in for a connection dropped part way
// through a GetObject body.
type brokenBody struct {
	head io.Reader
	err  error
}

func (b *brokenBody) Read(p []byte) (int, error) {
	n, err := b.head.Read(p)
	if errors.Is(err, io.EOF) {
		return n, b.err
	}
	return n, err
}

func (b *brokenBody) Close() error { return nil }

// TestProcessMessage_PreservesMessageWhenTheObjectStreamBreaks asserts an object whose
// body fails part way through is left in the queue for retry. Acking it would drop every
// record after the break with no way to recover them.
func TestProcessMessage_PreservesMessageWhenTheObjectStreamBreaks(t *testing.T) {
	ctx := context.Background()
	readErr := errors.New("read: connection reset by peer")

	mockSQS := &mocks.MockSQSClient{}
	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockClient.EXPECT().S3().Return(mockS3)

	validS3Event := `{"Records":[{"eventName":"s3:ObjectCreated:Put","s3":{"bucket":{"name":"mybucket"},"object":{"key":"mykey1","size":15}}}]}`
	mockSQS.EXPECT().ReceiveMessage(ctx, new(sqs.ReceiveMessageInput)).Return(&sqs.ReceiveMessageOutput{
		Messages: []types.Message{{
			Body:          aws.String(validS3Event),
			MessageId:     aws.String("123"),
			ReceiptHandle: aws.String("receipt-handle"),
		}},
	}, nil)

	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body: &brokenBody{head: strings.NewReader("line1\nline2\nline3\n"), err: readErr},
			}, nil
		})

	core, recorded := observer.New(zap.DebugLevel)
	set := componenttest.NewNopTelemetrySettings()
	set.Logger = zap.New(core)

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

	w := worker.New(set, sink, mockClient, obsrecv, 4096, 1000,
		1*time.Hour, 300*time.Second, 6*time.Hour, worker.WithTelemetryBuilder(tb))

	msg, err := mockClient.SQS().ReceiveMessage(ctx, new(sqs.ReceiveMessageInput))
	require.NoError(t, err)
	require.Len(t, msg.Messages, 1)

	done := make(chan struct{})
	go func() { w.ProcessMessage(ctx, msg.Messages[0], "myqueue", func() { close(done) }) }()
	<-done

	require.True(t, containsLogMessage(recorded, "preserving message in SQS for retry"),
		"a broken object stream must keep the message for retry")
	mockSQS.AssertNotCalled(t, "DeleteMessage", mock.Anything, mock.Anything)
}
