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
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// binaryPadding keeps a crafted header detecting as its own format rather than as text.
var binaryPadding = bytes.Repeat([]byte{0x00}, 60)

// errReadCloser fails every read. It stands in for a connection that drops after the
// object is opened but before any bytes arrive.
type errReadCloser struct{ err error }

func (e errReadCloser) Read([]byte) (int, error) { return 0, e.err }
func (e errReadCloser) Close() error             { return nil }

// TestBufferedReader_ReportsUnreadableBodies asserts a body that cannot be read fails
// before any parser is chosen. The object is retried, so the error must not be
// swallowed into an empty read.
func TestBufferedReader_ReportsUnreadableBodies(t *testing.T) {
	t.Parallel()

	readErr := errors.New("connection reset by peer")
	stream := &LogStream{
		Name:       "logs/object",
		Body:       errReadCloser{err: readErr},
		MaxLogSize: testMaxLogSize,
		Logger:     zap.NewNop(),
	}

	_, err := stream.BufferedReader(context.Background())
	require.ErrorIs(t, err, readErr)
	require.ErrorContains(t, err, "peek content")
	require.False(t, IsUnsupportedContent(err), "a read error must stay retryable")
}

// TestBufferedReader_ReportsCorruptCompression asserts a recognized container whose
// header does not decode fails at reader construction. Detection matched the format, so
// passing the bytes through would emit compressed data as log lines.
func TestBufferedReader_ReportsCorruptCompression(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body []byte
		want string
	}{
		{
			// A gzip header naming a compression method that does not exist.
			name: "gzip",
			body: append([]byte{0x1f, 0x8b, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, binaryPadding...),
			want: "create gzip reader",
		},
		{
			// An xz stream header whose flags fail their own checksum.
			name: "xz",
			body: append([]byte{0xfd, '7', 'z', 'X', 'Z', 0x00, 0xff, 0xff, 0xff, 0xff}, binaryPadding...),
			want: "create xz reader",
		},
		{
			// A zlib header naming a preset dictionary the object does not carry.
			name: "zlib",
			body: append([]byte{0x78, 0x20}, binaryPadding...),
			want: "create zlib reader",
		},
		{
			// An lzip container declaring a version this decoder does not read.
			name: "lzip",
			body: append([]byte{'L', 'Z', 'I', 'P', 0xff, 0x0c}, binaryPadding...),
			want: "create lzip reader",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stream := &LogStream{
				Name:       "logs/object",
				Body:       io.NopCloser(bytes.NewReader(tc.body)),
				MaxLogSize: testMaxLogSize,
				Logger:     zap.NewNop(),
			}

			_, err := stream.BufferedReader(context.Background())
			require.Error(t, err)
			require.ErrorContains(t, err, tc.want)
			require.False(t, IsUnsupportedContent(err),
				"a corrupt container is retryable rather than a dead-letter condition")
		})
	}
}

// TestBufferedReader_PassesThroughUnknownContent asserts content matching no codec is
// handed on unchanged rather than rejected.
func TestBufferedReader_PassesThroughUnknownContent(t *testing.T) {
	t.Parallel()

	stream := &LogStream{
		Name:       "logs/object",
		Body:       io.NopCloser(bytes.NewReader(append([]byte{0x01, 0x02, 0x03}, binaryPadding...))),
		MaxLogSize: testMaxLogSize,
		Logger:     zap.NewNop(),
	}

	got, err := readAllFromStream(t, stream)
	require.NoError(t, err)
	require.Equal(t, append([]byte{0x01, 0x02, 0x03}, binaryPadding...), got)
}

// TestBufferedReader_ReportsAFailedZstdDecoder asserts a zstd decoder that cannot be
// built surfaces as an error rather than passing compressed bytes through as log lines.
// The decoder only fails on an option it rejects, so the test supplies one.
func TestBufferedReader_ReportsAFailedZstdDecoder(t *testing.T) {
	// Not parallel: the decoder options are package state.
	original := zstdDecoderOptions
	zstdDecoderOptions = []zstd.DOption{zstd.WithDecoderMaxMemory(0)}
	defer func() { zstdDecoderOptions = original }()

	stream := &LogStream{
		Name:       "logs/object",
		Body:       io.NopCloser(bytes.NewReader(zstdBytes(t, codecPayload))),
		MaxLogSize: testMaxLogSize,
		Logger:     zap.NewNop(),
	}

	_, err := stream.BufferedReader(context.Background())
	require.ErrorContains(t, err, "create zstd reader")
	require.False(t, IsUnsupportedContent(err), "a decoder failure stays retryable")
}
