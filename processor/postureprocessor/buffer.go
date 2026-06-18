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

package postureprocessor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.uber.org/zap"
)

// tierQueue is a durable FIFO for a single tier, backed by a storage client.
//
// The storage.Client interface has no range scan, so the queue maintains its
// own index: head and tail sequence counters persisted in a meta record.
// Payloads are stored at q/<tier>/<seq>. Each mutation writes the payload (or a
// delete) and the updated meta in one atomic Batch so a crash can never leave
// the index inconsistent with the stored data.
type tierQueue struct {
	tier   string
	client storage.Client
	logger *zap.Logger

	maxBytes int64
	maxItems int64
	overflow string

	mu    sync.Mutex
	head  uint64
	tail  uint64
	bytes int64
	items int64
}

type queueMeta struct {
	Head  uint64 `json:"head"`
	Tail  uint64 `json:"tail"`
	Bytes int64  `json:"bytes"`
	Items int64  `json:"items"`
}

func (q *tierQueue) metaKey() string            { return "meta/" + q.tier }
func (q *tierQueue) payloadKey(s uint64) string { return fmt.Sprintf("q/%s/%020d", q.tier, s) }

// load reads the persisted meta record so the queue resumes where it left off.
func (q *tierQueue) load(ctx context.Context) error {
	raw, err := q.client.Get(ctx, q.metaKey())
	if err != nil {
		return fmt.Errorf("load meta: %w", err)
	}
	if raw == nil {
		return nil // fresh queue
	}
	var m queueMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("unmarshal meta: %w", err)
	}
	q.head, q.tail, q.bytes, q.items = m.Head, m.Tail, m.Bytes, m.Items
	return nil
}

func (q *tierQueue) overUnlocked(addItems, addBytes int64) bool {
	if q.maxItems > 0 && q.items+addItems > q.maxItems {
		return true
	}
	if q.maxBytes > 0 && q.bytes+addBytes > q.maxBytes {
		return true
	}
	return false
}

// enqueue appends a payload, applying the overflow policy if the tier is full.
func (q *tierQueue) enqueue(ctx context.Context, payload []byte) error {
	size := int64(len(payload))
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.overUnlocked(1, size) {
		switch q.overflow {
		case overflowDropNewest, overflowBlockDrop:
			q.logger.Warn("tier buffer full, dropping incoming batch",
				zap.String("tier", q.tier), zap.String("policy", q.overflow))
			return nil
		default: // drop_oldest
			for q.items > 0 && q.overUnlocked(1, size) {
				if err := q.evictOldestLocked(ctx); err != nil {
					return err
				}
			}
			if q.overUnlocked(1, size) {
				// Single batch larger than the whole quota; drop it.
				q.logger.Warn("incoming batch exceeds tier quota, dropping",
					zap.String("tier", q.tier), zap.Int64("size", size))
				return nil
			}
		}
	}

	seq := q.tail
	nm := queueMeta{Head: q.head, Tail: q.tail + 1, Bytes: q.bytes + size, Items: q.items + 1}
	metaBytes, err := json.Marshal(nm)
	if err != nil {
		return err
	}
	if err := q.client.Batch(ctx,
		storage.SetOperation(q.payloadKey(seq), payload),
		storage.SetOperation(q.metaKey(), metaBytes),
	); err != nil {
		return fmt.Errorf("enqueue batch: %w", err)
	}
	q.tail, q.bytes, q.items = nm.Tail, nm.Bytes, nm.Items
	return nil
}

// evictOldestLocked removes the head item. Caller must hold q.mu.
func (q *tierQueue) evictOldestLocked(ctx context.Context) error {
	key := q.payloadKey(q.head)
	data, err := q.client.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("evict get: %w", err)
	}
	size := int64(len(data))
	nm := queueMeta{Head: q.head + 1, Tail: q.tail, Bytes: q.bytes - size, Items: q.items - 1}
	if nm.Bytes < 0 {
		nm.Bytes = 0
	}
	metaBytes, err := json.Marshal(nm)
	if err != nil {
		return err
	}
	if err := q.client.Batch(ctx,
		storage.DeleteOperation(key),
		storage.SetOperation(q.metaKey(), metaBytes),
	); err != nil {
		return fmt.Errorf("evict batch: %w", err)
	}
	q.head, q.bytes, q.items = nm.Head, nm.Bytes, nm.Items
	q.logger.Debug("evicted oldest buffered batch", zap.String("tier", q.tier))
	return nil
}

// peek returns the head payload and its sequence without removing it.
func (q *tierQueue) peek(ctx context.Context) (payload []byte, seq uint64, ok bool) {
	q.mu.Lock()
	head, tail := q.head, q.tail
	q.mu.Unlock()
	if head >= tail {
		return nil, 0, false
	}
	data, err := q.client.Get(ctx, q.payloadKey(head))
	if err != nil || data == nil {
		q.logger.Warn("failed to read buffered batch", zap.String("tier", q.tier), zap.Uint64("seq", head), zap.Error(err))
		return nil, 0, false
	}
	return data, head, true
}

// advance removes a successfully drained item at seq.
func (q *tierQueue) advance(ctx context.Context, seq uint64, size int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if seq != q.head {
		return nil // already advanced
	}
	nm := queueMeta{Head: q.head + 1, Tail: q.tail, Bytes: q.bytes - int64(size), Items: q.items - 1}
	if nm.Bytes < 0 {
		nm.Bytes = 0
	}
	metaBytes, err := json.Marshal(nm)
	if err != nil {
		return err
	}
	if err := q.client.Batch(ctx,
		storage.DeleteOperation(q.payloadKey(seq)),
		storage.SetOperation(q.metaKey(), metaBytes),
	); err != nil {
		return fmt.Errorf("advance batch: %w", err)
	}
	q.head, q.bytes, q.items = nm.Head, nm.Bytes, nm.Items
	return nil
}

func (q *tierQueue) empty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.head >= q.tail
}
