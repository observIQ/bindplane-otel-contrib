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
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// archiveTempDir is the directory used to materialize random-access archives
// (zip, and later 7z/rar). An empty value uses the OS default temp directory.
// It is a package variable so tests can point it at a scratch directory and
// assert nothing is left behind.
var archiveTempDir = ""

// zipBackend is a random-access archiveBackend over stdlib archive/zip. zip
// needs an io.ReaderAt and a total size, which a streaming GCS body does not
// provide, so the body is materialized to a temp file first. The temp file is
// removed by Close on every path.
type zipBackend struct {
	zr   *zip.Reader
	f    *os.File
	next int
}

var _ archiveBackend = (*zipBackend)(nil)

// newZipBackend materializes reader to a temp file and opens a zip reader over
// it. On any failure it cleans up the temp file before returning, so a failed
// open never leaks.
func newZipBackend(reader io.Reader) (archiveBackend, error) {
	f, err := os.CreateTemp(archiveTempDir, "gcspubsub-zip-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}

	// Guard so a failure anywhere below removes the temp file.
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

	zr, err := zip.NewReader(f, size)
	if err != nil {
		// The bytes materialized fine but are not a valid zip: corrupt content,
		// not a transient IO error. Classify it as a DLQ condition.
		return nil, ErrCorruptArchive{Type: "zip", Err: err}
	}

	ok = true
	return &zipBackend{zr: zr, f: f}, nil
}

// Next advances to the next zip entry, or returns io.EOF when exhausted.
func (b *zipBackend) Next() (archiveEntry, error) {
	if b.next >= len(b.zr.File) {
		return nil, io.EOF
	}
	f := b.zr.File[b.next]
	b.next++
	return &zipEntry{f: f}, nil
}

// Close closes and removes the materialized temp file.
func (b *zipBackend) Close() error {
	name := b.f.Name()
	cerr := b.f.Close()
	rerr := os.Remove(name)
	return errors.Join(cerr, rerr)
}

// zipEntry is a single zip member. Open returns a fresh io.ReadCloser that the
// producer closes once the entry is consumed.
type zipEntry struct {
	f *zip.File
}

var _ archiveEntry = (*zipEntry)(nil)

func (e *zipEntry) Name() string { return e.f.Name }

func (e *zipEntry) IsDir() bool {
	return e.f.FileInfo().IsDir() || strings.HasSuffix(e.f.Name, "/")
}

func (e *zipEntry) Open() (io.Reader, error) { return e.f.Open() }
