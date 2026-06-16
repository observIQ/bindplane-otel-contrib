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

package worker_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"

	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/worker"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// gzipBytes returns data compressed as a single gzip member.
func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := gw.Write(data)
	require.NoError(t, err, "write gzip")
	require.NoError(t, gw.Close(), "close gzip")
	return buf.Bytes()
}

func strPtr(s string) *string { return &s }

// TestBufferedReaderDecompression covers how BufferedReader decides whether to
// decompress, including the content-based gzip sniffing that handles objects
// with no content-encoding header and no ".gz" extension (e.g. AWS Landing
// Zone Accelerator exports).
func TestBufferedReaderDecompression(t *testing.T) {
	plaintext := []byte("line one\nline two\nline three\n")
	gzipped := gzipBytes(t, plaintext)

	tests := []struct {
		name            string
		streamName      string
		contentEncoding *string
		body            []byte
		want            []byte
	}{
		{
			name:            "content-encoding header gzip, no extension",
			streamName:      "cloudwatch/exported-logs",
			contentEncoding: strPtr("gzip"),
			body:            gzipped,
			want:            plaintext,
		},
		{
			name:       "gz extension, no header",
			streamName: "logs/object.json.gz",
			body:       gzipped,
			want:       plaintext,
		},
		{
			// The LZA case: gzip-compressed bytes with neither signal present.
			name:       "magic-number sniff, no header and no extension",
			streamName: "AWSLogs/cloudwatch/exported-logs",
			body:       gzipped,
			want:       plaintext,
		},
		{
			name:       "plain text passthrough, no header and no extension",
			streamName: "logs/object.json",
			body:       plaintext,
			want:       plaintext,
		},
		{
			// An explicit non-gzip encoding is a deliberate signal; we warn and
			// pass the body through untouched rather than sniffing it.
			name:            "unsupported content-encoding passes through untouched",
			streamName:      "logs/object",
			contentEncoding: strPtr("deflate"),
			body:            plaintext,
			want:            plaintext,
		},
		{
			name:       "empty body",
			streamName: "logs/empty",
			body:       []byte{},
			want:       []byte{},
		},
		{
			name:       "one byte that matches first gzip id byte",
			streamName: "logs/tiny",
			body:       []byte{0x1f},
			want:       []byte{0x1f},
		},
		{
			// First two bytes match the gzip ID but Peek(3) cannot confirm the
			// compression-method byte, so it is treated as plain data.
			name:       "two bytes matching gzip id only",
			streamName: "logs/twobyte",
			body:       []byte{0x1f, 0x8b},
			want:       []byte{0x1f, 0x8b},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stream := worker.LogStream{
				Name:            tc.streamName,
				ContentEncoding: tc.contentEncoding,
				Body:            io.NopCloser(bytes.NewReader(tc.body)),
				MaxLogSize:      1024,
				Logger:          zap.NewNop(),
			}

			br, err := stream.BufferedReader(context.Background())
			require.NoError(t, err, "get buffered reader")

			got, err := io.ReadAll(br)
			require.NoError(t, err, "read decoded stream")
			require.Equal(t, tc.want, got)
		})
	}
}

// TestBufferedReaderSniffedGzipParsesWithCorrectOffsets verifies that a gzip
// object detected purely by its magic number (no header, no extension) decodes
// and parses identically to the same data uncompressed — including the
// decompressed-byte offsets used for restart/resume.
func TestBufferedReaderSniffedGzipParsesWithCorrectOffsets(t *testing.T) {
	plaintext := []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")
	wantOffsets := []int64{2, 4, 6, 8, 10, 12, 14, 16, 18}

	stream := worker.LogStream{
		Name:       "AWSLogs/cloudwatch/exported-logs", // no .gz suffix
		Body:       io.NopCloser(bytes.NewReader(gzipBytes(t, plaintext))),
		MaxLogSize: 1024,
		Logger:     zap.NewNop(),
	}

	br, err := stream.BufferedReader(context.Background())
	require.NoError(t, err, "get buffered reader")

	parser := worker.NewLineParser(br)
	logs, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err, "parse logs")

	var offsets []int64
	for _, err := range logs {
		require.NoError(t, err)
		offsets = append(offsets, parser.Offset())
	}

	require.Equal(t, wantOffsets, offsets)
}
