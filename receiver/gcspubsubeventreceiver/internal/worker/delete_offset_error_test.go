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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/worker"
)

// deleteErrStorage saves and loads normally but fails DeleteStorageData, to exercise
// the offset-delete error path after a successful ack.
type deleteErrStorage struct{ err error }

func (deleteErrStorage) SaveStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (deleteErrStorage) LoadStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (s deleteErrStorage) DeleteStorageData(context.Context, string) error { return s.err }
func (deleteErrStorage) Close(context.Context) error                       { return nil }

// TestProcessMessage_DeleteOffsetErrorLogged asserts a failure to delete the offset
// after a successful ack is logged and does not fail the object (it is already acked).
func TestProcessMessage_DeleteOffsetErrorLogged(t *testing.T) {
	store := deleteErrStorage{err: errors.New("storage extension unavailable")}
	client := fakeGCS(t, strings.Repeat("x", 4000)+"\n", "line\n", false)
	h := newGCSHarness(t, finalizeAttrs(), 1000, store, client, func() {}, 0)

	msg := &worker.PullMessage{
		AckID:      h.pubsub.ackID,
		MessageID:  h.pubsub.messageID,
		Attributes: h.pubsub.srv.Message(h.pubsub.messageID).Attributes,
	}
	ok := h.worker.ProcessMessage(context.Background(), msg, h.pubsub.subscription, func() {})

	require.True(t, ok, "the object is acked, so it succeeds despite the delete failure")
	require.True(t, h.logged("failed to delete offset"), "the delete failure is logged")
}
