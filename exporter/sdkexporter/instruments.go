// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdkexporter

import (
	"sync"

	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

type scopeKey struct {
	name    string
	version string
}

type instrumentKind int

const (
	kindCounter instrumentKind = iota
	kindUpDownCounter
	kindGauge
)

type instrumentKey struct {
	scope   scopeKey
	name    string
	unit    string
	kind    instrumentKind
	isFloat bool
}

// instrumentCache memoizes Meters and synchronous instruments.
type instrumentCache struct {
	mu     sync.RWMutex
	mp     metric.MeterProvider
	meters map[scopeKey]metric.Meter
	instrs map[instrumentKey]any
	logger *zap.Logger
}

func newInstrumentCache(mp metric.MeterProvider, logger *zap.Logger) *instrumentCache {
	return &instrumentCache{
		mp:     mp,
		meters: make(map[scopeKey]metric.Meter),
		instrs: make(map[instrumentKey]any),
		logger: logger,
	}
}

func (c *instrumentCache) meter(name, version, schemaURL string) metric.Meter {
	sk := scopeKey{name: name, version: version}
	c.mu.RLock()
	if m, ok := c.meters[sk]; ok {
		c.mu.RUnlock()
		return m
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.meters[sk]; ok {
		return m
	}
	opts := make([]metric.MeterOption, 0, 2)
	if version != "" {
		opts = append(opts, metric.WithInstrumentationVersion(version))
	}
	if schemaURL != "" {
		opts = append(opts, metric.WithSchemaURL(schemaURL))
	}
	m := c.mp.Meter(name, opts...)
	c.meters[sk] = m
	return m
}

// getOrCreateSync retrieves a cached synchronous instrument or creates one via
// factory under the write lock with double-check.
func getOrCreateSync[T any](c *instrumentCache, key instrumentKey, factory func() (T, error)) (T, error) {
	c.mu.RLock()
	if v, ok := c.instrs[key]; ok {
		c.mu.RUnlock()
		return v.(T), nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.instrs[key]; ok {
		return v.(T), nil
	}
	inst, err := factory()
	if err != nil {
		var zero T
		return zero, err
	}
	c.instrs[key] = inst
	return inst, nil
}

func (c *instrumentCache) int64Counter(m metric.Meter, sk scopeKey, name, unit, desc string) (metric.Int64Counter, error) {
	key := instrumentKey{scope: sk, name: name, unit: unit, kind: kindCounter, isFloat: false}
	return getOrCreateSync(c, key, func() (metric.Int64Counter, error) {
		return m.Int64Counter(name, metric.WithUnit(unit), metric.WithDescription(desc))
	})
}

func (c *instrumentCache) float64Counter(m metric.Meter, sk scopeKey, name, unit, desc string) (metric.Float64Counter, error) {
	key := instrumentKey{scope: sk, name: name, unit: unit, kind: kindCounter, isFloat: true}
	return getOrCreateSync(c, key, func() (metric.Float64Counter, error) {
		return m.Float64Counter(name, metric.WithUnit(unit), metric.WithDescription(desc))
	})
}

func (c *instrumentCache) int64UpDownCounter(m metric.Meter, sk scopeKey, name, unit, desc string) (metric.Int64UpDownCounter, error) {
	key := instrumentKey{scope: sk, name: name, unit: unit, kind: kindUpDownCounter, isFloat: false}
	return getOrCreateSync(c, key, func() (metric.Int64UpDownCounter, error) {
		return m.Int64UpDownCounter(name, metric.WithUnit(unit), metric.WithDescription(desc))
	})
}

func (c *instrumentCache) float64UpDownCounter(m metric.Meter, sk scopeKey, name, unit, desc string) (metric.Float64UpDownCounter, error) {
	key := instrumentKey{scope: sk, name: name, unit: unit, kind: kindUpDownCounter, isFloat: true}
	return getOrCreateSync(c, key, func() (metric.Float64UpDownCounter, error) {
		return m.Float64UpDownCounter(name, metric.WithUnit(unit), metric.WithDescription(desc))
	})
}

func (c *instrumentCache) int64Gauge(m metric.Meter, sk scopeKey, name, unit, desc string) (metric.Int64Gauge, error) {
	key := instrumentKey{scope: sk, name: name, unit: unit, kind: kindGauge, isFloat: false}
	return getOrCreateSync(c, key, func() (metric.Int64Gauge, error) {
		return m.Int64Gauge(name, metric.WithUnit(unit), metric.WithDescription(desc))
	})
}

func (c *instrumentCache) float64Gauge(m metric.Meter, sk scopeKey, name, unit, desc string) (metric.Float64Gauge, error) {
	key := instrumentKey{scope: sk, name: name, unit: unit, kind: kindGauge, isFloat: true}
	return getOrCreateSync(c, key, func() (metric.Float64Gauge, error) {
		return m.Float64Gauge(name, metric.WithUnit(unit), metric.WithDescription(desc))
	})
}
