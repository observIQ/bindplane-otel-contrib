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
	"github.com/sorairolake/lzip-go"
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

	// Size is the object's known raw size (Content-Length), used to tell an object
	// stored truncated from a download that broke early. Zero means unknown.
	Size int64

	// Raw and IncludeLogRecordOriginal reach the parser for this object. See
	// BodyOptions.
	Raw                      bool
	IncludeLogRecordOriginal bool

	// zstdDecoderOpts overrides the zstd decoder options. Nil uses
	// defaultZstdDecoderOptions. A test sets it to an option the decoder rejects to
	// exercise the build-failure path on its own stream, without mutating package state.
	zstdDecoderOpts []zstd.DOption

	// archiveTempDir is the directory used to materialize random-access archives
	// (zip, 7z). Empty uses the OS default temp directory. A test sets it to a scratch
	// dir (to assert nothing is left behind) or a missing dir (to force a create
	// failure) on its own stream, without mutating package state.
	archiveTempDir string
}

// bodyOptions returns the body options configured for this stream.
func (stream *LogStream) bodyOptions() BodyOptions {
	return BodyOptions{
		Raw:                      stream.Raw,
		IncludeLogRecordOriginal: stream.IncludeLogRecordOriginal,
	}
}

// BufferedReader returns a BufferedReader for the log stream. Compression is
// decided from the object's actual bytes, not from its name or Content-Encoding
// label: customers set both incorrectly, and GCS decompressive transcoding can
// strip compression while leaving the label in place. The label is only used to
// surface a warning when it disagrees with the detected content.
func (stream *LogStream) BufferedReader(_ context.Context) (BufferedReader, error) {
	// Count the raw source below decompression, so a decoder failure can be told apart
	// from a broken source stream: a raw read error means the stream broke (retry), and
	// a decoder failure with no raw read error means the content itself is at fault.
	raw := &countingReader{reader: stream.Body}

	// Wrap the body so the leading bytes can be inspected without consuming them.
	// The same wrapper is handed downstream, so no bytes are lost.
	br := bufio.NewReaderSize(raw, detectionPeekBytes)

	reader, decompressed, err := stream.decompress(br)
	if err != nil {
		// decompress classifies a delivered-but-unreadable header as a corrupt
		// container, but it cannot see the raw source: a download cut inside the header
		// (a raw read error, or fewer bytes than the object's size) is an incomplete
		// download that a retry can still read, not corruption. Mirror classifyReadFailure
		// so construction and parse-time failures agree.
		if raw.ReadErr() != nil || (stream.Size > 0 && raw.AtEOF() && raw.Offset() < stream.Size) {
			// Strip any corrupt-container wrapper so the result is purely a broken
			// stream and does not also satisfy IsUnsupportedContent (which would route
			// it to the dead-letter queue instead of a retry).
			inner := err
			var corrupt ErrCorruptContainer
			if errors.As(err, &corrupt) {
				inner = corrupt.Err
			}
			return nil, ErrStreamRead{Err: inner}
		}
		return nil, err
	}
	if decompressed {
		reader = &decompressLimitReader{r: reader, limit: maxDecompressedBytes}
	}
	return newBufferedReaderWithRaw(reader, stream.MaxLogSize, raw, stream.Size), nil
}

// defaultZstdDecoderOptions configures the zstd decoder. Concurrency 1 keeps it
// synchronous so it spawns no goroutines to leak, since the reader is never explicitly
// closed. A stream may override it via zstdDecoderOpts (used by a test to supply an
// option the decoder rejects, which is the only way the constructor fails).
var defaultZstdDecoderOptions = []zstd.DOption{zstd.WithDecoderConcurrency(1)}

// zstdOpts returns the stream's zstd decoder options, falling back to the package
// default when the stream sets no override.
func (stream *LogStream) zstdOpts() []zstd.DOption {
	if stream.zstdDecoderOpts != nil {
		return stream.zstdDecoderOpts
	}
	return defaultZstdDecoderOptions
}

// decompress detects the compression of the peeked content and returns a reader
// over the decompressed bytes. Decompression is single-level: the format stage
// re-detects the decompressed content. Unknown or uncompressed content is passed
// through unchanged.
func (stream *LogStream) decompress(br *bufio.Reader) (io.Reader, bool, error) {
	header, err := br.Peek(detectionPeekBytes)
	// A short object yields io.EOF (fewer than detectionPeekBytes available); that
	// is expected and the partial header is still valid for detection.
	// A truncated source yields io.ErrUnexpectedEOF here; the partial header is still
	// valid for format detection, and the truncation surfaces later at parse time where
	// it is classified, so tolerate it like a clean short read.
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false, fmt.Errorf("peek content: %w", err)
	}

	detected := mimetype.Detect(header)
	stream.warnOnGzipLabelMismatch(detected)

	switch {
	case detected.Is("application/gzip"):
		gzipReader, err := gzip.NewReader(br)
		if err != nil {
			// The header would not parse: the object is corrupt or truncated so early
			// it has no usable header. Route it to the dead-letter queue rather than
			// failing with an unclassified error that redelivers forever.
			return nil, false, ErrCorruptContainer{Format: "gzip", Err: err}
		}
		return gzipReader, true, nil
	case detected.Is("application/x-bzip2"):
		return bzip2.NewReader(br), true, nil
	case detected.Is("application/x-xz"):
		xzReader, err := xz.NewReader(br)
		if err != nil {
			return nil, false, ErrCorruptContainer{Format: "xz", Err: err}
		}
		return xzReader, true, nil
	case detected.Is("application/zstd"):
		zstdReader, err := zstd.NewReader(br, stream.zstdOpts()...)
		if err != nil {
			// A reader that will not build cannot decode the object, and a retry reads
			// the same bytes. Route it to the dead-letter queue rather than failing with
			// an unclassified error that redelivers forever, matching the decoders above.
			return nil, false, ErrCorruptContainer{Format: "zstd", Err: err}
		}
		return zstdReader.IOReadCloser(), true, nil
	case detected.Is("application/zlib"):
		// zlib carries a recognizable header, so it is content-detected. Raw
		// (headerless) DEFLATE has no such header and is handled via label-assist
		// in octetStreamDecoder.
		zlibReader, err := zlib.NewReader(br)
		if err != nil {
			return nil, false, ErrCorruptContainer{Format: "zlib", Err: err}
		}
		return zlibReader, true, nil
	case detected.Is("application/lzip"):
		// lzip carries a recognizable container magic (LZIP), so mimetype detects
		// it, but decoding needs a dedicated container decoder (ulikunitz/xz does
		// not read the lzip container).
		lzipReader, err := lzip.NewReader(br)
		if err != nil {
			// A truncated or corrupt lzip header fails here (for example a garbage
			// size field). Route it to the dead-letter queue rather than failing with
			// an unclassified error that redelivers forever.
			return nil, false, ErrCorruptContainer{Format: "lzip", Err: err}
		}
		return lzipReader, true, nil
	case detected.Is("application/octet-stream"):
		reader, matched, err := stream.octetStreamDecoder(br, header)
		if err != nil {
			return nil, false, err
		}
		if matched {
			return reader, true, nil
		}
		return br, false, nil
	default:
		return br, false, nil
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
