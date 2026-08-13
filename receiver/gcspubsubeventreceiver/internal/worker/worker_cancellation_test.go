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
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	publisher "cloud.google.com/go/pubsub/apiv1"
	subscriber "cloud.google.com/go/pubsub/apiv1"
	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/pstest"
	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/worker"
)

// memStorage is an in-memory StorageClient that obeys context cancellation. A test can
// then tell whether a checkpoint ran on a live context.
type memStorage struct {
	mu        sync.Mutex
	data      map[string][]byte
	deleted   []string // keys passed to DeleteStorageData, for tests asserting delete ordering
	deleteErr error    // when set, DeleteStorageData fails with it, for the delete-error path
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
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, key)
	delete(m.data, key)
	return nil
}

func (m *memStorage) Close(context.Context) error { return nil }

// fakeGCS serves an object as head followed by tail, so the storage client can stream
// from an httptest server rather than real GCS. When hold is set the handler stops after
// head and keeps the response open until the request context is cancelled, which is what
// puts a cancellation partway through the worker's read rather than before it starts.
// head must exceed the content-detection window, or the worker blocks before parsing.
func fakeGCS(t *testing.T, head, tail string, hold bool) *storage.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Object metadata lookups get a minimal JSON payload; everything else is a read.
		if r.URL.Query().Get("alt") != "media" && strings.Contains(r.URL.Path, "/storage/v1/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"myobject","bucket":"mybucket","contentType":"text/plain"}`)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(head)+len(tail)))
		_, _ = io.WriteString(w, head)
		if hold {
			// The declared length is never satisfied, so the client keeps waiting until
			// its request context is cancelled.
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, tail)
	}))
	t.Cleanup(srv.Close)

	client, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL),
		option.WithoutAuthentication(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// fakePubSub stands up a pstest server with one topic and subscription, publishes a
// message carrying attrs, and pulls it so the returned ack ID is live.
type fakePubSub struct {
	srv          *pstest.Server
	client       *subscriber.SubscriberClient
	subscription string
	messageID    string
	ackID        string
}

func newFakePubSub(t *testing.T, attrs map[string]string) *fakePubSub {
	t.Helper()

	ctx := context.Background()
	srv := pstest.NewServer()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	pubClient, err := publisher.NewPublisherClient(ctx, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = pubClient.Close() })

	subClient, err := subscriber.NewSubscriberClient(ctx, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = subClient.Close() })

	const topic = "projects/test/topics/test-topic"
	const subscription = "projects/test/subscriptions/test-sub"

	_, err = pubClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topic})
	require.NoError(t, err)
	_, err = subClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:               subscription,
		Topic:              topic,
		AckDeadlineSeconds: 600,
	})
	require.NoError(t, err)

	messageID := srv.Publish(topic, []byte("{}"), attrs)

	pull, err := subClient.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: subscription,
		MaxMessages:  1,
	})
	require.NoError(t, err)
	require.Len(t, pull.ReceivedMessages, 1)

	return &fakePubSub{
		srv:          srv,
		client:       subClient,
		subscription: subscription,
		messageID:    messageID,
		ackID:        pull.ReceivedMessages[0].AckId,
	}
}

func (f *fakePubSub) acks() int {
	return f.srv.Message(f.messageID).Acks
}

// nacked reports whether the message had its ack deadline set to 0, which is how the
// worker makes a message immediately available for redelivery.
func (f *fakePubSub) nacked() bool {
	for _, m := range f.srv.Message(f.messageID).Modacks {
		if m.AckDeadline == 0 {
			return true
		}
	}
	return false
}

type gcsHarness struct {
	worker *worker.Worker
	pubsub *fakePubSub
	sink   *consumertest.LogsSink
	logs   *observer.ObservedLogs
}

// newGCSHarness builds a worker over a fake Pub/Sub and, when storageClient is non-nil, a
// fake GCS. cancelAfterBatches makes the downstream consumer cancel the worker's context
// once it has accepted that many batches, which is how a config push lands partway
// through an object. Zero leaves the context alone.
func newGCSHarness(t *testing.T, attrs map[string]string, maxLogsEmitted int, store storageclient.StorageClient,
	storageClient *storage.Client, cancel context.CancelFunc, cancelAfterBatches int) *gcsHarness {
	t.Helper()

	ps := newFakePubSub(t, attrs)

	core, recorded := observer.New(zap.DebugLevel)
	set := componenttest.NewNopTelemetrySettings()
	set.Logger = zap.New(core)

	tb, err := metadata.NewTelemetryBuilder(set)
	require.NoError(t, err)

	params := receivertest.NewNopSettings(metadata.Type)
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             params.ID,
		Transport:              "pubsub",
		ReceiverCreateSettings: params,
	})
	require.NoError(t, err)

	sink := new(consumertest.LogsSink)
	batches := 0
	// Downstream consumers reject a cancelled context, so the sink must also reject it.
	next, err := consumer.NewLogs(func(ctx context.Context, ld plog.Logs) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sink.ConsumeLogs(ctx, ld); err != nil {
			return err
		}
		batches++
		if cancelAfterBatches > 0 && batches == cancelAfterBatches {
			cancel()
		}
		return nil
	})
	require.NoError(t, err)

	w := worker.New(set, next, storageClient, obsrecv, 4096, maxLogsEmitted,
		worker.WithTelemetryBuilder(tb),
		worker.WithSubscriberClient(ps.client),
	)
	w.SetOffsetStorage(store)

	return &gcsHarness{worker: w, pubsub: ps, sink: sink, logs: recorded}
}

