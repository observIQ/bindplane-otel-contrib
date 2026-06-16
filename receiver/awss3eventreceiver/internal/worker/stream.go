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
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"

	"go.uber.org/zap"
)

// gzipMagic is the start of a gzip member: the two ID bytes followed by the
// deflate compression method (the only method defined by RFC 1952). Matching
// all three bytes makes a false positive on real log data effectively
// impossible.
var gzipMagic = []byte{0x1f, 0x8b, 0x08}

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

// BufferedReader returns a BufferedReader for the log stream. Content is
// decompressed when it is gzipped, detected in priority order:
//  1. a "content-encoding: gzip" header,
//  2. a ".gz" object key suffix, or
//  3. when neither is present, the gzip magic number at the start of the body.
//
// Case 3 covers producers such as the AWS Landing Zone Accelerator that emit
// gzip-compressed objects without the header or extension.
func (stream *LogStream) BufferedReader(_ context.Context) (BufferedReader, error) {
	// An explicit content-encoding header wins.
	if stream.ContentEncoding != nil {
		switch *stream.ContentEncoding {
		case "gzip":
			return stream.gzipReader(stream.Body)

		default:
			stream.Logger.Warn("unsupported content encoding", zap.String("content_encoding", *stream.ContentEncoding))
			return NewBufferedReader(stream.Body, stream.MaxLogSize), nil
		}
	}

	// Then the object key extension.
	if strings.HasSuffix(stream.Name, ".gz") {
		return stream.gzipReader(stream.Body)
	}

	// No explicit signal: sniff the leading bytes. The object is always fetched
	// whole from byte 0 (no Range GET) and decompression precedes any offset
	// skip, so the gzip header is reliably present here on first read and on
	// restart/resume alike. Peek does not consume the bytes, so they remain
	// available whether we decompress or pass the body through unchanged.
	br := bufio.NewReader(stream.Body)
	if magic, err := br.Peek(len(gzipMagic)); err == nil && bytes.Equal(magic, gzipMagic) {
		return stream.gzipReader(br)
	}
	return NewBufferedReader(br, stream.MaxLogSize), nil
}

// gzipReader wraps r in a gzip decompressor and returns a BufferedReader over
// the decompressed stream.
func (stream *LogStream) gzipReader(r io.Reader) (BufferedReader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("create gzip reader: %w", err)
	}
	return NewBufferedReader(gz, stream.MaxLogSize), nil
}
