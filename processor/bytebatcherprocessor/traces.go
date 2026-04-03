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

package bytebatcherprocessor

import (
	"context"
	"time"

	"github.com/observiq/bindplane-otel-contrib/processor/bytebatcherprocessor/internal/metadata"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// batchTraces implements batch[ptrace.Traces].
type batchTraces struct {
	items                    []ptrace.Traces
	next                     consumer.Traces
	telemetry                *metadata.TelemetryBuilder
	telemetryRefreshInterval time.Duration
	lastFlush                time.Time
}

// newBatchTraces creates a new traces batch.
func newBatchTraces(next consumer.Traces, telemetry *metadata.TelemetryBuilder) batch[ptrace.Traces] {
	return &batchTraces{
		items:                    make([]ptrace.Traces, 0),
		next:                     next,
		telemetry:                telemetry,
		telemetryRefreshInterval: 15 * time.Second,
		lastFlush:                time.Now(),
	}
}

// add appends a trace entry to the batch.
func (b *batchTraces) add(item ptrace.Traces) {
	b.items = append(b.items, item)
}

// sizeBytes returns the size of a single trace entry in bytes.
func (b *batchTraces) sizeBytes(item ptrace.Traces) int {
	sizer := ptrace.ProtoMarshaler{}
	return sizer.TracesSize(item)
}

// flush sends all accumulated traces to the consumer.
func (b *batchTraces) flush(ctx context.Context) error {
	if len(b.items) == 0 {
		return nil
	}

	// Merge all accumulated traces into a single ptrace.Traces
	merged := ptrace.NewTraces()
	for _, traces := range b.items {
		traces.ResourceSpans().MoveAndAppendTo(merged.ResourceSpans())
	}

	// Record the batch size metric
	if b.telemetry != nil && time.Since(b.lastFlush) >= b.telemetryRefreshInterval {
		now := time.Now()
		sizer := ptrace.ProtoMarshaler{}
		size := int64(sizer.TracesSize(merged))
		b.telemetry.ProcessorBatchSendSizeTraces.Record(ctx, size)
		b.lastFlush = now
	}

	// Send to the next consumer
	return b.next.ConsumeTraces(ctx, merged)
}

// len returns the number of items in the batch.
func (b *batchTraces) len() int {
	return len(b.items)
}

// reset clears the batch for reuse.
func (b *batchTraces) reset() {
	b.items = b.items[:0]
}
