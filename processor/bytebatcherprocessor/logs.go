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
	"go.opentelemetry.io/collector/pdata/plog"
)

// batchLogs implements batch[plog.Logs].
type batchLogs struct {
	items                    []plog.Logs
	next                     consumer.Logs
	telemetry                *metadata.TelemetryBuilder
	telemetryRefreshInterval time.Duration
	lastFlush                time.Time
}

// newBatchLogs creates a new logs batch.
func newBatchLogs(next consumer.Logs, telemetry *metadata.TelemetryBuilder) batch[plog.Logs] {
	return &batchLogs{
		items:     make([]plog.Logs, 0),
		next:      next,
		telemetry: telemetry,
	}
}

// add appends a log entry to the batch.
func (b *batchLogs) add(item plog.Logs) {
	b.items = append(b.items, item)
}

// sizeBytes returns the size of a single log entry in bytes.
func (b *batchLogs) sizeBytes(item plog.Logs) int {
	sizer := plog.ProtoMarshaler{}
	return sizer.LogsSize(item)
}

// flush sends all accumulated logs to the consumer.
func (b *batchLogs) flush(ctx context.Context) error {
	if len(b.items) == 0 {
		return nil
	}

	// Merge all accumulated logs into a single plog.Logs
	merged := plog.NewLogs()
	for _, logs := range b.items {
		logs.ResourceLogs().MoveAndAppendTo(merged.ResourceLogs())
	}

	// Record the batch size metric
	if b.telemetry != nil && time.Since(b.lastFlush) >= b.telemetryRefreshInterval {
		now := time.Now()
		sizer := plog.ProtoMarshaler{}
		size := int64(sizer.LogsSize(merged))
		b.telemetry.ProcessorBatchSendSizeLogs.Record(ctx, size)
		b.lastFlush = now
	}

	// Send to the next consumer (single attempt; failures are logged and the batch is dropped upstream).
	return b.next.ConsumeLogs(ctx, merged)
}

// len returns the number of items in the batch.
func (b *batchLogs) len() int {
	return len(b.items)
}

// reset clears the batch for reuse.
func (b *batchLogs) reset() {
	b.items = b.items[:0]
}
