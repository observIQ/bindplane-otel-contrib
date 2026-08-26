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
	"testing"
	"time"

	subscriber "cloud.google.com/go/pubsub/apiv1"
	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/pstest"
	"cloud.google.com/go/storage"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/observiq/bindplane-otel-contrib/internal/blobstream"
)

// psSub stands up a pstest server with one subscription and a pulled message, and
// returns a real subscriber client wired to it.
type psSub struct {
	client       *subscriber.SubscriberClient
	srv          *pstest.Server
	subscription string
	ackID        string
	messageID    string
}

func newPSTestSub(t *testing.T) *psSub {
	t.Helper()
	ctx := context.Background()

	srv := pstest.NewServer()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	pubClient, err := subscriber.NewSubscriberClient(ctx, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = pubClient.Close() })

	const topic = "projects/test/topics/t"
	const sub = "projects/test/subscriptions/s"
	_, err = srv.GServer.CreateTopic(ctx, &pubsubpb.Topic{Name: topic})
	require.NoError(t, err)
	_, err = pubClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: sub, Topic: topic, AckDeadlineSeconds: 600,
	})
	require.NoError(t, err)

	mid := srv.Publish(topic, []byte("{}"), nil)
	pull, err := pubClient.Pull(ctx, &pubsubpb.PullRequest{Subscription: sub, MaxMessages: 1})
	require.NoError(t, err)
	require.Len(t, pull.ReceivedMessages, 1)

	return &psSub{client: pubClient, srv: srv, subscription: sub, ackID: pull.ReceivedMessages[0].AckId, messageID: mid}
}

// TestExtendAckDeadline_NilSubClient covers the early return when no subscriber is set.
func TestExtendAckDeadline_NilSubClient(t *testing.T) {
	t.Parallel()
	w := &Worker{}
	w.extendAckDeadline(context.Background(), "ack", "sub", zap.NewNop())
}

// TestExtendAckDeadline_ContextCancel covers the ctx.Done() exit.
func TestExtendAckDeadline_ContextCancel(t *testing.T) {
	t.Parallel()
	ps := newPSTestSub(t)
	fc := clockwork.NewFakeClock()
	w := &Worker{subClient: ps.client, clock: fc, maxExtension: time.Hour, logger: zap.NewNop()}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.extendAckDeadline(ctx, ps.ackID, ps.subscription, zap.NewNop()); close(done) }()

	fc.BlockUntil(1)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("extendAckDeadline did not return on cancel")
	}
}

// TestExtendAckDeadline_SuccessThenMax covers a successful extension followed by the
// maximum-extension stop, using an advanceable fake clock.
func TestExtendAckDeadline_SuccessThenMax(t *testing.T) {
	t.Parallel()
	ps := newPSTestSub(t)
	fc := clockwork.NewFakeClock()
	w := &Worker{subClient: ps.client, clock: fc, maxExtension: 20 * time.Second, logger: zap.NewNop()}

	done := make(chan struct{})
	go func() {
		w.extendAckDeadline(context.Background(), ps.ackID, ps.subscription, zap.NewNop())
		close(done)
	}()

	// First tick (15s): 15s < 20s, so the deadline is extended.
	fc.BlockUntil(1)
	fc.Advance(15 * time.Second)
	require.Eventually(t, func() bool {
		for _, m := range ps.srv.Message(ps.messageID).Modacks {
			if m.AckDeadline == 30 {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond, "expected an ack-deadline extension")

	// Second tick (30s total): 30s >= 20s, so it stops.
	fc.Advance(15 * time.Second)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("extendAckDeadline did not stop at max extension")
	}
}

// TestExtendAckDeadline_ModifyError covers the branch where the extension RPC fails.
func TestExtendAckDeadline_ModifyError(t *testing.T) {
	t.Parallel()
	ps := newPSTestSub(t)
	fc := clockwork.NewFakeClock()
	w := &Worker{subClient: ps.client, clock: fc, maxExtension: time.Hour, logger: zap.NewNop()}

	done := make(chan struct{})
	go func() {
		// A subscription that does not exist makes ModifyAckDeadline fail.
		w.extendAckDeadline(context.Background(), "bad", "projects/test/subscriptions/missing", zap.NewNop())
		close(done)
	}()

	fc.BlockUntil(1)
	fc.Advance(15 * time.Second)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("extendAckDeadline did not return on RPC error")
	}
}

// TestNackMessage_ErrorLogged covers the nack error branch.
func TestNackMessage_ErrorLogged(t *testing.T) {
	t.Parallel()
	ps := newPSTestSub(t)
	w := &Worker{subClient: ps.client, logger: zap.NewNop()}
	w.nackMessage(context.Background(), "bad", "projects/test/subscriptions/missing")
}

// TestRecordDLQMetrics_AllKinds covers every DLQ metric branch, plus the nil-metrics
// short-circuit.
func TestRecordDLQMetrics_AllKinds(t *testing.T) {
	t.Parallel()

	w := newGCSConsumeWorker(t, nil, nil)
	ctx := context.Background()

	w.recordDLQMetrics(ctx, storage.ErrObjectNotExist)
	w.recordDLQMetrics(ctx, &googleapi.Error{Code: 403})
	w.recordDLQMetrics(ctx, blobstream.ErrUnsupportedContent{MIMEType: "image/png"})
	w.recordDLQMetrics(ctx, errors.New("generic failure"))

	w.metrics = nil
	w.recordDLQMetrics(ctx, storage.ErrObjectNotExist)
}

// TestHandleProcessingError_Paths covers the cancellation, DLQ, and transient
// branches. subClient is nil, so nackMessage returns without a real RPC.
func TestHandleProcessingError_Paths(t *testing.T) {
	t.Parallel()

	w := newGCSConsumeWorker(t, nil, nil)
	ctx := context.Background()

	w.handleProcessingError(ctx, "ack", "sub", context.Canceled, zap.NewNop())
	w.handleProcessingError(ctx, "ack", "sub", blobstream.ErrUnsupportedContent{MIMEType: "image/png"}, zap.NewNop())
	w.handleProcessingError(ctx, "ack", "sub", errors.New("transient stream failure"), zap.NewNop())
}

// TestWithMaxExtension covers the WithMaxExtension option.
func TestWithMaxExtension(t *testing.T) {
	t.Parallel()

	w := &Worker{}
	WithMaxExtension(5 * time.Minute)(w)
	require.Equal(t, 5*time.Minute, w.maxExtension)
}
