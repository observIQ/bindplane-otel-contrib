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
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
)

func newTestMetrics(t *testing.T) *metadata.TelemetryBuilder {
	t.Helper()
	tb, err := metadata.NewTelemetryBuilder(componenttest.NewNopTelemetrySettings())
	require.NoError(t, err)
	return tb
}

// TestHandleProcessingError_DeadlineExceededDoesNotHotRedeliver asserts that a downstream
// timeout (context.DeadlineExceeded) with a live context is treated as a transient failure
// and left for the visibility timeout to redeliver, NOT nacked for immediate redelivery.
// Immediate redelivery would hammer a downstream that is already struggling under backpressure.
func TestHandleProcessingError_DeadlineExceededDoesNotHotRedeliver(t *testing.T) {
	t.Parallel()

	mockSQS := &mocks.MockSQSClient{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS).Maybe()

	w := &Worker{client: mockClient, metrics: newTestMetrics(t)}

	// Live context: the downstream timed out, we are not shutting down.
	w.handleProcessingError(context.Background(), types.Message{ReceiptHandle: aws.String("rh")},
		"https://sqs.example/q", context.DeadlineExceeded, zap.NewNop())

	mockSQS.AssertNotCalled(t, "ChangeMessageVisibility", mock.Anything, mock.Anything)
}

// TestHandleProcessingError_ShutdownNacksImmediately asserts that when our own context is
// cancelled (a shutdown / config push), the message is nacked for immediate redelivery so it
// resumes promptly from the checkpoint — the DeadlineExceeded there is our own wind-down.
func TestHandleProcessingError_ShutdownNacksImmediately(t *testing.T) {
	t.Parallel()

	mockSQS := &mocks.MockSQSClient{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockSQS.EXPECT().ChangeMessageVisibility(mock.Anything, mock.Anything).
		Return(&sqs.ChangeMessageVisibilityOutput{}, nil).Once()

	w := &Worker{client: mockClient, metrics: newTestMetrics(t)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.handleProcessingError(ctx, types.Message{ReceiptHandle: aws.String("rh")},
		"https://sqs.example/q", context.DeadlineExceeded, zap.NewNop())

	mockSQS.AssertExpectations(t)
}
