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
	"bytes"
	"compress/flate"
	"compress/zlib"
	"io"
	"os"
	"testing"

	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/stretchr/testify/require"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const codecPayload = "hello\nworld\nthird line\n"

func strPtr(s string) *string { return &s }

func xzBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	require.NoError(t, err)
	_, err = io.WriteString(w, s)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func zstdBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	_, err = io.WriteString(w, s)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func lz4Bytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := lz4.NewWriter(&buf)
	_, err := io.WriteString(w, s)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func snappyFramedBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := snappy.NewBufferedWriter(&buf)
	_, err := io.WriteString(w, s)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func lzmaBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := lzma.NewWriter(&buf)
	require.NoError(t, err)
	_, err = io.WriteString(w, s)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func zlibBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, err := io.WriteString(w, s)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func rawFlateBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = io.WriteString(w, s)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// TestBufferedReader_StreamingCodecs verifies each supported codec is detected
// from content (or a label for the headerless formats) and decompressed.
func TestBufferedReader_StreamingCodecs(t *testing.T) {
	t.Parallel()

	bz, err := os.ReadFile("testdata/hello.txt.bz2")
	require.NoError(t, err)
	const bzPayload = "hello\nworld\n" // matches the committed sample

	testCases := []struct {
		name            string
		body            []byte
		contentEncoding *string
		want            string
	}{
		{"bzip2", bz, nil, bzPayload},
		{"xz", xzBytes(t, codecPayload), nil, codecPayload},
		{"zstd", zstdBytes(t, codecPayload), nil, codecPayload},
		{"lz4", lz4Bytes(t, codecPayload), nil, codecPayload},
		{"snappy-framed", snappyFramedBytes(t, codecPayload), nil, codecPayload},
		{"lzma label-assist", lzmaBytes(t, codecPayload), strPtr("lzma"), codecPayload},
		{"zlib content-detected", zlibBytes(t, codecPayload), nil, codecPayload},
		{"deflate raw flate label-assist", rawFlateBytes(t, codecPayload), strPtr("deflate"), codecPayload},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stream := &LogStream{
				Name:            "logs/object",
				ContentEncoding: tc.contentEncoding,
				Body:            io.NopCloser(bytes.NewReader(tc.body)),
				MaxLogSize:      testMaxLogSize,
				Logger:          zap.NewNop(),
			}
			got, err := readAllFromStream(t, stream)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
		})
	}
}

// TestBufferedReader_LZMALabelSignals verifies lzma is decoded when any real
// label names it, not only Content-Encoding: a .lzma name or a lzma content type
// must also work, since object storage rarely sets Content-Encoding: lzma.
func TestBufferedReader_LZMALabelSignals(t *testing.T) {
	t.Parallel()

	lzmaContentType := "application/x-lzma"

	testCases := []struct {
		name            string
		objectName      string
		contentEncoding *string
		contentType     *string
	}{
		{"name suffix", "logs/data.lzma", nil, nil},
		{"content-encoding", "logs/data", strPtr("lzma"), nil},
		{"content-type", "logs/data", nil, &lzmaContentType},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stream := &LogStream{
				Name:            tc.objectName,
				ContentEncoding: tc.contentEncoding,
				ContentType:     tc.contentType,
				Body:            io.NopCloser(bytes.NewReader(lzmaBytes(t, codecPayload))),
				MaxLogSize:      testMaxLogSize,
				Logger:          zap.NewNop(),
			}
			got, err := readAllFromStream(t, stream)
			require.NoError(t, err)
			require.Equal(t, codecPayload, string(got))
		})
	}
}

// TestBufferedReader_CompressionLabelLies verifies content wins over a lying name
// and the mismatch is warned.
func TestBufferedReader_CompressionLabelLies(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)

	stream := &LogStream{
		Name:       "logs/object.gz", // claims gzip
		Body:       io.NopCloser(bytes.NewReader(zstdBytes(t, codecPayload))),
		MaxLogSize: testMaxLogSize,
		Logger:     zap.New(core),
	}
	got, err := readAllFromStream(t, stream)
	require.NoError(t, err)
	require.Equal(t, codecPayload, string(got))
	require.Positive(t, logs.FilterMessageSnippet("label").Len(),
		"expected a mismatch warning when the .gz name disagrees with zstd content")
}

// TestBufferedReader_DeflateLabelNonDeflateErrors verifies that when the content
// is an unrecognized binary (octet-stream) carrying a deflate label but is not
// actually DEFLATE, the decode surfaces an error rather than emitting silent
// garbage. (Recognized text with a deflate label is trusted as text and is not
// force-decoded; that is covered elsewhere.)
func TestBufferedReader_DeflateLabelNonDeflateErrors(t *testing.T) {
	t.Parallel()

	// A short binary run detects as application/octet-stream and is not valid
	// DEFLATE, so flate decoding fails on read.
	binary := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}
	stream := &LogStream{
		Name:            "logs/object",
		ContentEncoding: strPtr("deflate"),
		Body:            io.NopCloser(bytes.NewReader(binary)),
		MaxLogSize:      testMaxLogSize,
		Logger:          zap.NewNop(),
	}
	_, err := readAllFromStream(t, stream)
	require.Error(t, err)
}
