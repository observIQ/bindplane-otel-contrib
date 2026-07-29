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
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// LogBuffer is a buffer for plog.Logs. It owns a single bounded store of at
// most idealSize log records: there is no list of payload entries to grow,
// re-count, or pin evicted payloads.
type LogBuffer struct {
	mutex sync.Mutex
	// store holds the retained records, oldest first, capped at idealSize.
	store plog.Logs
	// count is the number of log records in store. It is written while
	// holding mutex and read lock-free by Len.
	count     atomic.Int64
	idealSize int
}

// NewLogBuffer creates a logBuffer with the ideal size set
func NewLogBuffer(idealSize int) *LogBuffer {
	return &LogBuffer{
		store:     plog.NewLogs(),
		idealSize: idealSize,
	}
}

// Len returns the number of log records in the buffer.
// It is lock-free and safe for concurrent use.
func (l *LogBuffer) Len() int {
	return int(l.count.Load())
}

// Reset drops all buffered log records, releasing the retained telemetry.
// It is safe for concurrent use.
func (l *LogBuffer) Reset() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.store = plog.NewLogs()
	l.count.Store(0)
}

// Add copies at most idealSize of the newest log records out of ld into the
// buffer, evicting the oldest buffered records to stay within the ideal
// size. ld is never retained or mutated, so callers may pass pipeline
// payloads directly.
func (l *LogBuffer) Add(ld plog.Logs) {
	logSize := ld.LogRecordCount()
	// Zero-count payloads contribute nothing to a snapshot.
	if logSize == 0 {
		return
	}

	// Copy only what the buffer can retain. This bounds the per-Add copy cost
	// by idealSize regardless of how large the incoming payload is. The copy
	// happens before taking the lock so concurrent Adds do not serialize on
	// the copy work.
	kept := min(logSize, l.idealSize)
	incoming := plog.NewLogs()
	copyLogsTail(ld, incoming, logSize-kept)

	l.mutex.Lock()
	defer l.mutex.Unlock()

	// Append the copied records and evict the oldest overflow, so the store
	// never holds more than idealSize records.
	incoming.ResourceLogs().MoveAndAppendTo(l.store.ResourceLogs())
	total := int(l.count.Load()) + kept
	if over := total - l.idealSize; over > 0 {
		dropOldestLogRecords(l.store, over)
		total = l.idealSize
	}
	l.count.Store(int64(total))
}

// ConstructPayload condenses the buffer and serializes to protobuf. Does not compress the payload to be compatible with both the snapshot reporter and the snapshot processor.
// It ensures that the payload's compressed size is less than the maximum payload size, returning an error if it cannot sample logs within the maximum payload size.
// Samples with decreasing retention (100%, 75%, 50%, 25%, 1%) and returns the first payload that fits, so the common case costs a single marshal.
// Clears the buffer if it cannot sample logs within the maximum payload size. This should allow the next snapshot to have a valid payload size.
func (l *LogBuffer) ConstructPayload(logsMarshaler plog.Marshaler, searchQuery *string, minimumTimestamp *time.Time, maximumPayloadSize int) ([]byte, error) {
	// Copy the buffered records while holding the lock, then release it so
	// filtering, sampling, marshaling, and compression never stall the
	// pipelines feeding Add. The copy is bounded by idealSize.
	payloadCopy := plog.NewLogs()
	l.mutex.Lock()
	l.store.CopyTo(payloadCopy)
	l.mutex.Unlock()

	// Filter the payload
	filteredPayload := filterLogs(payloadCopy, searchQuery, minimumTimestamp)

	// Count the log records in the filtered payload once; sampling identifies
	// records by their flat position in traversal order.
	recordCount := filteredPayload.LogRecordCount()

	var lastError error

	// Walk retention from highest to lowest and return the first payload that
	// fits within the maximum payload size once compressed.
	for _, retentionPercent := range []int{100, 75, 50, 25, 1} {
		// Sample the logs based on the retention percentage
		logsToMarshal := randomSampleLogs(filteredPayload, recordCount, retentionPercent)

		payload, err := logsMarshaler.MarshalLogs(logsToMarshal)
		if err != nil {
			lastError = fmt.Errorf("failed to construct payload: %w", err)
			break
		}

		// The uncompressed size is an upper bound on the compressed size, so
		// a payload already under the limit needs no compression pass at all.
		if len(payload) <= maximumPayloadSize {
			return payload, nil
		}

		// Check the compressed size without retaining the compressed bytes.
		size, err := compressedSize(payload)
		if err != nil {
			lastError = fmt.Errorf("failed to compress payload: %w", err)
			break
		}

		if size <= maximumPayloadSize {
			return payload, nil
		}

		lastError = fmt.Errorf("snapshot buffer is too large to construct payload")
	}

	// Encountered an error or we've tried all retentions and still can't fit the payload
	// so clear the buffer and return the last error seen
	l.Reset()
	return nil, lastError
}

