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
	"sync"
	"sync/atomic"
	"time"
)

// DefaultRefreshInterval is the window over which a buffer admits at most its
// ideal size worth of items. A snapshot is a debug view of recent telemetry;
// turning it over more than once a second buys nothing a person can see,
// while admitting every batch on a busy pipeline costs a bounded copy per
// batch.
const DefaultRefreshInterval = time.Second

// On-demand mode. A pipeline that can fill a whole buffer in a fraction of a
// second does not need one kept warm: a snapshot request can collect its own
// records just in time. Such buffers stop collecting between requests, so the
// steady-state cost is one atomic load per batch and no retained telemetry.
const (
	// highThroughputMultiple is how many buffers' worth of records per
	// second a pipeline must deliver to count as high-throughput. Ten means
	// a request can expect to collect a full buffer in about 100 ms.
	highThroughputMultiple = 10
	// minBatchesPerSecond is the batch rate a high-throughput pipeline must
	// also sustain, so a request never waits long for the next batch.
	minBatchesPerSecond = 10
	// fastWindowsToOnDemand is how many consecutive high-throughput windows
	// switch a buffer to on-demand mode. Three means a burst does not flip
	// the mode.
	fastWindowsToOnDemand = 3
	// fillWait bounds how long a request in on-demand mode waits for the
	// store to fill before answering with whatever has arrived. It matches
	// the interval at which Bindplane polls for snapshots, so a slow pipeline
	// costs at most one poll.
	fillWait = time.Second
	// heartbeatInterval is how often an idle on-demand buffer admits one
	// payload anyway, so its store stays a recent picture of the stream and a
	// request after the pipeline goes quiet still shows the last records that
	// flowed. One bounded copy every ten seconds is below any measurable cost.
	heartbeatInterval = 10 * time.Second
	// heartbeatSampleMask makes the idle hot path read the clock only once
	// every 64 payloads; the other 63 cost one atomic add.
	heartbeatSampleMask = 63
)

// epoch anchors the monotonic clock all window arithmetic uses. time.Since
// on a time.Time that carries a monotonic reading is immune to wall-clock
// steps, which would otherwise freeze a window until the clock caught up.
var epoch = time.Now()

// monoNow returns nanoseconds since epoch on the monotonic clock.
func monoNow() int64 {
	return int64(time.Since(epoch))
}

// admission decides which payloads Add looks at.
//
// Continuous mode: about budget items (the buffer's ideal size) are admitted
// per interval, however the pipeline batches them. Small batches turn the
// buffer over as fast as large ones, and a large batch is admitted about once
// per interval.
//
// On-demand mode: the buffer stops collecting and keeps its store as the last
// known snapshot, refreshed by one payload per heartbeatInterval. A request
// arms the buffer, waits until a full store's worth of fresh records has
// arrived or fillWait has passed, answers with the store (fresh records if the
// pipeline is flowing, the last known ones if it went quiet), and disarms.
// Each time a window rolls over, the records and batches offered give the
// pipeline's rate; after fastWindowsToOnDemand consecutive windows above
// highThroughputMultiple buffers per second (and minBatchesPerSecond batches)
// the buffer switches to on-demand mode. It leaves the first time a request's
// wait times out.
//
// The rejected path reads only the payload's record count (a walk of its
// resource and scope groups, no allocation) and is lock-free: one atomic
// load (collecting), two atomic adds, one atomic load, one clock read and one
// more load. An interval of zero admits everything and disables on-demand
// detection.
type admission struct {
	interval time.Duration
	budget   int64
	// windowNs is the start of the current window in monotonic nanoseconds
	// (see monoNow).
	windowNs atomic.Int64
	// used is the number of items admitted in the current window.
	used atomic.Int64
	// batches and offered count every payload offered in the current window
	// and the records it carried, admitted or not. They give the pipeline's
	// batch and record rate at rollover.
	batches atomic.Int64
	offered atomic.Int64
	// collecting is the hot-path gate. It is false only in on-demand mode
	// between requests. armed is true while a request is in flight on an
	// on-demand buffer, so Add knows to report a full store.
	collecting atomic.Bool
	armed      atomic.Bool
	// idleBatches counts payloads offered while idle in on-demand mode and
	// lastHeartbeatNs is when one was last admitted as a heartbeat.
	idleBatches     atomic.Int64
	lastHeartbeatNs atomic.Int64

	// Slow-path state, touched only when a window rolls over or a request
	// begins or ends.
	mu          sync.Mutex
	onDemand    bool
	fastWindows int
	// waiters is the number of in-flight requests keeping an on-demand
	// buffer armed. filled is closed once the store reaches the ideal size
	// while armed.
	waiters      int
	filled       chan struct{}
	filledClosed bool
	// Detection and wait parameters; package constants unless a test
	// overrides them.
	highThroughputMultiple int
	minBatchesPerSecond    int
	fastWindowsToOnDemand  int
	fillWait               time.Duration
	heartbeatInterval      time.Duration
}

// decision is what Add should do with a payload.
type decision uint8

const (
	// reject ignores the payload without reading it.
	reject decision = iota
	// admit reads and copies the payload.
	admit
	// enterOnDemand ignores the payload and stops collecting: the pipeline
	// is fast enough for just-in-time collection. The store is kept as the
	// last known snapshot.
	enterOnDemand
)

// init sets the interval and budget and opens the first window now.
func (a *admission) init(interval time.Duration, budget int) {
	a.interval = interval
	a.budget = int64(budget)
	a.windowNs.Store(monoNow())
	a.used.Store(0)
	a.collecting.Store(true)
	a.highThroughputMultiple = highThroughputMultiple
	a.minBatchesPerSecond = minBatchesPerSecond
	a.fastWindowsToOnDemand = fastWindowsToOnDemand
	a.fillWait = fillWait
	a.heartbeatInterval = heartbeatInterval
}

