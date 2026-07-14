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
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bodgit/sevenzip"
)

// sevenZipBackend is a random-access archiveBackend over github.com/bodgit/sevenzip.
// Like zip, 7z needs an io.ReaderAt and a total size, so the body is materialized
// to a temp file that Close removes on every path. It reuses the shared iteration,
// bomb limits and offset model in archiveProducer.
type sevenZipBackend struct {
	zr   *sevenzip.Reader
	f    *os.File
	next int
}

var _ archiveBackend = (*sevenZipBackend)(nil)

// newSevenZipBackend materializes reader to a temp file and opens a 7z reader over
// it, cleaning up the temp file on any failure.
func newSevenZipBackend(reader io.Reader) (archiveBackend, error) {
	f, err := os.CreateTemp(archiveTempDir, "gcspubsub-7z-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}

	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()

	size, err := io.Copy(f, reader)
	if err != nil {
		return nil, fmt.Errorf("materialize archive: %w", err)
	}

	zr, err := sevenzip.NewReader(f, size)
	if err != nil {
		// Materialized fine but not a valid 7z: corrupt content, not transient IO.
		return nil, ErrCorruptArchive{Type: "7z", Err: err}
	}

	ok = true
	return &sevenZipBackend{zr: zr, f: f}, nil
}

// Next advances to the next 7z entry, or returns io.EOF when exhausted.
func (b *sevenZipBackend) Next() (archiveEntry, error) {
	if b.next >= len(b.zr.File) {
		return nil, io.EOF
	}
	f := b.zr.File[b.next]
	b.next++
	return &sevenZipEntry{f: f}, nil
}

// Close closes and removes the materialized temp file.
func (b *sevenZipBackend) Close() error {
	name := b.f.Name()
	cerr := b.f.Close()
	rerr := os.Remove(name)
	return errors.Join(cerr, rerr)
}

// sevenZipEntry is a single 7z member. Open returns a fresh io.ReadCloser that
// the producer closes once the entry is consumed.
type sevenZipEntry struct {
	f *sevenzip.File
}

var _ archiveEntry = (*sevenZipEntry)(nil)

func (e *sevenZipEntry) Name() string { return e.f.Name }

func (e *sevenZipEntry) IsDir() bool {
	return e.f.FileInfo().IsDir() || strings.HasSuffix(e.f.Name, "/")
}

func (e *sevenZipEntry) Open() (io.Reader, error) { return e.f.Open() }
