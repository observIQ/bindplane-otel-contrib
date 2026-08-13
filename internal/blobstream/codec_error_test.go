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
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestIsZlibHeader covers each rule the two-byte classification applies. The stream
// cannot be rewound, so a wrong answer here builds a reader that can never recover.
func TestIsZlibHeader(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		b0, b1 byte
		want   bool
	}{
		{name: "standard zlib header", b0: 0x78, b1: 0x9c, want: true},
		{name: "best compression", b0: 0x78, b1: 0xda, want: true},
		{name: "small window", b0: 0x68, b1: 0x05, want: true},
		{name: "wrong compression method", b0: 0x77, b1: 0x9c, want: false},
		{name: "window size above seven", b0: 0x88, b1: 0x9c, want: false},
		{name: "checksum not a multiple of thirty one", b0: 0x78, b1: 0x9d, want: false},
		{name: "raw deflate first byte", b0: 0x00, b1: 0x00, want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, isZlibHeader(tc.b0, tc.b1))
		})
	}
}

// zlibWithHeader re-stamps real zlib output with the given header bytes. The header
// sits outside the adler32 checksum, so the payload still decodes. It produces content
// that carries a valid zlib header while detecting as an unrecognized binary, which is
// the only way the deflate label path reaches its zlib branch.
func zlibWithHeader(t *testing.T, s string, b0, b1 byte) []byte {
	t.Helper()
	body := zlibBytes(t, s)
	require.Greater(t, len(body), 2)
	body[0], body[1] = b0, b1
	return body
}

// TestDeflateLabel_ReadsZlibWrappedContent asserts a deflate-labeled object whose bytes
// carry a zlib header is read as zlib rather than raw DEFLATE.
func TestDeflateLabel_ReadsZlibWrappedContent(t *testing.T) {
	t.Parallel()

	stream := &LogStream{
		Name:            "logs/object",
		ContentEncoding: strPtr("deflate"),
		Body:            io.NopCloser(bytes.NewReader(zlibWithHeader(t, codecPayload, 0x68, 0x05))),
		MaxLogSize:      testMaxLogSize,
		Logger:          zap.NewNop(),
	}

	got, err := readAllFromStream(t, stream)
	require.NoError(t, err)
	require.Equal(t, codecPayload, string(got))
}

// TestDeflateLabel_ReportsUnreadableZlibHeader asserts a zlib header the reader rejects
// surfaces as an error. A preset-dictionary header names a dictionary the object does
// not carry, so no reader can decode it.
func TestDeflateLabel_ReportsUnreadableZlibHeader(t *testing.T) {
	t.Parallel()

	// 0x68 0x24 passes the two-byte classification and sets the preset-dictionary flag.
	body := append(zlibWithHeader(t, codecPayload, 0x68, 0x24), 0x00)

	stream := &LogStream{
		Name:            "logs/object",
		ContentEncoding: strPtr("deflate"),
		Body:            io.NopCloser(bytes.NewReader(body)),
		MaxLogSize:      testMaxLogSize,
		Logger:          zap.NewNop(),
	}

	_, err := readAllFromStream(t, stream)
	require.Error(t, err)
	require.ErrorContains(t, err, "zlib")
}

// TestLZMALabel_ReportsUnreadableStream asserts an object labeled lzma whose bytes are
// not lzma fails at reader construction rather than emitting garbage.
func TestLZMALabel_ReportsUnreadableStream(t *testing.T) {
	t.Parallel()

	// An lzma header's first byte encodes the properties and must be under 225. The
	// trailing zeros keep the object detecting as an unrecognized binary, which is
	// what routes it to the label-assist path.
	notLZMA := append([]byte{0xff}, bytes.Repeat([]byte{0x00}, 63)...)

	stream := &LogStream{
		Name:       "logs/object.lzma",
		Body:       io.NopCloser(bytes.NewReader(notLZMA)),
		MaxLogSize: testMaxLogSize,
		Logger:     zap.NewNop(),
	}

	_, err := readAllFromStream(t, stream)
	require.Error(t, err)
	require.ErrorContains(t, err, "lzma")
}
