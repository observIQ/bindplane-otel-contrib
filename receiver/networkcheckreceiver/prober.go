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

package networkcheckreceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver"

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
)

// targetResult is one target's outcome for a single probe cycle.
type targetResult struct {
	target *targetState

	// startedAt is when this target's probe began, used as the log record
	// timestamp so a slow check lands at its start rather than its completion.
	startedAt time.Time

	ping    PingResult
	pingErr error

	// traced is true when traceroute ran for this target on this cycle;
	// shouldRun only fires every Nth check.
	traced   bool
	trace    TraceResult
	traceErr error
}

// probeCycle is one pass over the active batch of targets. Both the metrics and
// the logs path render the same cycle, so the two signals always describe the
// same observation.
type probeCycle struct {
	// at is when probing began, used as the timestamp on emitted telemetry.
	at time.Time

	// requestedAt is when the cycle was asked for, before any jitter delay.
	// Freshness is measured from this: charging the jitter wait against the
	// freshness budget would let a jitter above 10% of the collection interval
	// make the cached cycle look fresh at the next tick, so no probe would run
	// and the previous results would be re-emitted under their original
	// timestamp.
	requestedAt time.Time

	results []targetResult
}

// sharedProber owns the probing state for one receiver instance. A receiver ID
// wired into both a metrics and a logs pipeline is instantiated twice by the
// collector; without sharing, every probe would run twice and the target would
// see double the traffic. Instances are refcounted through acquireProber and
// released on shutdown.
type sharedProber struct {
	cfg      *Config
	logger   *zap.Logger
	settings receiver.Settings

	mu          sync.Mutex
	started     bool
	targets     []*targetState
	systemDNS   string
	batchOffset int

	last *probeCycle

	// cycles counts completed probe cycles. Tests assert on it to prove two
	// signals share one cycle rather than probing independently.
	cycles int
}

var (
	proberRegistryMu sync.Mutex
	proberRegistry   = map[component.ID]*proberEntry{}
)

type proberEntry struct {
	prober *sharedProber
	refs   int
}

// acquireProber returns the prober for id, creating it on first use. Each call
// must be paired with releaseProber.
func acquireProber(id component.ID, cfg *Config, settings receiver.Settings) *sharedProber {
	proberRegistryMu.Lock()
	defer proberRegistryMu.Unlock()

	if e, ok := proberRegistry[id]; ok {
		e.refs++
		return e.prober
	}
	p := &sharedProber{cfg: cfg, logger: settings.Logger, settings: settings}
	proberRegistry[id] = &proberEntry{prober: p, refs: 1}
	return p
}

// releaseProber drops a reference and removes the prober once the last signal
// using it has shut down.
func releaseProber(id component.ID) {
	proberRegistryMu.Lock()
	defer proberRegistryMu.Unlock()

	e, ok := proberRegistry[id]
	if !ok {
		return
	}
	e.refs--
	if e.refs <= 0 {
		delete(proberRegistry, id)
	}
}

