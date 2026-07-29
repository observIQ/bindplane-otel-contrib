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

package snapshot

import (
	"bytes"
	"compress/gzip"
	"io"
	"sync"
)

// gzipWriterPool reuses gzip writers across compression calls. A gzip writer
// at the default level carries roughly 700KiB of compressor state (hash
// tables, window, token buffers), so allocating one per call is a large
// source of garbage.
var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
}

// countingWriter counts the bytes written to it, discarding the data.
type countingWriter struct{ n int }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

// compressedSize returns the size data would gzip compress to, without
// allocating or retaining the compressed bytes. It is safe for concurrent
// use.
func compressedSize(data []byte) (int, error) {
	cw := &countingWriter{}

	w := gzipWriterPool.Get().(*gzip.Writer)
	defer gzipWriterPool.Put(w)
	w.Reset(cw)

	if _, err := w.Write(data); err != nil {
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}

	return cw.n, nil
}

// Compress gzip compresses the input data. It is safe for concurrent use.
func Compress(data []byte) ([]byte, error) {
	// Pre-size the output buffer with a rough gzip ratio estimate to avoid
	// growth reallocations while writing.
	buf := bytes.NewBuffer(make([]byte, 0, len(data)/3+512))

	w := gzipWriterPool.Get().(*gzip.Writer)
	defer gzipWriterPool.Put(w)
	w.Reset(buf)

	if _, err := w.Write(data); err != nil {
		return nil, err
	}

	// Close flushes remaining data and writes the gzip footer. The writer is
	// reusable after Reset, so pooling it after Close is safe.
	if err := w.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
