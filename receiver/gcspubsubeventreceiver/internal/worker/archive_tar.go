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
	"archive/tar"
	"io"
	"strings"
)

// tarBackend is a streaming archiveBackend over stdlib archive/tar. It reads
// entries sequentially and never materializes the archive, so it works on
// arbitrarily large tar streams (including the decompressed output of a
// .tar.gz/.tar.zst/.tar.xz/.tar.bz2 object).
type tarBackend struct {
	tr *tar.Reader
}

var _ archiveBackend = (*tarBackend)(nil)

// newTarBackend wraps r (positioned at the first tar byte) in a tar reader.
func newTarBackend(r io.Reader) *tarBackend {
	return &tarBackend{tr: tar.NewReader(r)}
}

// Next advances to the next tar entry. It returns io.EOF when the archive is
// exhausted.
func (b *tarBackend) Next() (archiveEntry, error) {
	hdr, err := b.tr.Next()
	if err != nil {
		return nil, err
	}
	return &tarEntry{hdr: hdr, r: b.tr}, nil
}

// Close is a no-op: the tar reader holds no resources of its own (the underlying
// stream is owned by the caller).
func (b *tarBackend) Close() error { return nil }

// tarEntry is a single tar member. Its reader is the shared tar.Reader and is
// only valid until the next call to tarBackend.Next, which matches the
// forward-only iteration the producer performs.
type tarEntry struct {
	hdr *tar.Header
	r   io.Reader
}

var _ archiveEntry = (*tarEntry)(nil)

func (e *tarEntry) Name() string { return e.hdr.Name }

// IsDir reports whether the entry is a directory (nothing to parse).
func (e *tarEntry) IsDir() bool {
	return e.hdr.Typeflag == tar.TypeDir || strings.HasSuffix(e.hdr.Name, "/")
}

// Open returns a reader over the entry's bytes. For tar this is the shared
// stream reader positioned at the current entry.
func (e *tarEntry) Open() (io.Reader, error) { return e.r, nil }
