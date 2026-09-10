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
	"sync/atomic"
	"time"

	"github.com/jonboulle/clockwork"
	"go.uber.org/zap"
)

// LookupSource is an interface for different lookup data sources.
type LookupSource interface {
	// Lookup returns a map of attributes for the given key. The context must
	// be honored so callers can bound or cancel slow/blocked lookups.
	Lookup(ctx context.Context, key string) (map[string]string, error)
	// Load initializes or refreshes the lookup source.
	Load() error
	// Close cleans up resources used by the lookup source.
	Close() error
}

// errSourceUnavailable is returned without contacting the backend while a
// network source is known to be down and its retry window has not elapsed.
var errSourceUnavailable = errors.New("lookup source unavailable")

// sourceRetryInterval is how long a network source stays marked down after a
// failure before one lookup is let through to probe it. It bounds the cost of
// an outage to one failed dial (at most lookup_timeout) per interval instead
// of one per record. Fixed rather than configurable until a deployment needs
// a different value.
const sourceRetryInterval = 5 * time.Second

// availability is the outage state of a network source, shared by redis and
// api. It logs one Warn when the source stops answering and one Info when it
// answers again, and while down it lets a single probe through per retry
// interval so records do not each pay for a failed dial. Lock-free because
// Lookup runs on every pipeline goroutine.
type availability struct {
	logger  *zap.Logger
	name    string
	clock   clockwork.Clock
	down    atomic.Bool
	retryAt atomic.Int64 // unix nanos; meaningful only while down
}

func newAvailability(logger *zap.Logger, name string) *availability {
	return &availability{logger: logger, name: name, clock: clockwork.NewRealClock()}
}

// skip reports whether a lookup should fail immediately without a network
// call. Once the retry window has elapsed exactly one caller wins the swap and
// probes the backend; concurrent callers keep skipping until it reports back.
func (a *availability) skip() bool {
	if !a.down.Load() {
		return false
	}
	now := a.clock.Now().UnixNano()
	retryAt := a.retryAt.Load()
	if now < retryAt {
		return true
	}
	return !a.retryAt.CompareAndSwap(retryAt, now+int64(sourceRetryInterval))
}

// markDown records a failed probe. retryAt is written before down so a
// concurrent skip that observes down also observes a fresh window.
func (a *availability) markDown(err error) {
	a.retryAt.Store(a.clock.Now().Add(sourceRetryInterval).UnixNano())
	if !a.down.Swap(true) {
		a.logger.Warn(a.name+" lookup source unavailable; records pass through un-enriched until it recovers", zap.Error(err))
	}
}

// startDown records an outage seen at startup without logging; the caller has
// already reported it with startup-specific wording.
func (a *availability) startDown() {
	a.retryAt.Store(a.clock.Now().Add(sourceRetryInterval).UnixNano())
	a.down.Store(true)
}

func (a *availability) markUp() {
	if a.down.Swap(false) {
		a.logger.Info(a.name + " lookup source reachable again; enrichment resumed")
	}
}
