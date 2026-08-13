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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"

	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/metadatatest"
	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/worker"
)

// truncatedJSONArrayBody builds a JSON array that ends part way through with no closing
// ']', large enough that content detection completes before the cut.
func truncatedJSONArrayBody() string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"host":"h","msg":"padding padding padding padding"}`)
	}
	return b.String() // no closing ']'
}

// TestProcessMessage_TruncatedObjectDeliversAndAcks asserts a GCS object that ends part way
// through a record delivers the records read before the cut and acks the message, rather
// than routing the whole object to the dead-letter queue.
func TestProcessMessage_TruncatedObjectDeliversAndAcks(t *testing.T) {
	body := truncatedJSONArrayBody()

	ps := newFakePubSub(t, finalizeAttrs())
	tt := componenttest.NewTelemetry()
	defer func() { require.NoError(t, tt.Shutdown(context.Background())) }()
	set := metadatatest.NewSettings(tt).TelemetrySettings
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
	w := worker.New(set, sink, fakeGCS(t, body, "", false), obsrecv, 4096, 1000,
		worker.WithTelemetryBuilder(tb),
		worker.WithSubscriberClient(ps.client),
	)
	w.SetOffsetStorage(newMemStorage())

	msg := &worker.PullMessage{
		AckID:      ps.ackID,
		MessageID:  ps.messageID,
		Attributes: ps.srv.Message(ps.messageID).Attributes,
	}

	// ProcessMessage returns true when the message is acked (not nacked to the DLQ).
	require.True(t, w.ProcessMessage(context.Background(), msg, ps.subscription, func() {}),
		"a truncated object is delivered and acked, not routed to the DLQ")
	require.Positive(t, sink.LogRecordCount(), "the records read before the cut are delivered")

	// The truncation is surfaced through the counter (incremented once, after the ack).
	metadatatest.AssertEqualGcseventTruncatedObjects(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 1}}, metricdatatest.IgnoreTimestamp())
}

// TestProcessMessage_TruncatedObjectClearsSavedOffset asserts that once a truncated GCS
// object is delivered and acked, its saved resume offset is cleared, so a later re-upload
// under the same object name reprocesses from the beginning rather than resuming past the
// earlier cut. Duplicating a few records is preferable to dropping the re-uploaded tail.
func TestProcessMessage_TruncatedObjectClearsSavedOffset(t *testing.T) {
	body := truncatedJSONArrayBody()

	ps := newFakePubSub(t, finalizeAttrs())
	set := componenttest.NewNopTelemetrySettings()
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
	store := newMemStorage()
	w := worker.New(set, sink, fakeGCS(t, body, "", false), obsrecv, 4096, 1000,
		worker.WithTelemetryBuilder(tb),
		worker.WithSubscriberClient(ps.client),
	)
	w.SetOffsetStorage(store)

	msg := &worker.PullMessage{
		AckID:      ps.ackID,
		MessageID:  ps.messageID,
		Attributes: ps.srv.Message(ps.messageID).Attributes,
	}

	require.True(t, w.ProcessMessage(context.Background(), msg, ps.subscription, func() {}),
		"a truncated object is delivered and acked")
	require.Positive(t, sink.LogRecordCount(), "the records read before the cut are delivered")

	offsetKey := fmt.Sprintf("%s_%s", worker.OffsetStorageKey, "myobject")
	require.False(t, store.has(offsetKey), "the truncated object's saved offset must be cleared on ack")
}
