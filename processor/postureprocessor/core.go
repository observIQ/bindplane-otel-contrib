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

package postureprocessor

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/internal/posture"
)

const (
	signalLogs    = "logs"
	signalMetrics = "metrics"
	signalTraces  = "traces"

	// defaultTierName is the reserved tier that owns telemetry matching no
	// configured tier condition.
	defaultTierName = "_default"

	// drainRetryInterval bounds how long the drain loop waits before retrying a
	// tier whose export previously failed, even without a posture change.
	drainRetryInterval = 5 * time.Second
)

// tier pairs a tier name with the minimum posture level at which it egresses.
type tier struct {
	name     string
	minLevel posture.Level
}

// emitFunc unmarshals a stored payload and forwards it to the next consumer.
type emitFunc func(ctx context.Context, payload []byte) error

// core holds the signal-agnostic machinery shared by the logs, metrics, and
// traces processors: posture wiring, the per-tier durable queues, and the
// background drain loop.
type core struct {
	logger      *zap.Logger
	cfg         *Config
	levels      posture.LevelSet
	tiers       []tier
	signal      string
	componentID component.ID

	emit emitFunc

	provider    posture.Provider
	ownProvider posture.Controller // non-nil in inline mode; we own its lifecycle

	queues []*tierQueue

	//#nosec G404 -- rng only adds jitter to drain timing, not security-sensitive
	rnd *rand.Rand

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newCore(cfg *Config, levels posture.LevelSet, tiers []tier, signal string, componentID component.ID, logger *zap.Logger) *core {
	return &core{
		logger:      logger,
		cfg:         cfg,
		levels:      levels,
		tiers:       tiers,
		signal:      signal,
		componentID: componentID,
		//#nosec G404 -- jitter only
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (c *core) start(ctx context.Context, host component.Host) error {
	if err := c.resolveProvider(ctx, host); err != nil {
		return err
	}
	client, err := c.acquireStorage(ctx, host)
	if err != nil {
		return err
	}
	if err := c.buildQueues(ctx, client); err != nil {
		return err
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.wg.Add(1)
	go c.drainLoop(bgCtx)
	return nil
}

func (c *core) shutdown(context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	if c.ownProvider != nil {
		c.ownProvider.Shutdown()
	}
	// The storage client is owned by the storage extension, which closes it on
	// its own shutdown; do not close it here to avoid a double close.
	return nil
}

func (c *core) resolveProvider(ctx context.Context, host component.Host) error {
	if c.cfg.PostureExtension != nil {
		ext, ok := host.GetExtensions()[*c.cfg.PostureExtension]
		if !ok {
			return fmt.Errorf("posture extension %q not found", c.cfg.PostureExtension)
		}
		prov, ok := ext.(posture.Provider)
		if !ok {
			return fmt.Errorf("extension %q is not a posture provider", c.cfg.PostureExtension)
		}
		c.provider = prov
		return nil
	}

	pcfg := *c.cfg.Posture
	if len(pcfg.Levels) == 0 {
		pcfg.Levels = c.cfg.levelsOrDefault()
	}
	ctrl, err := posture.NewProvider(pcfg, c.logger)
	if err != nil {
		return fmt.Errorf("inline posture: %w", err)
	}
	if err := ctrl.Start(ctx); err != nil {
		return fmt.Errorf("start inline posture: %w", err)
	}
	c.provider = ctrl
	c.ownProvider = ctrl
	return nil
}

func (c *core) acquireStorage(ctx context.Context, host component.Host) (storage.Client, error) {
	ext, ok := host.GetExtensions()[*c.cfg.StorageID]
	if !ok {
		return nil, fmt.Errorf("storage extension %q not found", c.cfg.StorageID)
	}
	se, ok := ext.(storage.Extension)
	if !ok {
		return nil, fmt.Errorf("extension %q is not a storage extension", c.cfg.StorageID)
	}
	client, err := se.GetClient(ctx, component.KindProcessor, c.componentID, c.signal)
	if err != nil {
		return nil, fmt.Errorf("get storage client: %w", err)
	}
	return client, nil
}

func (c *core) buildQueues(ctx context.Context, client storage.Client) error {
	c.queues = make([]*tierQueue, len(c.tiers))
	for i, t := range c.tiers {
		q := &tierQueue{
			tier:     t.name,
			client:   client,
			logger:   c.logger,
			maxBytes: c.cfg.Buffer.MaxBytes,
			maxItems: c.cfg.Buffer.MaxItems,
			overflow: c.cfg.Buffer.OverflowPolicy,
		}
		if err := q.load(ctx); err != nil {
			return fmt.Errorf("load tier %q: %w", t.name, err)
		}
		c.queues[i] = q
	}
	return nil
}

// minLevelFor returns the minimum posture level required to egress the tier at idx.
func (c *core) minLevelFor(idx int) posture.Level {
	return c.tiers[idx].minLevel
}

// currentLevel returns the current posture level.
func (c *core) currentLevel() posture.Level {
	return c.provider.Current()
}

// drainLoop releases buffered telemetry whenever the posture permits. It drains
// on startup (to recover backlog at the current level), on every posture
// change, and on a retry ticker (so a transient export failure is retried even
// without a posture change).
func (c *core) drainLoop(ctx context.Context) {
	defer c.wg.Done()

	ch, unsub := c.provider.Subscribe()
	defer unsub()

	ticker := time.NewTicker(drainRetryInterval)
	defer ticker.Stop()

	for {
		c.drainEligible(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ch:
		case <-ticker.C:
		}
	}
}

// drainEligible drains every tier that the current level permits, most
// important (config order) first. It returns early on context cancellation, a
// posture drop, or an export failure (retried later).
func (c *core) drainEligible(ctx context.Context) {
	level := c.currentLevel()
	for idx, t := range c.tiers {
		if t.minLevel > level {
			continue
		}
		q := c.queues[idx]
		for !q.empty() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if c.currentLevel() < t.minLevel {
				break // posture dropped below this tier
			}
			data, seq, ok := q.peek(ctx)
			if !ok {
				break
			}
			err := c.emit(ctx, data)
			c.provider.RecordExportResult(err == nil)
			if err != nil {
				c.logger.Warn("drain emit failed; will retry",
					zap.String("tier", t.name), zap.Error(err))
				return
			}
			if err := q.advance(ctx, seq, len(data)); err != nil {
				c.logger.Error("failed to advance queue after drain",
					zap.String("tier", t.name), zap.Error(err))
				return
			}
			c.rateLimit(ctx, len(data))
		}
	}
}

// rateLimit pauses between drained batches per the drain config (interval +
// optional jitter + optional byte-rate throttle).
func (c *core) rateLimit(ctx context.Context, size int) {
	dur := c.cfg.Drain.Interval
	if dur <= 0 {
		dur = defaultDrainInterval
	}
	if j := c.cfg.Drain.JitterFraction; j > 0 {
		dur = time.Duration(float64(dur) * (1 + (c.rnd.Float64()*2-1)*j))
	}
	if c.cfg.Drain.MaxBytesPerSec > 0 {
		dur += time.Duration(float64(size) / float64(c.cfg.Drain.MaxBytesPerSec) * float64(time.Second))
	}
	if dur <= 0 {
		return
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
