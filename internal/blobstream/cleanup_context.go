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

package blobstream

import (
	"context"
	"errors"
	"time"
)

// cleanupTimeout bounds each wind-down call, so a shutdown cannot block forever.
const cleanupTimeout = 30 * time.Second

// IsCancellation reports a cancelled context or an expired deadline. A config push
// or a shutdown causes it. It is routine, and never a DLQ condition.
func IsCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// CleanupContext detaches ctx from its cancellation and applies cleanupTimeout. The
// checkpoint, ack, and nack then run after the cancellation that stopped processing.
func CleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

// DrainContext returns ctx while it is live, and a CleanupContext after cancellation.
// Records already read are then delivered and checkpointed, not re-read.
func DrainContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return context.WithCancel(ctx)
	}
	return CleanupContext(ctx)
}
