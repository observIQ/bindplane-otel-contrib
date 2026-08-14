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
	"errors"
	"io"
)

// countingReader is a reader that counts the number of bytes read.
type countingReader struct {
	reader  io.Reader
	offset  int64
	readErr error
	atEOF   bool
}

// Offset returns the number of bytes read.
func (r *countingReader) Offset() int64 {
	return r.offset
}

// ReadErr returns the last failure the underlying reader reported, or nil. End of
// stream is not a failure, so it is not recorded.
//
// A decoder that hides the cause of its own failure uses this to tell a broken stream
// from content it cannot decode.
func (r *countingReader) ReadErr() error {
	return r.readErr
}

// AtEOF reports that the underlying reader reached the end of the object. A decoder
// that stops without it stopped early.
func (r *countingReader) AtEOF() bool {
	return r.atEOF
}

// Read reads the given number of bytes and updates the offset.
func (r *countingReader) Read(p []byte) (n int, err error) {
	n, err = r.reader.Read(p)
	r.offset += int64(n)
	switch {
	case err == nil:
	case errors.Is(err, io.EOF):
		r.atEOF = true
	default:
		r.readErr = err
	}
	return n, err
}
