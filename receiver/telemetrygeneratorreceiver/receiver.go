// Copyright observIQ, Inc.
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

package telemetrygeneratorreceiver //import "github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver"

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/observiq/blitz/embed"
	"github.com/observiq/blitz/generator/filegen/embeddedlibrary"
	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

// telemetryProducer is an interface for producing telemetry used by the specific receivers
type telemetryProducer interface {
	// produce should generate telemetry from each generator, update the timestamps to now, and send it to the next consumer
	produce() error
}

type telemetryGeneratorReceiver struct {
	logger     *zap.Logger
	cfg        *Config
	doneChan   chan struct{}
	ctx        context.Context
	cancelFunc context.CancelCauseFunc
	producer   telemetryProducer

	// blitzRunner is non-nil when the signal-type receiver has any
	// Type: "blitz" generator entries; populated by the signal-type
	// constructor (e.g. logsGeneratorReceiver.buildBlitzRunner). The
	// field lives on the base type so Start/Shutdown orchestration is
	// signal-agnostic and v0.18.0's metrics/traces blitz Producers
	// can drop in without re-implementing lifecycle wiring on each
	// signal-type receiver.
	blitzRunner embed.Runner

	// hasTickerGenerators is true when the signal-type receiver has
	// non-blitz generators driven by the payloads_per_second ticker.
	// For blitz-only configs (and any config with zero pull-style
	// generators) the ticker is short-circuited — blitz modules emit
	// on their own schedule and an unused ticker would just wake the
	// downstream pipeline with empty payloads each interval.
	hasTickerGenerators bool
}

// newTelemetryGeneratorReceiver creates a new telemetry generator receiver
func newTelemetryGeneratorReceiver(_ context.Context, logger *zap.Logger, cfg *Config, tp telemetryProducer) telemetryGeneratorReceiver {
	return telemetryGeneratorReceiver{
		logger:   logger,
		cfg:      cfg,
		doneChan: make(chan struct{}),
		producer: tp,
	}
}

// Shutdown stops the blitz runner (if any), then drains the ticker
// goroutine via the receiver's cancellation context. Blitz is
// stopped before the ticker drain begins so in-flight blitz module
// emissions get a chance to flush downstream before the receiver's
// context is cancelled and the ticker goroutine exits.
func (r *telemetryGeneratorReceiver) Shutdown(ctx context.Context) error {
	var blitzErr error
	if r.blitzRunner != nil {
		blitzErr = r.blitzRunner.Stop(ctx)
	}
	if r.cancelFunc == nil {
		return blitzErr
	}
	r.cancelFunc(errors.New("shutdown"))
	var drainErr error
	select {
	case <-ctx.Done():
		drainErr = ctx.Err()
	case <-r.doneChan:
	}
	if blitzErr != nil {
		return blitzErr
	}
	return drainErr
}

// Start orchestrates receiver startup across blitz (when configured)
// and the ticker-driven generator loop (when any non-blitz generators
// exist to drive it).
//
// Order of operations, fixed:
//  1. Assert the embed library is built in when blitz is configured,
//     so a missing -tags embed_library is reported with a clear
//     pointer rather than as a downstream filegen error.
//  2. Initialize the cancellation context shared by both subsystems
//     so Shutdown can unwind them with one call.
//  3. Start blitz first. If its Start fails, return the error before
//     spawning any ticker goroutine — the OTel-contract failure path
//     (Start error ⇒ Shutdown not called) then never leaves a
//     dangling ticker.
//  4. Spawn the ticker only when there are non-blitz generators to
//     drive it. For blitz-only or zero-generator configs the ticker
//     is short-circuited entirely.
func (r *telemetryGeneratorReceiver) Start(_ context.Context, _ component.Host) error {
	if r.blitzRunner != nil {
		if err := assertEmbeddedLibraryAvailable(embeddedlibrary.FS()); err != nil {
			return err
		}
	}

	r.ctx, r.cancelFunc = context.WithCancelCause(context.Background())

	if r.blitzRunner != nil {
		if err := r.blitzRunner.Start(r.ctx, embed.Host{Logger: r.logger}); err != nil {
			r.cancelFunc(errors.New("blitz runner Start failed"))
			close(r.doneChan)
			return fmt.Errorf("start blitz runner: %w", err)
		}
	}

	if r.hasTickerGenerators {
		r.startTicker()
	} else {
		// No ticker goroutine will close doneChan; close it eagerly
		// so Shutdown's drain path passes through immediately.
		close(r.doneChan)
	}
	return nil
}

// startTicker spawns the payloads_per_second producer loop. r.ctx
// and r.cancelFunc must already be initialized — Start does this
// before reaching here so blitz and the ticker share one
// cancellation handle.
func (r *telemetryGeneratorReceiver) startTicker() {
	go func() {
		defer close(r.doneChan)

		ticker := time.NewTicker(time.Second / time.Duration(r.cfg.PayloadsPerSecond))
		defer ticker.Stop()

		if err := r.producer.produce(); err != nil {
			r.logger.Error("Error generating telemetry", zap.Error(err))
		}
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				if err := r.producer.produce(); err != nil {
					r.logger.Error("Error generating telemetry", zap.Error(err))
				}
			}
		}
	}()
}
