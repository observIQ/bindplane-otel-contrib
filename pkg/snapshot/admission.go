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

package snapshot

import (
	"sync/atomic"
	"time"
)

// DefaultRefreshInterval is the window over which a buffer admits at most its
// ideal size worth of items. A snapshot is a debug view of recent telemetry;
// turning it over more than once a second buys nothing a person can see,
// while admitting every batch on a busy pipeline costs a bounded copy per
// batch.
const DefaultRefreshInterval = time.Second

// admission bounds the copy work a buffer does per interval: about budget
// items (the buffer's ideal size) are admitted per interval, however the
// pipeline batches them. Small batches turn the buffer over as fast as large
// ones, and a large batch is admitted about once per interval. The rejected
// path is lock-free and never reads the payload: one atomic load, one clock
// read, one more load. An interval of zero admits everything.
type admission struct {
	interval time.Duration
	budget   int64
	// windowNs is the start of the current window in unix nanoseconds.
	windowNs atomic.Int64
	// used is the number of items admitted in the current window.
	used atomic.Int64
}

// init sets the interval and budget and opens the first window now.
func (a *admission) init(interval time.Duration, budget int) {
	a.interval = interval
	a.budget = int64(budget)
	a.windowNs.Store(time.Now().UnixNano())
	a.used.Store(0)
}

// exhausted reports whether the current window's budget is spent. Callers
// check it before reading the payload so rejected payloads cost O(1). When the
// interval has elapsed it opens a new window and resets the budget.
func (a *admission) exhausted() bool {
	if a.interval <= 0 || a.used.Load() < a.budget {
		return false
	}
	// time.Now costs ~30ns per rejected batch; a ticker-armed atomic flag
	// would make it ~1ns if a profile ever shows it.
	now := time.Now().UnixNano()
	start := a.windowNs.Load()
	if now-start < int64(a.interval) {
		return true
	}
	// Exactly one caller rolls the window over; the rest are rejected this
	// once and admitted on their next payload.
	if !a.windowNs.CompareAndSwap(start, now) {
		return true
	}
	a.used.Store(0)
	return false
}

// charge records n items admitted in the current window.
func (a *admission) charge(n int) {
	a.used.Add(int64(n))
}

// Option configures a buffer at construction.
type Option func(*admission)

// WithRefreshInterval overrides DefaultRefreshInterval for a buffer. Zero
// admits every payload, restoring the pre-rate-limit behavior.
func WithRefreshInterval(d time.Duration) Option {
	return func(a *admission) { a.interval = d }
}
