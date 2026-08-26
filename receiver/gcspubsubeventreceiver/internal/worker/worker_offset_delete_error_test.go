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
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProcessMessage_LogsOffsetDeleteFailure asserts that after a fully-read object is
// acked, a failure to delete its stored offset is logged rather than escalated. The stale
// offset is harmless once the object is acked, so the delete error must not fail the run.
func TestProcessMessage_LogsOffsetDeleteFailure(t *testing.T) {
	store := newMemStorage()
	store.deleteErr = errors.New("storage extension unavailable")

	body, _ := objectLines(0, 5)
	h := newGCSHarness(t, finalizeAttrs(), 1000, store, fakeGCS(t, body, "", false), func() {}, 0)

	require.True(t, h.process(context.Background()), "a complete read should report success")
	require.True(t, h.logged("failed to delete offset"),
		"a failed offset delete after a successful ack is logged, not escalated")
}