// MetricBuffer is a buffer for pmetric.Metrics. It owns a single bounded
// store of at most idealSize data points: there is no list of payload
// entries to grow, re-count, or pin evicted payloads.
type MetricBuffer struct {
	mutex sync.Mutex
	// store holds the retained data points, oldest first, capped at idealSize.
	store pmetric.Metrics
	// count is the number of data points in store. It is written while
	// holding mutex and read lock-free by Len.
	count     atomic.Int64
	idealSize int
}

// NewMetricBuffer creates a metricBuffer with the ideal size set
func NewMetricBuffer(idealSize int) *MetricBuffer {
	return &MetricBuffer{
		store:     pmetric.NewMetrics(),
		idealSize: idealSize,
	}
}

// Len returns the number of data points in the buffer.
// It is lock-free and safe for concurrent use.
func (l *MetricBuffer) Len() int {
	return int(l.count.Load())
}

// Reset drops all buffered data points, releasing the retained telemetry.
// It is safe for concurrent use.
func (l *MetricBuffer) Reset() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.store = pmetric.NewMetrics()
	l.count.Store(0)
}

// Add copies at most idealSize of the newest data points out of md into the
// buffer, evicting the oldest buffered data points to stay within the ideal
// size. md is never retained or mutated, so callers may pass pipeline
// payloads directly.
func (l *MetricBuffer) Add(md pmetric.Metrics) {
	metricSize := md.DataPointCount()
	// Zero-count payloads contribute nothing to a snapshot.
	if metricSize == 0 {
		return
	}

	// Copy only what the buffer can retain. This bounds the per-Add copy cost
	// by idealSize regardless of how large the incoming payload is. The copy
	// happens before taking the lock so concurrent Adds do not serialize on
	// the copy work.
	kept := min(metricSize, l.idealSize)
	incoming := pmetric.NewMetrics()
	copyMetricsTail(md, incoming, metricSize-kept)

	l.mutex.Lock()
	defer l.mutex.Unlock()

	// Append the copied data points and evict the oldest overflow, so the
	// store never holds more than idealSize data points.
	incoming.ResourceMetrics().MoveAndAppendTo(l.store.ResourceMetrics())
	total := int(l.count.Load()) + kept
	if over := total - l.idealSize; over > 0 {
		dropOldestDataPoints(l.store, over)
		total = l.idealSize
	}
	l.count.Store(int64(total))
}

// ConstructPayload condenses the buffer and serializes to protobuf. Does not compress the payload to be compatible with both the snapshot reporter and the snapshot processor.
// It ensures that the payload's compressed size is less than the maximum payload size, returning an error if it cannot sample metrics within the maximum payload size.
// Samples with decreasing retention (100%, 75%, 50%, 25%, 1%) and returns the first payload that fits, so the common case costs a single marshal.
// Clears the buffer if it cannot sample metrics within the maximum payload size. This should allow the next snapshot to have a valid payload size.
func (l *MetricBuffer) ConstructPayload(metricMarshaler pmetric.Marshaler, searchQuery *string, minimumTimestamp *time.Time, maximumPayloadSize int) ([]byte, error) {
	// Copy the buffered data points while holding the lock, then release it so
	// filtering, sampling, marshaling, and compression never stall the
	// pipelines feeding Add. The copy is bounded by idealSize.
	payloadCopy := pmetric.NewMetrics()
	l.mutex.Lock()
	l.store.CopyTo(payloadCopy)
	l.mutex.Unlock()

	// filter the payload
	filteredPayload := filterMetrics(payloadCopy, searchQuery, minimumTimestamp)

	// Count the data points in the filtered payload once; sampling identifies
	// data points by their flat position in traversal order.
	dataPointCount := filteredPayload.DataPointCount()

	var lastError error

	// Walk retention from highest to lowest and return the first payload that
	// fits within the maximum payload size once compressed.
	for _, retentionPercent := range []int{100, 75, 50, 25, 1} {
		// Sample the metrics based on the retention percentage
		metricsToMarshal := randomSampleMetrics(filteredPayload, dataPointCount, retentionPercent)

		payload, err := metricMarshaler.MarshalMetrics(metricsToMarshal)
		if err != nil {
			lastError = fmt.Errorf("failed to construct payload: %w", err)
			break
		}

		// The uncompressed size is an upper bound on the compressed size, so
		// a payload already under the limit needs no compression pass at all.
		if len(payload) <= maximumPayloadSize {
			return payload, nil
		}

		// Check the compressed size without retaining the compressed bytes.
		size, err := compressedSize(payload)
		if err != nil {
			lastError = fmt.Errorf("failed to compress payload: %w", err)
			break
		}

		if size <= maximumPayloadSize {
			return payload, nil
		}

		lastError = fmt.Errorf("snapshot buffer is too large to construct payload")
	}

	// Encountered an error or we've tried all retentions and still can't fit the payload
	// so clear the buffer and return the last error seen
	l.Reset()
	return nil, lastError
}

