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

// Internal test file — uses package worker to access unexported symbols.
package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsCancellation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "context.Canceled", err: context.Canceled, want: true},
		{name: "wrapped context.Canceled", err: fmt.Errorf("read object: %w", context.Canceled), want: true},
		{name: "context.DeadlineExceeded", err: context.DeadlineExceeded, want: true},
		{name: "wrapped context.DeadlineExceeded", err: fmt.Errorf("read object: %w", context.DeadlineExceeded), want: true},
		{name: "unrelated error", err: errors.New("connection reset by peer"), want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, isCancellation(tc.err))
		})
	}
}

func TestCleanupContext_OutlivesParentCancellation(t *testing.T) {
	t.Parallel()

	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()

	cleanup, cancel := cleanupContext(parent)
	defer cancel()

	require.NoError(t, cleanup.Err(), "wind-down work must not inherit the parent's cancellation")

	deadline, ok := cleanup.Deadline()
	require.True(t, ok, "wind-down work must be bounded by a deadline")
	require.WithinDuration(t, time.Now().Add(cleanupTimeout), deadline, time.Second)

	cancel()
	require.ErrorIs(t, cleanup.Err(), context.Canceled, "the returned cancel must still release the context")
}

func TestDrainContext_FollowsLiveParentAndDetachesFromCancelledOne(t *testing.T) {
	t.Parallel()

	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()

	drain, cancelDrain := drainContext(live)
	require.NoError(t, drain.Err())
	cancelLive()
	require.ErrorIs(t, drain.Err(), context.Canceled, "a live parent still governs the drain")
	cancelDrain()

	dead, cancelDead := context.WithCancel(context.Background())
	cancelDead()

	detached, cancelDetached := drainContext(dead)
	defer cancelDetached()
	require.NoError(t, detached.Err(), "an already-cancelled parent must not cancel the drain")
}

func TestDLQConditionKind_CancellationIsNeverDLQ(t *testing.T) {
	t.Parallel()

	// A config push cancels the context mid-object. Routing that to the dead-letter
	// queue would send good data to the DLQ on every config change.
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("read object: %w", context.Canceled),
		fmt.Errorf("read object: %w", context.DeadlineExceeded),
	} {
		require.Equal(t, dlqErrorKindNone, dlqConditionKind(err), "err: %v", err)
		require.False(t, isDLQConditionError(err), "err: %v", err)
	}
}