// heartbeat is the hot path of an idle on-demand buffer. It admits one payload
// per heartbeatInterval so the store never falls far behind the stream while
// nobody is asking. The clock is read once every heartbeatSampleMask+1
// payloads; the rest cost one atomic add.
func (a *admission) heartbeat() bool {
	if a.idleBatches.Add(1)&heartbeatSampleMask != 0 {
		return false
	}
	now := monoNow()
	last := a.lastHeartbeatNs.Load()
	if now-last < int64(a.heartbeatInterval) {
		return false
	}
	return a.lastHeartbeatNs.CompareAndSwap(last, now)
}

// decide reports whether Add should copy a payload of records items. Rejected
// payloads cost O(1) beyond the count. When the interval has elapsed it opens a
// new window, resets the budget and samples the closed window's throughput.
func (a *admission) decide(records int) decision {
	if a.interval <= 0 {
		return admit
	}
	a.batches.Add(1)
	a.offered.Add(int64(records))
	if a.used.Load() < a.budget {
		return admit
	}
	// The clock read costs ~30ns per rejected batch; a ticker-armed atomic
	// flag would make it ~1ns if a profile ever shows it.
	now := monoNow()
	start := a.windowNs.Load()
	if now-start < int64(a.interval) {
		return reject
	}
	// Exactly one caller rolls the window over; the rest are rejected this
	// once and admitted on their next payload.
	if !a.windowNs.CompareAndSwap(start, now) {
		return reject
	}
	batches := a.batches.Swap(0)
	offered := a.offered.Swap(0)
	a.used.Store(0)
	if a.sampleWindow(batches, offered, now-start) {
		return enterOnDemand
	}
	return admit
}

// charge records kept items admitted in the current window.
func (a *admission) charge(kept int) {
	a.used.Add(int64(kept))
}

// sampleWindow takes the record and batch rate of a window that just closed
// and tracks consecutive high-throughput windows. It reports true when the
// buffer should switch to on-demand mode.
func (a *admission) sampleWindow(batches, offered, durationNs int64) bool {
	if a.budget <= 0 || durationNs <= 0 {
		return false
	}
	seconds := float64(durationNs) / float64(time.Second)
	recordsPerSecond := float64(offered) / seconds
	batchesPerSecond := float64(batches) / seconds

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.onDemand {
		return false
	}
	fast := recordsPerSecond >= float64(a.highThroughputMultiple)*float64(a.budget) &&
		batchesPerSecond >= float64(a.minBatchesPerSecond)
	if !fast {
		a.fastWindows = 0
		return false
	}
	a.fastWindows++
	if a.fastWindows < a.fastWindowsToOnDemand {
		return false
	}
	a.onDemand = true
	a.fastWindows = 0
	a.lastHeartbeatNs.Store(monoNow())
	a.collecting.Store(false)
	return true
}

// full tells an armed on-demand buffer that a full store's worth of fresh
// records has arrived, releasing any request waiting on it.
func (a *admission) full() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.filled != nil && !a.filledClosed {
		close(a.filled)
		a.filledClosed = true
	}
}

// beginRequest prepares the buffer for a snapshot request. In continuous mode
// it returns nil and the request proceeds at once. In on-demand mode it arms
// the buffer with a fresh budget and returns a channel that is closed once a
// full store's worth of fresh records has arrived; the request should wait on
// it for up to fillWait. armed is true for the request that armed the buffer,
// which must reset the buffer's fresh-record count.
func (a *admission) beginRequest() (filled <-chan struct{}, armed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.onDemand {
		return nil, false
	}
	a.waiters++
	if a.waiters == 1 {
		// First request of this cycle: a fresh channel and a fresh window so
		// the whole budget is available to fill the store. Later concurrent
		// requests share the channel, which may already be closed.
		a.filled = make(chan struct{})
		a.filledClosed = false
		a.windowNs.Store(monoNow())
		a.used.Store(0)
		a.batches.Store(0)
		a.offered.Store(0)
		a.armed.Store(true)
		a.collecting.Store(true)
		return a.filled, true
	}
	return a.filled, false
}

// endRequest disarms an on-demand buffer once its last in-flight request has
// taken its copy of the store. filledInTime reports whether fresh records
// filled the store before the wait expired; if they did not, the pipeline is
// no longer fast enough for just-in-time collection and the buffer returns to
// continuous mode. The store is kept either way as the last known snapshot.
func (a *admission) endRequest(filledInTime bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.waiters--
	if !filledInTime {
		a.onDemand = false
		a.fastWindows = 0
		a.collecting.Store(true)
	}
	if a.waiters > 0 {
		return
	}
	a.filled = nil
	a.armed.Store(false)
	if a.onDemand {
		a.lastHeartbeatNs.Store(monoNow())
		a.collecting.Store(false)
	}
}

// awaitFill waits for an armed buffer to fill or for the wait to expire and
// reports which happened.
func (a *admission) awaitFill(filled <-chan struct{}) bool {
	timer := time.NewTimer(a.fillWait)
	defer timer.Stop()
	select {
	case <-filled:
		return true
	case <-timer.C:
		return false
	}
}

// Option configures a buffer at construction.
type Option func(*admission)

// WithRefreshInterval overrides DefaultRefreshInterval for a buffer. Zero
// admits every payload and disables on-demand detection, restoring the
// pre-rate-limit behavior.
func WithRefreshInterval(d time.Duration) Option {
	return func(a *admission) { a.interval = d }
}