// TraceBuffer is a buffer for ptrace.Traces. It owns a single bounded store
// of at most idealSize spans: there is no list of payload entries to grow,
// re-count, or pin evicted payloads.
type TraceBuffer struct {
	mutex sync.Mutex
	// store holds the retained spans, oldest first, capped at idealSize.
	store ptrace.Traces
	// count is the number of spans in store. It is written while holding
	// mutex and read lock-free by Len.
	count     atomic.Int64
	idealSize int
}

// NewTraceBuffer creates a traceBuffer with the ideal size set
func NewTraceBuffer(idealSize int) *TraceBuffer {
	return &TraceBuffer{
		store:     ptrace.NewTraces(),
		idealSize: idealSize,
	}
}

// Len returns the number of spans in the buffer.
// It is lock-free and safe for concurrent use.
func (l *TraceBuffer) Len() int {
	return int(l.count.Load())
}

// Reset drops all buffered spans, releasing the retained telemetry.
// It is safe for concurrent use.
func (l *TraceBuffer) Reset() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.store = ptrace.NewTraces()
	l.count.Store(0)
}

// Add copies at most idealSize of the newest spans out of td into the
// buffer, evicting the oldest buffered spans to stay within the ideal size.
// td is never retained or mutated, so callers may pass pipeline payloads
// directly.
func (l *TraceBuffer) Add(td ptrace.Traces) {
	traceSize := td.SpanCount()
	// Zero-count payloads contribute nothing to a snapshot.
	if traceSize == 0 {
		return
	}

	// Copy only what the buffer can retain. This bounds the per-Add copy cost
	// by idealSize regardless of how large the incoming payload is. The copy
	// happens before taking the lock so concurrent Adds do not serialize on
	// the copy work.
	kept := min(traceSize, l.idealSize)
	incoming := ptrace.NewTraces()
	copyTracesTail(td, incoming, traceSize-kept)

	l.mutex.Lock()
	defer l.mutex.Unlock()

	// Append the copied spans and evict the oldest overflow, so the store
	// never holds more than idealSize spans.
	incoming.ResourceSpans().MoveAndAppendTo(l.store.ResourceSpans())
	total := int(l.count.Load()) + kept
	if over := total - l.idealSize; over > 0 {
		dropOldestSpans(l.store, over)
		total = l.idealSize
	}
	l.count.Store(int64(total))
}

// ConstructPayload condenses the buffer and serializes to protobuf. Does not compress the payload to be compatible with both the snapshot reporter and the snapshot processor.
// It ensures that the payload's compressed size is less than the maximum payload size, returning an error if it cannot sample traces within the maximum payload size.
// Samples with decreasing retention (100%, 75%, 50%, 25%, 1%) and returns the first payload that fits, so the common case costs a single marshal.
// Clears the buffer if it cannot sample traces within the maximum payload size. This should allow the next snapshot to have a valid payload size.
func (l *TraceBuffer) ConstructPayload(traceMarshaler ptrace.Marshaler, searchQuery *string, minimumTimestamp *time.Time, maximumPayloadSize int) ([]byte, error) {
	// Copy the buffered spans while holding the lock, then release it so
	// filtering, sampling, marshaling, and compression never stall the
	// pipelines feeding Add. The copy is bounded by idealSize.
	payloadCopy := ptrace.NewTraces()
	l.mutex.Lock()
	l.store.CopyTo(payloadCopy)
	l.mutex.Unlock()

	// Filter the payload
	filteredPayload := filterTraces(payloadCopy, searchQuery, minimumTimestamp)

	// Count the spans in the filtered payload once; sampling identifies spans
	// by their flat position in traversal order.
	spanCount := filteredPayload.SpanCount()

	var lastError error

	// Walk retention from highest to lowest and return the first payload that
	// fits within the maximum payload size once compressed.
	for _, retentionPercent := range []int{100, 75, 50, 25, 1} {
		// Sample the traces based on the retention percentage
		tracesToMarshal := randomSampleTraces(filteredPayload, spanCount, retentionPercent)

		payload, err := traceMarshaler.MarshalTraces(tracesToMarshal)
		if err != nil {
			lastError = fmt.Errorf("failed to construct payload: %w", err)
			break
		}

		// The uncompressed size is an upper bound on the compressed size, so
		// a payload already under the limit needs no compression pass at all.
		if len(payload) <= maximumPayloadSize {
			return payload, nil
		}

		// Check the compressed size without retaining the compressed bytes.
		size, err := compressedSize(payload)
		if err != nil {
			lastError = fmt.Errorf("failed to compress payload: %w", err)
			break
		}

		if size <= maximumPayloadSize {
			return payload, nil
		}

		lastError = fmt.Errorf("snapshot buffer is too large to construct payload")
	}

	// Encountered an error or we've tried all retentions and still can't fit the payload
	// so clear the buffer and return the last error seen
	l.Reset()
	return nil, lastError
}
