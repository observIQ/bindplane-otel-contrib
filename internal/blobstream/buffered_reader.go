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
	// ReadErr returns the last failure the underlying (post-decompression) reader
	// reported, or nil.
	ReadErr() error
	// AtEOF reports that the underlying (post-decompression) reader reached the end.
	AtEOF() bool
	// RawReadErr returns the last failure the raw source reported (below any
	// decompression), or nil. A decoder failure with no raw read error means the source
	// delivered the bytes and the content itself is at fault, so a retry reads the same
	// bytes; a raw read error means the stream broke and a retry may succeed.
	RawReadErr() error
	// RawAtEOF reports that the raw source reached the end of the object.
	RawAtEOF() bool
	// RawTruncated reports that the raw source ended having delivered fewer bytes than
	// the object's known size, i.e. the download did not complete. It reports false when
	// the size is unknown.
	RawTruncated() bool
	io.Reader
}

type bufferedReader struct {
	countingReader *countingReader
	reader         *bufio.Reader
	// raw counts the raw source below any decompression. For an uncompressed stream it
	// is the same counter as countingReader.
	raw *countingReader
	// expectedSize is the object's known raw size (Content-Length), or 0 when unknown.
	expectedSize int64
	// bufSize is the read buffer size, which equals the configured max_log_size. Parsers
	// use it to bound a single record the way max_log_size bounds a single line.
	bufSize int
}

// maxRecordBytes reports the configured max_log_size, so a parser can bound a single
// record to the same limit that bounds a single line.
func (r *bufferedReader) maxRecordBytes() int64 { return int64(r.bufSize) }

// NewBufferedReader returns a BufferedReader that wraps the given reader and buffers the
// reads. The buffer size is the size of the buffer to use for the reader. It will be the
// maximum number of bytes that will be returned by ReadLine() and should correspond to
// the maximum log size.
func NewBufferedReader(reader io.Reader, bufferSize int) BufferedReader {
	r := &countingReader{reader: reader}
	return &bufferedReader{
		countingReader: r,
		reader:         bufio.NewReaderSize(r, bufferSize),
		raw:            r,
		bufSize:        bufferSize,
	}
}

// newBufferedReaderWithRaw is NewBufferedReader with an explicit raw source counter and
// the object's known size, used on the main path where decompression sits between the
// source and the parser.
func newBufferedReaderWithRaw(reader io.Reader, bufferSize int, raw *countingReader, expectedSize int64) BufferedReader {
	r := &countingReader{reader: reader}
	return &bufferedReader{
		countingReader: r,
		reader:         bufio.NewReaderSize(r, bufferSize),
		raw:            raw,
		expectedSize:   expectedSize,
		bufSize:        bufferSize,
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

// RawReadErr returns the last failure the raw source reported, or nil.
func (r *bufferedReader) RawReadErr() error {
	return r.raw.ReadErr()
}

// RawAtEOF reports that the raw source reached the end of the object.
func (r *bufferedReader) RawAtEOF() bool {
	return r.raw.AtEOF()
}

// RawTruncated reports that the raw source ended short of the object's known size. It
// fails open (reports false) when completeness cannot be judged: when the size is unknown
// (expectedSize <= 0, e.g. a GCS transcoded object — logged at reader construction), and
// when the transport already decompressed the payload, since the raw counter then measures
// decompressed bytes against a compressed Content-Length. Fail-open is deliberate: a false
// truncation would redeliver a complete object forever.
func (r *bufferedReader) RawTruncated() bool {
	return r.expectedSize > 0 && r.raw.AtEOF() && r.raw.Offset() < r.expectedSize
}
