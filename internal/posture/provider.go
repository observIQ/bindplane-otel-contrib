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

package posture

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// Provider supplies the current posture level and notifies subscribers when it
// changes.
type Provider interface {
	// Current returns the current effective posture level.
	Current() Level
	// Subscribe returns a channel that receives the effective level whenever it
	// changes, plus a function to unsubscribe and close the channel. The channel
	// is buffered to depth one and only ever holds the latest level (older
	// pending values are dropped), so a slow consumer never blocks the provider.
	Subscribe() (<-chan Level, func())
	// RecordExportResult feeds an export success/failure into the auto-detect
	// source. It is a no-op when auto-detect is disabled.
	RecordExportResult(success bool)
}

// Controller is a Provider that also owns the lifecycle of its sources. The
// component that constructs the provider (the posture extension, or the
// processor in inline mode) drives Start/Shutdown; read-only consumers only
// depend on Provider.
type Controller interface {
	Provider
	// Start begins watching all enabled sources.
	Start(ctx context.Context) error
	// Shutdown stops all sources and closes subscriber channels.
	Shutdown()
}

// source is an internal posture input that votes on a level.
type source interface {
	name() string
	start(ctx context.Context) error
	stop()
}

// provider is the default Provider implementation. It combines the votes of its
// enabled sources using a most-restrictive-wins (minimum level) policy.
type provider struct {
	logger *zap.Logger
	levels LevelSet
	def    Level

	mu        sync.Mutex
	votes     map[string]Level
	effective Level

	subsMu sync.Mutex
	subs   map[int]chan Level
	nextID int

	sources  []source
	detector *exportHealthDetector // nil when auto-detect disabled

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewProvider builds a Controller from cfg. Call Start to begin watching sources.
func NewProvider(cfg Config, logger *zap.Logger) (Controller, error) {
	ls, err := NewLevelSet(cfg.levelsOrDefault())
	if err != nil {
		return nil, err
	}

	def := ls.Min()
	if cfg.Default != "" {
		if def, err = ls.Parse(cfg.Default); err != nil {
			return nil, fmt.Errorf("default: %w", err)
		}
	}

	p := &provider{
		logger:    logger,
		levels:    ls,
		def:       def,
		votes:     make(map[string]Level),
		effective: def,
		subs:      make(map[int]chan Level),
	}

	if sf := cfg.SignalFile; sf != nil {
		p.votes["signal_file"] = def
		p.sources = append(p.sources, newSignalFileSource(sf, ls, p.logger, p.setVote))
	}
	if cs := cfg.ControlServer; cs != nil {
		p.votes["control_server"] = def
		p.sources = append(p.sources, newControlSource(cs, ls, p.logger, p.setVote, p.snapshot))
	}
	if ac := cfg.AutoDetect; ac != nil {
		p.votes["auto_detect"] = def
		p.detector = newExportHealthDetector(ac, ls, def, p.logger, p.setVote)
	}

	return p, nil
}

// Start begins watching all enabled sources.
func (p *provider) Start(ctx context.Context) error {
	bgCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	for _, s := range p.sources {
		if err := s.start(bgCtx); err != nil {
			cancel()
			return fmt.Errorf("start %s source: %w", s.name(), err)
		}
	}
	_ = ctx
	return nil
}

// Shutdown stops all sources and closes subscriber channels.
func (p *provider) Shutdown() {
	if p.cancel != nil {
		p.cancel()
	}
	for _, s := range p.sources {
		s.stop()
	}
	p.wg.Wait()

	p.subsMu.Lock()
	for id, ch := range p.subs {
		close(ch)
		delete(p.subs, id)
	}
	p.subsMu.Unlock()
}

// Current returns the current effective level.
func (p *provider) Current() Level {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effective
}

// RecordExportResult forwards an export result to the auto-detect source.
func (p *provider) RecordExportResult(success bool) {
	if p.detector != nil {
		p.detector.record(success)
	}
}

// Subscribe registers a new subscriber.
func (p *provider) Subscribe() (<-chan Level, func()) {
	ch := make(chan Level, 1)
	p.subsMu.Lock()
	id := p.nextID
	p.nextID++
	p.subs[id] = ch
	p.subsMu.Unlock()

	unsubscribe := func() {
		p.subsMu.Lock()
		defer p.subsMu.Unlock()
		if c, ok := p.subs[id]; ok {
			close(c)
			delete(p.subs, id)
		}
	}
	return ch, unsubscribe
}

// snapshot returns the effective level and a copy of per-source votes for the
// control server's status response.
func (p *provider) snapshot() (Level, map[string]Level) {
	p.mu.Lock()
	defer p.mu.Unlock()
	votes := make(map[string]Level, len(p.votes))
	for k, v := range p.votes {
		votes[k] = v
	}
	return p.effective, votes
}

// setVote records a source's vote and recomputes the effective level
// (minimum across all votes). Subscribers are notified if it changed.
func (p *provider) setVote(sourceKey string, level Level) {
	level = p.levels.Clamp(level)

	p.mu.Lock()
	p.votes[sourceKey] = level
	newEffective := p.def
	first := true
	for _, v := range p.votes {
		if first || v < newEffective {
			newEffective = v
			first = false
		}
	}
	changed := newEffective != p.effective
	p.effective = newEffective
	p.mu.Unlock()

	if !changed {
		return
	}
	p.logger.Info("posture level changed",
		zap.String("source", sourceKey),
		zap.String("source_level", p.levels.Name(level)),
		zap.String("effective_level", p.levels.Name(newEffective)),
	)
	p.notify(newEffective)
}

// notify sends the latest level to every subscriber without blocking. A
// subscriber's buffered channel is drained first so it always holds the newest
// value.
func (p *provider) notify(level Level) {
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	for _, ch := range p.subs {
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- level:
		default:
		}
	}
}