// start builds probe state. Safe to call once per signal; only the first call
// does work.
func (p *sharedProber) start(_ context.Context, _ component.Host) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil
	}

	p.systemDNS = detectSystemDNS()

	icmpAvailable, icmpPrivileged := checkICMPMode()
	if !icmpAvailable {
		p.logger.Warn("ICMP unavailable (raw and datagram modes both failed); ICMP targets will fall back to HTTP probing")
	} else if !icmpPrivileged {
		p.logger.Info("ICMP running in datagram (unprivileged) mode")
	}

	// Built locally and published only on success: a mid-loop failure that left
	// a partial list on the prober would be appended to a second time when the
	// other signal calls start, and every target already built would be probed
	// twice per cycle.
	var targets []*targetState

	for i, tc := range p.cfg.Targets {
		method := tc.Method
		if method == "" {
			method = MethodICMP
		}

		dnsServer := tc.DNSServer
		if dnsServer == "" {
			dnsServer = p.systemDNS
		}

		var pg pinger
		var err error

		if method == MethodICMP && !icmpAvailable {
			p.logger.Warn("falling back to HTTP for ICMP target",
				zap.String("target", redactEndpoint(tc.Endpoint)),
				zap.Int("index", i),
			)
			method = MethodHTTP
		}

		switch method {
		case MethodICMP:
			pg = newICMPPinger(tc, icmpPrivileged)
		case MethodDNS:
			pg = newDNSPinger(tc)
		default:
			fallbackTC := tc
			if !strings.HasPrefix(fallbackTC.Endpoint, "http://") && !strings.HasPrefix(fallbackTC.Endpoint, "https://") {
				fallbackTC.Endpoint = "http://" + fallbackTC.Endpoint
			}
			pg, err = newHTTPPinger(fallbackTC, dnsServer)
			if err != nil {
				return fmt.Errorf("target[%d] HTTP pinger: %w", i, err)
			}
		}

		targets = append(targets, &targetState{
			cfg:       tc,
			p:         pg,
			tr:        newTracerouter(p.cfg.Traceroute, tc.Endpoint),
			dnsServer: dnsServer,
		})
	}

	p.targets = targets
	p.started = true
	return nil
}

// latestCycle returns the most recent probe cycle, running a new one only when
// the cached cycle is older than maxAge. Callers serialize on the mutex, so a
// second signal arriving while a cycle is in flight waits and receives that
// same cycle rather than starting its own.
func (p *sharedProber) latestCycle(ctx context.Context, maxAge time.Duration) *probeCycle {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.last != nil && time.Since(p.last.requestedAt) < maxAge {
		return p.last
	}

	p.last = p.runCycle(ctx, time.Now())
	p.cycles++
	return p.last
}

// runCycle probes the active batch. Caller must hold p.mu.
func (p *sharedProber) runCycle(ctx context.Context, requestedAt time.Time) *probeCycle {
	if p.cfg.Jitter > 0 {
		delay := time.Duration(rand.Int64N(int64(p.cfg.Jitter)))
		select {
		case <-ctx.Done():
			return &probeCycle{at: time.Now(), requestedAt: requestedAt}
		case <-time.After(delay):
		}
	}

	batch := p.activeBatch()
	cycle := &probeCycle{
		at:          time.Now(),
		requestedAt: requestedAt,
		results:     make([]targetResult, 0, len(batch)),
	}

	for _, ts := range batch {
		tr := targetResult{target: ts, startedAt: time.Now()}

		tr.ping, tr.pingErr = ts.p.ping(ctx)
		if tr.pingErr != nil {
			cycle.results = append(cycle.results, tr)
			continue
		}

		ts.checkCount++
		if ts.tr.shouldRun(ts.checkCount, tr.ping) {
			tr.traced = true
			tr.trace, tr.traceErr = ts.tr.trace(ctx)
		}

		cycle.results = append(cycle.results, tr)
	}

	if p.cfg.BatchSize > 0 && len(p.targets) > 0 {
		p.batchOffset = (p.batchOffset + len(batch)) % len(p.targets)
	}

	return cycle
}

// activeBatch returns the slice of targets to probe this cycle. Caller must
// hold p.mu.
func (p *sharedProber) activeBatch() []*targetState {
	size := p.cfg.BatchSize
	if size <= 0 || size >= len(p.targets) {
		return p.targets
	}

	batch := make([]*targetState, 0, size)
	for i := 0; i < size; i++ {
		batch = append(batch, p.targets[(p.batchOffset+i)%len(p.targets)])
	}
	return batch
}

// cycleMaxAge is how stale a cached cycle may be before a caller triggers a new
// one. Slightly under the collection interval so two signals ticking on the
// same schedule share a cycle, while a single signal still probes every tick.
func (p *sharedProber) cycleMaxAge() time.Duration {
	interval := p.cfg.CollectionInterval
	if interval <= 0 {
		return 0
	}
	return interval - interval/10
}
