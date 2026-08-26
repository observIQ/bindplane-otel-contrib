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

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.uber.org/zap"
)

func fastBackOff(enabled bool) configretry.BackOffConfig {
	return configretry.BackOffConfig{
		Enabled:             enabled,
		InitialInterval:     time.Millisecond,
		RandomizationFactor: 0,
		Multiplier:          1,
		MaxInterval:         time.Millisecond,
		MaxElapsedTime:      50 * time.Millisecond,
	}
}

// TestConsumeWithRetry_DisabledCallsOnce asserts that with backoff disabled the consume
// runs exactly once and the error is returned unchanged (prior behavior).
func TestConsumeWithRetry_DisabledCallsOnce(t *testing.T) {
	t.Parallel()
	calls := 0
	err := consumeWithRetry(context.Background(), fastBackOff(false), zap.NewNop(), func() error {
		calls++
		return errors.New("boom")
	})
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

// TestConsumeWithRetry_RetriesThenSucceeds asserts a transient failure is retried with
// backoff until it succeeds.
func TestConsumeWithRetry_RetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	calls := 0
	err := consumeWithRetry(context.Background(), fastBackOff(true), zap.NewNop(), func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}

// TestConsumeWithRetry_PermanentNotRetried asserts a permanent error bypasses backoff.
func TestConsumeWithRetry_PermanentNotRetried(t *testing.T) {
	t.Parallel()
	calls := 0
	err := consumeWithRetry(context.Background(), fastBackOff(true), zap.NewNop(), func() error {
		calls++
		return consumererror.NewPermanent(errors.New("permanent"))
	})
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

// TestConsumeWithRetry_StopsOnContextCancel asserts a cancelled context ends the retry loop.
func TestConsumeWithRetry_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := consumeWithRetry(ctx, fastBackOff(true), zap.NewNop(), func() error {
		calls++
		return errors.New("transient")
	})
	require.Error(t, err)
	require.Equal(t, 1, calls, "a cancelled context must not drive further attempts")
}

// TestConsumeWithRetry_GivesUpAfterMaxElapsed asserts the loop eventually returns the last
// error once the backoff is exhausted rather than retrying forever.
func TestConsumeWithRetry_GivesUpAfterMaxElapsed(t *testing.T) {
	t.Parallel()
	calls := 0
	err := consumeWithRetry(context.Background(), fastBackOff(true), zap.NewNop(), func() error {
		calls++
		return errors.New("always fails")
	})
	require.Error(t, err)
	require.Greater(t, calls, 1, "an exhausted backoff should have retried at least once")
}
