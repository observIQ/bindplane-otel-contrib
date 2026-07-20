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
	"bufio"
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/snappy"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz/lzma"
	"go.uber.org/zap"
)

// Offset-zero magic for compression formats that mimetype reports as
// application/octet-stream. These are checked directly against the peeked bytes.
var (
	lz4Magic          = []byte{0x04, 0x22, 0x4d, 0x18}
	snappyFramedMagic = []byte{0xff, 0x06, 0x00, 0x00, 's', 'N', 'a', 'P', 'p', 'Y'}
)

// octetStreamDecoder handles the compression formats that mimetype cannot
// identify (they resolve to application/octet-stream). It returns a reader over
// the decompressed bytes and whether a codec matched. When nothing matches it
// returns (nil, false, nil) so the caller passes the bytes through unchanged.
//
// lz4 and framed snappy carry reliable offset-zero magic and are matched from
// content. The remaining formats are headerless or too weakly framed to detect,
// so they are attempted only when the object's Content-Encoding label names them
// (label-assist), and the decode is best-effort.
func (stream *LogStream) octetStreamDecoder(br *bufio.Reader, header []byte) (io.Reader, bool, error) {
	switch {
	case bytes.HasPrefix(header, lz4Magic):
		return lz4.NewReader(br), true, nil
	case bytes.HasPrefix(header, snappyFramedMagic):
		return snappy.NewReader(br), true, nil
	}

	// Label-assist for headerless formats. These carry no reliable content
	// signature, so they are attempted only when a label names them. Any real
	// signal counts (name suffix, Content-Encoding, or Content-Type), and the
	// decode is best-effort.
	switch {
	case stream.labeledLZMA():
		stream.Logger.Warn("decoding lzma stream from label; content is not self-describing",
			zap.String("name", stream.Name))
		lr, err := lzma.NewReader(br)
		if err != nil {
			return nil, false, fmt.Errorf("create lzma reader: %w", err)
		}
		return lr, true, nil
	case stream.labeledDeflate():
		r, err := stream.deflateReader(br, header)
		if err != nil {
			return nil, false, err
		}
		return r, true, nil
	}
	return nil, false, nil
}

// labeledLZMA reports whether any available label names the object as lzma: a
// .lzma name, a Content-Encoding of lzma or x-lzma, or a Content-Type mentioning
// lzma (for example application/x-lzma). lzma-alone has no reliable magic, so a
// label is the only signal.
func (stream *LogStream) labeledLZMA() bool {
	if strings.HasSuffix(stream.Name, ".lzma") {
		return true
	}
	if ce := stream.ContentEncoding; ce != nil && (*ce == "lzma" || *ce == "x-lzma") {
		return true
	}
	if ct := stream.ContentType; ct != nil && strings.Contains(*ct, "lzma") {
		return true
	}
	return false
}

// labeledDeflate reports whether the object is labeled as raw DEFLATE. Raw
// DEFLATE has no standard name suffix or content type, so Content-Encoding is the
// only real signal.
func (stream *LogStream) labeledDeflate() bool {
	ce := stream.ContentEncoding
	return ce != nil && *ce == "deflate"
}

// deflateReader classifies the stream as zlib-wrapped or raw DEFLATE from its
// first two bytes and constructs exactly one reader. The stream is not seekable,
// so a wrong guess cannot be undone; the two-byte classification avoids building
// and discarding a reader.
func (stream *LogStream) deflateReader(br *bufio.Reader, header []byte) (io.Reader, error) {
	stream.Logger.Warn("decoding deflate stream from Content-Encoding label; content is not self-describing",
		zap.String("name", stream.Name))
	if len(header) >= 2 && isZlibHeader(header[0], header[1]) {
		zr, err := zlib.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("create zlib reader: %w", err)
		}
		return zr, nil
	}
	return flate.NewReader(br), nil
}

// isZlibHeader reports whether the two leading bytes form a valid zlib header:
// compression method 8 (DEFLATE), a window size (CINFO) of at most 7, and a
// 16-bit value that is a multiple of 31. Raw DEFLATE has no such header.
func isZlibHeader(b0, b1 byte) bool {
	if b0&0x0f != 8 || b0>>4 > 7 {
		return false
	}
	return (uint16(b0)<<8|uint16(b1))%31 == 0
}
