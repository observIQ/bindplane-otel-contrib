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
	"go.opentelemetry.io/collector/pdata/pmetric"
)

// batchMetrics implements batch[pmetric.Metrics].
type batchMetrics struct {
	items                    []pmetric.Metrics
	next                     consumer.Metrics
	telemetry                *metadata.TelemetryBuilder
	telemetryRefreshInterval time.Duration
	lastFlush                time.Time
}

// newBatchMetrics creates a new metrics batch.
func newBatchMetrics(next consumer.Metrics, telemetry *metadata.TelemetryBuilder) batch[pmetric.Metrics] {
	return &batchMetrics{
		items:                    make([]pmetric.Metrics, 0),
		next:                     next,
		telemetry:                telemetry,
		telemetryRefreshInterval: 15 * time.Second,
		lastFlush:                time.Now(),
	}
}

// add appends a metric entry to the batch.
func (b *batchMetrics) add(item pmetric.Metrics) {
	b.items = append(b.items, item)
}

// sizeBytes returns the size of a single metric entry in bytes.
func (b *batchMetrics) sizeBytes(item pmetric.Metrics) int {
	sizer := pmetric.ProtoMarshaler{}
	return sizer.MetricsSize(item)
}

// flush sends all accumulated metrics to the consumer.
func (b *batchMetrics) flush(ctx context.Context) error {
	if len(b.items) == 0 {
		return nil
	}

	merged := pmetric.NewMetrics()
	for _, metrics := range b.items {
		metrics.ResourceMetrics().MoveAndAppendTo(merged.ResourceMetrics())
	}

	// Record the batch size metric only if the last flush was more than the telemetry refresh interval ago
	if b.telemetry != nil && time.Since(b.lastFlush) >= b.telemetryRefreshInterval {
		now := time.Now()
		sizer := pmetric.ProtoMarshaler{}
		size := int64(sizer.MetricsSize(merged))
		b.telemetry.ProcessorBatchSendSizeMetrics.Record(ctx, size)
		b.lastFlush = now
	}

	// Send to the next consumer (single attempt; failures are logged and the batch is dropped upstream).
	return b.next.ConsumeMetrics(ctx, merged)
}

// len returns the number of items in the batch.
func (b *batchMetrics) len() int {
	return len(b.items)
}

// reset clears the batch for reuse.
func (b *batchMetrics) reset() {
	b.items = b.items[:0]
}