func (h *gcsHarness) process(ctx context.Context) bool {
	msg := &worker.PullMessage{
		AckID:      h.pubsub.ackID,
		MessageID:  h.pubsub.messageID,
		Attributes: h.pubsub.srv.Message(h.pubsub.messageID).Attributes,
	}
	return h.worker.ProcessMessage(ctx, msg, h.pubsub.subscription, func() {})
}

func (h *gcsHarness) bodyStrings() []string {
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

func (h *gcsHarness) logged(msg string) bool {
	for _, e := range h.logs.All() {
		if e.Message == msg {
			return true
		}
	}
	return false
}

func finalizeAttrs() map[string]string {
	return map[string]string{
		worker.AttrEventType: worker.EventTypeObjectFinalize,
		worker.AttrBucketID:  "mybucket",
		worker.AttrObjectID:  "myobject",
	}
}

// objectLines builds n newline-terminated lines and returns both the encoded bytes and
// the lines they should parse into. Lines are padded so a modest count still exceeds the
// content-detection window.
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

// TestProcessMessage_AcksFilteredMessageAfterCancellation asserts that the ack for a
// message the receiver deliberately skips still lands once the context is cancelled.
// Without it the message is redelivered forever.
func TestProcessMessage_AcksFilteredMessageAfterCancellation(t *testing.T) {
	attrs := map[string]string{
		worker.AttrEventType: "OBJECT_DELETE",
		worker.AttrBucketID:  "mybucket",
		worker.AttrObjectID:  "myobject",
	}
	h := newGCSHarness(t, attrs, 1000, newMemStorage(), nil, func() {}, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.False(t, h.process(ctx), "a non-finalize event is skipped rather than processed")
	require.Eventually(t, func() bool { return h.pubsub.acks() == 1 }, 5*time.Second, 10*time.Millisecond,
		"a skipped message must still be acked after cancellation")
}

// TestProcessMessage_CancelledMidObject asserts the wind-down contract: records already
// read are delivered and checkpointed, the message is nacked for redelivery rather than
// acked, and the cancellation is not treated as a DLQ condition.
func TestProcessMessage_CancelledMidObject(t *testing.T) {
	const batchSize = 100
	head, headLines := objectLines(0, 250)
	tail, _ := objectLines(250, 50)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newGCSHarness(t, finalizeAttrs(), batchSize, newMemStorage(), fakeGCS(t, head, tail, true), cancel, 1)

	require.False(t, h.process(ctx), "a cancelled object is not fully processed")

	delivered := h.bodyStrings()
	require.Greater(t, len(delivered), batchSize,
		"the partial batch left in hand is drained rather than dropped for redelivery")
	require.Less(t, len(delivered), len(headLines)+50,
		"a cancelled read should stop rather than draining the whole object")
	require.Equal(t, headLines[:len(delivered)], delivered,
		"delivered records should be whole records forming a prefix of the object")

	require.Eventually(t, func() bool { return h.pubsub.nacked() }, 5*time.Second, 10*time.Millisecond,
		"a cancelled message should be nacked so it redelivers immediately")
	require.Zero(t, h.pubsub.acks(), "a partially read object must not be acked")
	require.False(t, h.logged("DLQ condition triggered, nacking message for redelivery/DLQ processing"),
		"cancellation must never route an object to the DLQ")
}

// TestProcessMessage_ResumesFromCheckpointAfterCancellation asserts that the checkpoint
// written during wind-down matches what was flushed, so redelivery loses no records and
// replays none.
func TestProcessMessage_ResumesFromCheckpointAfterCancellation(t *testing.T) {
	const batchSize = 100
	head, headLines := objectLines(0, 250)
	tail, tailLines := objectLines(250, 50)
	lines := append(headLines, tailLines...)
	store := newMemStorage()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newGCSHarness(t, finalizeAttrs(), batchSize, store, fakeGCS(t, head, tail, true), cancel, 1)
	first.process(ctx)
	delivered := first.bodyStrings()
	require.NotEmpty(t, delivered)

	// Redelivery: the same object, now served in full, processed on a live context.
	second := newGCSHarness(t, finalizeAttrs(), batchSize, store, fakeGCS(t, head, tail, false), func() {}, 0)
	require.True(t, second.process(context.Background()), "a complete read should report success")

	require.Equal(t, lines, append(delivered, second.bodyStrings()...),
		"the two runs together should cover the object exactly once")
	require.Eventually(t, func() bool { return second.pubsub.acks() == 1 }, 5*time.Second, 10*time.Millisecond,
		"a fully read object should be acked")
}
