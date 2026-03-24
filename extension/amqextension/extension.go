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

package amqextension

import (
	"context"
	"sync"
	"time"

	filter "github.com/observiq/bindplane-otel-contrib/internal/amqfilter"
	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

type amqExtension struct {
	logger    *zap.Logger
	cfg       *Config
	filterSet *filter.FilterSet
	mu        sync.RWMutex

	doneChan chan struct{}
	wg       sync.WaitGroup
}

func newAMQExtension(logger *zap.Logger, cfg *Config) *amqExtension {
	return &amqExtension{
		logger:   logger,
		cfg:      cfg,
		doneChan: make(chan struct{}),
	}
}

func (e *amqExtension) Start(_ context.Context, _ component.Host) error {
	if err := e.initFilters(); err != nil {
		return err
	}

	if e.cfg.ResetInterval > 0 {
		e.wg.Add(1)
		go e.resetLoop()
	}

	return nil
}

func (e *amqExtension) initFilters() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.filterSet = filter.NewFilterSet()

	for _, fc := range e.cfg.Filters {
		opts := fc.ToFilterConfig()
		if opts == nil {
			continue
		}

		f, err := e.filterSet.AddFilterFromConfig(fc.Name, opts)
		if err != nil {
			return err
		}

		e.logger.Info("AMQ filter initialized",
			zap.String("name", fc.Name),
			zap.String("kind", string(fc.FilterKind())),
			zap.Uint("capacity_bits", f.Cap()),
		)
	}

	return nil
}

func (e *amqExtension) resetLoop() {
	defer e.wg.Done()

	t := time.NewTicker(e.cfg.ResetInterval)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			e.logger.Info("Resetting all AMQ filters")
			if err := e.initFilters(); err != nil {
				e.logger.Error("Failed to reset filters", zap.Error(err))
			}
		case <-e.doneChan:
			return
		}
	}
}

func (e *amqExtension) Shutdown(ctx context.Context) error {
	close(e.doneChan)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.wg.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// Filter returns the named filter, or nil if not found.
func (e *amqExtension) Filter(name string) filter.Filter {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.filterSet.Filter(name)
}

// Add adds a value to the named filter.
func (e *amqExtension) Add(name string, value []byte) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if f := e.filterSet.Filter(name); f != nil {
		f.Add(value)
	}
}

// AddString adds a string value to the named filter.
func (e *amqExtension) AddString(name string, s string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if f := e.filterSet.Filter(name); f != nil {
		f.AddString(s)
	}
}

// MayContain returns false if the value is definitely not in the named filter,
// true if it may be present (with possible false positives).
// Returns false if the filter does not exist.
func (e *amqExtension) MayContain(name string, value []byte) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if f := e.filterSet.Filter(name); f != nil {
		return f.MayContain(value)
	}
	return false
}

// MayContainString returns false if the string is definitely not in the named filter,
// true if it may be present (with possible false positives).
// Returns false if the filter does not exist.
func (e *amqExtension) MayContainString(name string, s string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if f := e.filterSet.Filter(name); f != nil {
		return f.MayContainString(s)
	}
	return false
}
