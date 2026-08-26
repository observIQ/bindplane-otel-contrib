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
	"bufio"
	"io"
)

// BufferedReader is a reader that can be used to read a log stream.
type BufferedReader interface {
	Offset() int64
	ReadLine() (line []byte, isPrefix bool, err error)
	// ReadSlice reads up to and including delim. It reports the read error with any
	// bytes already held. ReadLine does not.
	ReadSlice(delim byte) (line []byte, err error)
	UnreadByte() error
	Peek(n int) ([]byte, error)
	// ReadErr returns the last failure the underlying reader reported, or nil.
	ReadErr() error
	// AtEOF reports that the underlying reader reached the end of the object.
	AtEOF() bool
	io.Reader
}

type bufferedReader struct {
	countingReader *countingReader
	reader         *bufio.Reader
}

// NewBufferedReader returns a BufferedReader that wraps the given reader and buffers the
// reads. The buffer size is the size of the buffer to use for the reader. It will be the
// maximum number of bytes that will be returned by ReadLine() and should correspond to
// the maximum log size.
func NewBufferedReader(reader io.Reader, bufferSize int) BufferedReader {
	r := &countingReader{reader: reader}
	return &bufferedReader{
		countingReader: r,
		reader:         bufio.NewReaderSize(r, bufferSize),
	}
}

var _ BufferedReader = &bufferedReader{}

// Offset returns the number of bytes read.
func (r *bufferedReader) Offset() int64 {
	// subtract the number of bytes in the buffer from the offset since those bytes haven't
	// actually be read by a consumer
	return r.countingReader.Offset() - int64(r.reader.Buffered())
}

// ReadLine reads a line from the reader.
func (r *bufferedReader) ReadLine() (line []byte, isPrefix bool, err error) {
	return r.reader.ReadLine()
}

// ReadSlice reads from the reader up to and including delim.
func (r *bufferedReader) ReadSlice(delim byte) (line []byte, err error) {
	return r.reader.ReadSlice(delim)
}

// UnreadByte returns the most recently read byte to the buffer.
func (r *bufferedReader) UnreadByte() error {
	return r.reader.UnreadByte()
}

// Read reads the given number of bytes.
func (r *bufferedReader) Read(p []byte) (n int, err error) {
	return r.reader.Read(p)
}

// Peek peeks the given number of bytes.
func (r *bufferedReader) Peek(n int) ([]byte, error) {
	return r.reader.Peek(n)
}

// ReadErr returns the last failure the underlying reader reported, or nil.
func (r *bufferedReader) ReadErr() error {
	return r.countingReader.ReadErr()
}

// AtEOF reports that the underlying reader reached the end of the object.
func (r *bufferedReader) AtEOF() bool {
	return r.countingReader.AtEOF()
}
