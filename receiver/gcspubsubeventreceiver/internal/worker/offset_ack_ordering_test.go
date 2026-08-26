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

	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/worker"
)

// TestProcessMessage_OffsetKeptWhenAckFails asserts the object's saved offset is not
// deleted when the ack fails. Deleting it before a successful ack means a redelivery
// (the message was never acked) reprocesses the whole object from the start, duplicating
// every record. The offset must survive an ack failure as the resume point.
func TestProcessMessage_OffsetKeptWhenAckFails(t *testing.T) {
	store := newMemStorage()
	// A complete object: the head exceeds the content-detection window, then a final line.
	client := fakeGCS(t, strings.Repeat("x", 4000)+"\n", "line\n", false)
	h := newGCSHarness(t, finalizeAttrs(), 1000, store, client, func() {}, 0)

	msg := &worker.PullMessage{
		AckID:      h.pubsub.ackID,
		MessageID:  h.pubsub.messageID,
		Attributes: h.pubsub.srv.Message(h.pubsub.messageID).Attributes,
	}
	// A subscription that does not exist makes Acknowledge fail fast with NotFound (a
	// non-retryable error), while the GCS read uses a separate httptest server and still
	// succeeds, so processing completes and only the ack fails.
	badSub := "projects/test/subscriptions/does-not-exist"
	h.worker.ProcessMessage(context.Background(), msg, badSub, func() {})

	offsetKey := fmt.Sprintf("%s_%s", worker.OffsetStorageKey, "myobject")
	require.NotContains(t, store.deleted, offsetKey,
		"offset must be preserved when the ack fails, so a redelivery resumes rather than reprocessing")
}
