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
	"compress/bzip2"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gabriel-vasile/mimetype"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"go.uber.org/zap"
)

// detectionPeekBytes is the number of leading bytes inspected for content
// detection. It matches mimetype's default read limit so detection sees the same
// window the library would.
const detectionPeekBytes = 3072

// LogStream is a struct containing the information about a stream of logs.
type LogStream struct {
	Name            string
	ContentEncoding *string
	ContentType     *string
	Body            io.ReadCloser
	MaxLogSize      int
	Logger          *zap.Logger
	TryDecoding     bool
}

// BufferedReader returns a BufferedReader for the log stream. Compression is
// decided from the object's actual bytes, not from its name or Content-Encoding
// label: customers set both incorrectly, and GCS decompressive transcoding can
// strip compression while leaving the label in place. The label is only used to
// surface a warning when it disagrees with the detected content.
func (stream *LogStream) BufferedReader(_ context.Context) (BufferedReader, error) {
	// Wrap the body so the leading bytes can be inspected without consuming them.
	// The same wrapper is handed downstream, so no bytes are lost.
	br := bufio.NewReaderSize(stream.Body, detectionPeekBytes)

	reader, err := stream.decompress(br)
	if err != nil {
		return nil, err
	}
	return NewBufferedReader(reader, stream.MaxLogSize), nil
}

// decompress detects the compression of the peeked content and returns a reader
// over the decompressed bytes. Decompression is single-level: the format stage
// re-detects the decompressed content. Unknown or uncompressed content is passed
// through unchanged.
func (stream *LogStream) decompress(br *bufio.Reader) (io.Reader, error) {
	header, err := br.Peek(detectionPeekBytes)
	// A short object yields io.EOF (fewer than detectionPeekBytes available); that
	// is expected and the partial header is still valid for detection.
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("peek content: %w", err)
	}

	detected := mimetype.Detect(header)
	stream.warnOnGzipLabelMismatch(detected)

	switch {
	case detected.Is("application/gzip"):
		gzipReader, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("create gzip reader: %w", err)
		}
		return gzipReader, nil
	case detected.Is("application/x-bzip2"):
		return bzip2.NewReader(br), nil
	case detected.Is("application/x-xz"):
		xzReader, err := xz.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("create xz reader: %w", err)
		}
		return xzReader, nil
	case detected.Is("application/zstd"):
		// Concurrency 1 keeps the decoder synchronous so it spawns no goroutines
		// to leak (the reader is never explicitly closed).
		zstdReader, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, fmt.Errorf("create zstd reader: %w", err)
		}
		return zstdReader.IOReadCloser(), nil
	case detected.Is("application/zlib"):
		// zlib carries a recognizable header, so it is content-detected. Raw
		// (headerless) DEFLATE has no such header and is handled via label-assist
		// in octetStreamDecoder.
		zlibReader, err := zlib.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("create zlib reader: %w", err)
		}
		return zlibReader, nil
	case detected.Is("application/octet-stream"):
		reader, matched, err := stream.octetStreamDecoder(br, header)
		if err != nil {
			return nil, err
		}
		if matched {
			return reader, nil
		}
		return br, nil
	default:
		return br, nil
	}
}

// warnOnGzipLabelMismatch logs when the object's gzip label (a .gz name or
// Content-Encoding: gzip) disagrees with the detected content.
func (stream *LogStream) warnOnGzipLabelMismatch(detected *mimetype.MIME) {
	labeledGzip := strings.HasSuffix(stream.Name, ".gz") ||
		(stream.ContentEncoding != nil && *stream.ContentEncoding == "gzip")
	contentGzip := detected.Is("application/gzip")
	if labeledGzip != contentGzip {
		stream.Logger.Warn("compression label disagrees with content",
			zap.String("name", stream.Name),
			zap.Bool("labeled_gzip", labeledGzip),
			zap.String("detected_content_type", detected.String()))
	}
}
