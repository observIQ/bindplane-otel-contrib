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

package networkcheckreceiver

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver/internal/metadata"
)

// countingPinger records how many times a target was actually probed.
type countingPinger struct {
	calls atomic.Int64
}

func (c *countingPinger) ping(_ context.Context) (PingResult, error) {
	c.calls.Add(1)
	return PingResult{Method: MethodHTTP, StatusCode: 200, TotalDuration: time.Millisecond}, nil
}

func newTestProber(t *testing.T, interval time.Duration) (*sharedProber, *countingPinger) {
	t.Helper()
	cp := &countingPinger{}
	cfg := createDefaultConfig().(*Config)
	cfg.CollectionInterval = interval

	p := &sharedProber{cfg: cfg, logger: zap.NewNop()}
	p.started = true
	p.targets = []*targetState{{
		cfg: TargetConfig{Method: MethodHTTP},
		p:   cp,
		tr:  newTracerouter(TracerouteConfig{}, "example.com"),
	}}
	return p, cp
}

// The whole reason sharedProber exists: a receiver ID wired into both a metrics
// and a logs pipeline is instantiated twice, and without sharing each instance
// would probe independently, doubling real network traffic against the target.
func TestSharedProber_TwoSignalsShareOneCycle(t *testing.T) {
	p, cp := newTestProber(t, time.Minute)
	ctx := context.Background()
	maxAge := p.cycleMaxAge()

	metricsCycle := p.latestCycle(ctx, maxAge)
	logsCycle := p.latestCycle(ctx, maxAge)

	require.Same(t, metricsCycle, logsCycle, "both signals must render the same cycle")
	require.EqualValues(t, 1, cp.calls.Load(), "target must be probed once, not once per signal")
	require.Equal(t, 1, p.cycles)
}

func TestSharedProber_ReprobesAfterMaxAge(t *testing.T) {
	// A tiny interval makes maxAge tiny, so the cached cycle expires promptly.
	p, cp := newTestProber(t, 10*time.Millisecond)
	ctx := context.Background()

	p.latestCycle(ctx, p.cycleMaxAge())
	require.EqualValues(t, 1, cp.calls.Load())

	time.Sleep(20 * time.Millisecond)

	p.latestCycle(ctx, p.cycleMaxAge())
	require.EqualValues(t, 2, cp.calls.Load(), "a stale cycle must trigger a fresh probe")
	require.Equal(t, 2, p.cycles)
}

func TestSharedProber_ConcurrentCallersProbeOnce(t *testing.T) {
	p, cp := newTestProber(t, time.Minute)
	ctx := context.Background()
	maxAge := p.cycleMaxAge()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.latestCycle(ctx, maxAge)
		}()
	}
	wg.Wait()

	require.EqualValues(t, 1, cp.calls.Load(),
		"concurrent callers must serialize onto one cycle rather than racing into parallel probes")
}

func TestProberRegistry_RefcountsAcrossSignals(t *testing.T) {
	id := component.MustNewID("networkcheck")
	cfg := createDefaultConfig().(*Config)
	settings := receivertest.NewNopSettings(metadata.Type)

	a := acquireProber(id, cfg, settings)
	b := acquireProber(id, cfg, settings)
	require.Same(t, a, b, "both signals must receive the same prober instance")

	// First shutdown must not tear down state the surviving signal still uses.
	releaseProber(id)
	proberRegistryMu.Lock()
	_, stillPresent := proberRegistry[id]
	proberRegistryMu.Unlock()
	require.True(t, stillPresent)

	releaseProber(id)
	proberRegistryMu.Lock()
	_, gone := proberRegistry[id]
	proberRegistryMu.Unlock()
	require.False(t, gone, "last release must remove the prober")
}

// A mid-loop start failure used to leave a partial target list on the shared
// prober. The second signal then ran the loop again and appended every target
// a second time, so each was probed twice per cycle — defeating the sharing.
func TestSharedProber_FailedStartLeavesNoPartialTargets(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.Targets = []TargetConfig{
		{Method: MethodHTTP},
		{Method: MethodHTTP},
	}
	cfg.Targets[0].Endpoint = "http://example.com"
	// A CA file that does not exist makes LoadTLSConfig fail, so the second
	// target cannot build a pinger and start returns mid-loop.
	cfg.Targets[1].Endpoint = "https://example.org"
	cfg.Targets[1].TLS.CAFile = filepath.Join(t.TempDir(), "missing-ca.pem")

	p := &sharedProber{cfg: cfg, logger: zap.NewNop()}
	err := p.start(context.Background(), nil)
	require.Error(t, err, "a target with an unreadable CA file must fail start")
	require.Empty(t, p.targets, "a failed start must publish no targets")
	require.False(t, p.started)

	// The second signal calling start must not append onto a partial list.
	_ = p.start(context.Background(), nil)
	require.LessOrEqual(t, len(p.targets), len(cfg.Targets),
		"targets must never exceed the configured count")
}

// Jitter was charged against the freshness budget: cycle age was measured from
// when probing began after the jitter wait, so a jitter above 10% of the
// interval made a stale cycle look fresh, skipping a tick and re-emitting the
// previous results under their original timestamp.
func TestSharedProber_JitterDoesNotConsumeFreshnessBudget(t *testing.T) {
	p, cp := newTestProber(t, 200*time.Millisecond)
	p.cfg.Jitter = 150 * time.Millisecond
	ctx := context.Background()

	first := p.latestCycle(ctx, p.cycleMaxAge())
	require.EqualValues(t, 1, cp.calls.Load())

	// Freshness runs from when the cycle was requested, so the jitter spent
	// inside it must not be credited as elapsed lifetime.
	require.False(t, first.requestedAt.IsZero())
	require.True(t, !first.at.Before(first.requestedAt),
		"probing must start at or after the cycle was requested")

	time.Sleep(p.cycleMaxAge() + 20*time.Millisecond)
	second := p.latestCycle(ctx, p.cycleMaxAge())
	require.NotSame(t, first, second, "an expired cycle must be re-probed, not re-emitted")
	require.EqualValues(t, 2, cp.calls.Load())
}
