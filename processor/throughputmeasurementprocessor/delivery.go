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

package throughputmeasurementprocessor

import (
	"context"
	"sync/atomic"
)

// deliveryTracker records that a downstream path accepted a payload.
//
// A fanout consumer joins the errors of its branches. Thus when one branch
// fails and one succeeds, the processor above the fanout gets an error. Each
// processor puts its own tracker in the context before it forwards a payload.
// A processor below it marks that tracker when its own forward succeeds. The
// processor above then knows that at least one branch accepted the payload.
// This works because the context and the forward are synchronous.
type deliveryTracker struct {
	delivered atomic.Bool
}

type deliveryTrackerKey struct{}

// trackerFromContext returns the nearest tracker in ctx, or nil.
func trackerFromContext(ctx context.Context) *deliveryTracker {
	t, _ := ctx.Value(deliveryTrackerKey{}).(*deliveryTracker)
	return t
}

// contextWithTracker returns a copy of ctx that carries t.
func contextWithTracker(ctx context.Context, t *deliveryTracker) context.Context {
	return context.WithValue(ctx, deliveryTrackerKey{}, t)
}

// markDelivered records that a downstream path accepted the payload.
// It is safe to call on a nil tracker.
func (t *deliveryTracker) markDelivered() {
	if t != nil {
		t.delivered.Store(true)
	}
}

// wasDelivered reports whether a downstream path accepted the payload.
// It is safe to call on a nil tracker.
func (t *deliveryTracker) wasDelivered() bool {
	return t != nil && t.delivered.Load()
}
