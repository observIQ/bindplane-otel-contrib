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
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
)

// checkpointProbe blocks the first offset save so the test can cancel the parent
// context while the save is in flight, then records the context the save actually ran
// on. It stands in for a cancellation that lands during a mid-object checkpoint.
type checkpointProbe struct {
	*memStorage
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	ctxErr   error
	captured bool
}

func (s *checkpointProbe) SaveStorageData(ctx context.Context, key string, data storageclient.StorageData) error {
	probe := false
	s.once.Do(func() { probe = true })
	if probe {
		close(s.started)
		<-s.release
		s.mu.Lock()
		s.ctxErr = ctx.Err()
		s.captured = true
		s.mu.Unlock()
	}
	return s.memStorage.SaveStorageData(ctx, key, data)
}

// TestCheckpoint_SurvivesCancellationDuringSave asserts the mid-object checkpoint runs
// on a fully detached context, so a cancellation landing while the save is in flight
// cannot abort it and strand the offset. A stranded offset would replay the delivered
// batch on redelivery. The delivery flush stays on the drain context; only the offset
// save is unconditionally detached.
func TestCheckpoint_SurvivesCancellationDuringSave(t *testing.T) {
	const batchSize = 100
	head, _ := objectLines(0, 250)
	tail, _ := objectLines(250, 50)

	probe := &checkpointProbe{
		memStorage: newMemStorage(),
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A full object over a small batch size fires a mid-object checkpoint on a live
	// context before the object finishes.
	h := newGCSHarness(t, finalizeAttrs(), batchSize, probe, fakeGCS(t, head, tail, false), func() {}, 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.process(ctx)
	}()

	// Cancel the parent while the first checkpoint save is blocked in flight, then let
	// it proceed.
	<-probe.started
	cancel()
	close(probe.release)
	<-done

	probe.mu.Lock()
	defer probe.mu.Unlock()
	require.True(t, probe.captured, "the mid-object checkpoint save should have run")
	require.NoError(t, probe.ctxErr,
		"the checkpoint save must run on a detached context, so a cancellation landing mid-save cannot abort it")
}
