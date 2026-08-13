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
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
)

func newVisibilityClockWorker(clock clockwork.Clock, mockSQS *mocks.MockSQSClient, maxWindow time.Duration) *Worker {
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	return &Worker{
		client:                      mockClient,
		clock:                       clock,
		visibilityTimeout:           10 * time.Second,
		visibilityExtensionInterval: 10 * time.Second,
		maxVisibilityWindow:         maxWindow,
	}
}

func visibilityTestMessage() types.Message {
	return types.Message{MessageId: aws.String("m1"), ReceiptHandle: aws.String("rh1")}
}

// TestExtendMessageVisibility_ExtendsOnFakeClock asserts the visibility monitor drives its
// timer off the injected clock: advancing the fake clock to the first extension point
// extends the message by the extension interval, with no wall-clock wait.
func TestExtendMessageVisibility_ExtendsOnFakeClock(t *testing.T) {
	t.Parallel()

	fake := clockwork.NewFakeClock()
	mockSQS := &mocks.MockSQSClient{}
	var timeout atomic.Int64
	mockSQS.EXPECT().ChangeMessageVisibility(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
			timeout.Store(int64(in.VisibilityTimeout))
			return &sqs.ChangeMessageVisibilityOutput{}, nil
		})
	w := newVisibilityClockWorker(fake, mockSQS, time.Hour) // large window: never extend-to-max

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { w.extendMessageVisibility(ctx, visibilityTestMessage(), "q", zap.NewNop()); close(done) }()

	fake.BlockUntil(1) // wait until the monitor's timer is registered
	fake.Advance(getSafetyMargin(10 * time.Second))

	require.Eventually(t, func() bool { return timeout.Load() == 10 }, time.Second, 5*time.Millisecond,
		"the timer firing off the fake clock must extend visibility by the extension interval")
	cancel()
	<-done
}

// TestExtendMessageVisibility_ExtendsToMaxAndStops asserts that when the next extension
// would exceed the maximum window, the monitor extends by the remaining time and stops,
// all driven off the injected clock.
func TestExtendMessageVisibility_ExtendsToMaxAndStops(t *testing.T) {
	t.Parallel()

	fake := clockwork.NewFakeClock()
	mockSQS := &mocks.MockSQSClient{}
	var timeout atomic.Int64
	mockSQS.EXPECT().ChangeMessageVisibility(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
			timeout.Store(int64(in.VisibilityTimeout))
			return &sqs.ChangeMessageVisibilityOutput{}, nil
		})
	w := newVisibilityClockWorker(fake, mockSQS, 6*time.Second) // small window: extend-to-max on first fire

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { w.extendMessageVisibility(ctx, visibilityTestMessage(), "q", zap.NewNop()); close(done) }()

	fake.BlockUntil(1)
	fake.Advance(getSafetyMargin(10 * time.Second)) // now at 5s; next interval (10s) would pass the 6s window

	select {
	case <-done: // extend-to-max returns and the monitor stops on its own
	case <-time.After(time.Second):
		t.Fatal("extendMessageVisibility did not stop after extending to max")
	}
	require.Equal(t, int64(1), timeout.Load(), "extend-to-max uses the remaining window (1s) as the timeout")
}
