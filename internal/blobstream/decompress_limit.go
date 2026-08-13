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

package blobstream

import (
	"fmt"
	"io"
)

// maxDecompressedBytes caps the total decompressed output of a single object on
// the non-archive path, so a decompression bomb (a tiny compressed input that
// expands to an enormous stream) fails the object instead of exhausting memory.
// Uncompressed passthrough content is naturally bounded by the object size and is
// not capped. It is a variable so tests can lower it to trip the cap with small
// inputs.
var maxDecompressedBytes int64 = 32 << 30 // 32 GiB

// ErrDecompressLimitExceeded indicates an object's decompressed output exceeded
// maxDecompressedBytes (a decompression bomb). It routes to the unsupported-file
// DLQ condition so the object is not retried, since a retry decompresses the same
// bomb.
type ErrDecompressLimitExceeded struct {
	Limit int64
}

func (e ErrDecompressLimitExceeded) Error() string {
	return fmt.Sprintf("decompressed output exceeded limit of %d bytes", e.Limit)
}

// decompressLimitReader caps the total bytes read from a decompressed stream.
// Once the cap is tripped it fails every subsequent Read so the parser aborts
// promptly. It is single-goroutine (driven synchronously by the parser), so it
// needs no synchronization.
type decompressLimitReader struct {
	r       io.Reader
	limit   int64
	read    int64
	tripped error
}

func (d *decompressLimitReader) Read(p []byte) (int, error) {
	if d.tripped != nil {
		return 0, d.tripped
	}
	n, err := d.r.Read(p)
	d.read += int64(n)
	if d.limit >= 0 && d.read > d.limit {
		d.tripped = ErrDecompressLimitExceeded{Limit: d.limit}
		return 0, d.tripped
	}
	return n, err
}
