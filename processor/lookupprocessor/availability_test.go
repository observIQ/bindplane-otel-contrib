// Copyright  observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lookupprocessor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func fakeAvailability(t *testing.T) (*availability, *clockwork.FakeClock, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	clock := clockwork.NewFakeClock()
	a := newAvailability(zap.New(core), "test")
	a.clock = clock
	return a, clock, logs
}

func TestAvailability_UpNeverSkips(t *testing.T) {
	a, _, logs := fakeAvailability(t)
	for i := 0; i < 3; i++ {
		require.False(t, a.skip())
	}
	a.markUp()
	require.Empty(t, logs.All(), "no transition, no log")
}

func TestAvailability_DownSkipsUntilWindowThenOneProbe(t *testing.T) {
	a, clock, logs := fakeAvailability(t)
	boom := errors.New("boom")

	a.markDown(boom)
	a.markDown(boom)
	require.Len(t, logs.FilterLevelExact(zapcore.WarnLevel).All(), 1, "one Warn per outage")

	require.True(t, a.skip(), "inside the window every caller skips")
	clock.Advance(sourceRetryInterval - time.Millisecond)
	require.True(t, a.skip())

	clock.Advance(time.Millisecond)
	require.False(t, a.skip(), "first caller after the window probes")
	require.True(t, a.skip(), "second caller in the same window skips")

	a.markDown(boom)
	require.True(t, a.skip(), "a failed probe opens a fresh window")
	require.Len(t, logs.FilterLevelExact(zapcore.WarnLevel).All(), 1, "still one Warn")

	clock.Advance(sourceRetryInterval)
	require.False(t, a.skip())
	a.markUp()
	require.False(t, a.skip(), "recovered source never skips")
	require.Len(t, logs.FilterMessageSnippet("reachable again").All(), 1)
}

func TestAvailability_OneProbeAcrossConcurrentCallers(t *testing.T) {
	a, clock, _ := fakeAvailability(t)
	a.markDown(errors.New("boom"))
	clock.Advance(sourceRetryInterval)

	var probes int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !a.skip() {
				mu.Lock()
				probes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, probes, "exactly one goroutine wins the probe")
}

func TestAvailability_StartDownIsQuiet(t *testing.T) {
	a, _, logs := fakeAvailability(t)
	a.startDown()
	require.True(t, a.skip())
	require.Empty(t, logs.All(), "startup wording is the caller's; startDown itself logs nothing")
}

func TestRedisSource_SkipsNetworkWhileDown(t *testing.T) {
	// Refused connections take ~1.7s per lookup through go-redis retries; a
	// skipped lookup must return without touching the network at all.
	src, err := NewRedisSource(&RedisConfig{Address: unreachableAddr}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	start := time.Now()
	for i := 0; i < 100; i++ {
		_, err := src.Lookup(context.Background(), "k")
		require.ErrorIs(t, err, errSourceUnavailable)
	}
	require.Less(t, time.Since(start), 500*time.Millisecond, "100 skipped lookups must not dial")
}
