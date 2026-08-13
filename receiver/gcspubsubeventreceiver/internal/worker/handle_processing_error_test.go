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

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/metadata"
)

func newTestMetrics(t *testing.T) *metadata.TelemetryBuilder {
	t.Helper()
	tb, err := metadata.NewTelemetryBuilder(componenttest.NewNopTelemetrySettings())
	require.NoError(t, err)
	return tb
}

// TestHandleProcessingError_DeadlineExceededDoesNotHotRedeliver asserts that a downstream
// timeout (context.DeadlineExceeded) with a live context is treated as a transient failure
// (preserve for the ack deadline to lapse), not a cancellation nacked for immediate redelivery.
func TestHandleProcessingError_DeadlineExceededDoesNotHotRedeliver(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.DebugLevel)
	w := &Worker{metrics: newTestMetrics(t)} // subClient nil: nack is a no-op

	w.handleProcessingError(context.Background(), "ack-id", "sub", context.DeadlineExceeded, zap.New(core))

	require.Equal(t, 1, logs.FilterMessage("error processing record, preserving message for retry").Len(),
		"a live-context downstream timeout must take the preserve-for-retry path")
	require.Zero(t, logs.FilterMessage("processing cancelled, nacking message for redelivery").Len(),
		"a downstream timeout must not be treated as a cancellation")
}

// TestHandleProcessingError_ShutdownNacksImmediately asserts that a cancelled context (a
// shutdown / config push) is nacked for immediate redelivery so it resumes from the checkpoint.
func TestHandleProcessingError_ShutdownNacksImmediately(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.DebugLevel)
	w := &Worker{metrics: newTestMetrics(t)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.handleProcessingError(ctx, "ack-id", "sub", context.DeadlineExceeded, zap.New(core))

	require.Equal(t, 1, logs.FilterMessage("processing cancelled, nacking message for redelivery").Len(),
		"a cancelled context is a shutdown and must nack for immediate redelivery")
}
