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

package worker

import (
	"io"

	"github.com/nwaples/rardecode/v2"
)

// rarBackend is a streaming archiveBackend over github.com/nwaples/rardecode. Like
// tar, rardecode exposes a single io.Reader over the sequence of entries, so no
// materialization is needed; it reuses the shared iteration, bomb limits and
// offset model in archiveProducer.
//
// Multi-volume RAR sets (an archive split across several .partN / .rNN files) are
// out of scope: one GCS object is treated as one complete volume. A part of a
// multi-volume set will fail to parse and route to the DLQ, which is the intended
// behavior for an incomplete archive.
type rarBackend struct {
	rr *rardecode.Reader
}

var _ archiveBackend = (*rarBackend)(nil)

// newRarBackend opens a rar reader over the (single-volume) stream.
func newRarBackend(reader io.Reader) (archiveBackend, error) {
	rr, err := rardecode.NewReader(reader)
	if err != nil {
		// rardecode/v2 validates the first block header here, so a bad header is
		// corrupt content, not a transient IO error: classify it as a DLQ
		// condition rather than a generic retryable failure.
		return nil, ErrCorruptArchive{Type: "rar", Err: err}
	}
	return &rarBackend{rr: rr}, nil
}

// Next advances to the next rar entry, or returns io.EOF when exhausted.
func (b *rarBackend) Next() (archiveEntry, error) {
	hdr, err := b.rr.Next()
	if err != nil {
		return nil, err
	}
	return &rarEntry{hdr: hdr, r: b.rr}, nil
}

// Close is a no-op: rardecode holds no resources beyond the caller-owned stream.
func (b *rarBackend) Close() error { return nil }

// rarEntry is a single rar member. Its reader is the shared rar reader and is
// only valid until the next call to Next, matching the forward-only iteration.
type rarEntry struct {
	hdr *rardecode.FileHeader
	r   io.Reader
}

var _ archiveEntry = (*rarEntry)(nil)

func (e *rarEntry) Name() string { return e.hdr.Name }

func (e *rarEntry) IsDir() bool { return e.hdr.IsDir }

func (e *rarEntry) Open() (io.Reader, error) { return e.r, nil }
