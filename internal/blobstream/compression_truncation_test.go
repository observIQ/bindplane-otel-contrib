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
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// driveCompressed runs a compressed object through the producer and returns the record
// count and the terminating error.
func driveCompressed(t *testing.T, data []byte) (int, error) {
	t.Helper()
	return driveStream(LogStream{Name: "o", Body: io.NopCloser(bytes.NewReader(data)), MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: false})
}

// driveStream runs a fully-built stream, returning any error from reader construction,
// producer creation, or iteration (a corrupt header fails at construction, not mid-read).
func driveStream(stream LogStream) (int, error) {
	reader, err := stream.BufferedReader(context.Background())
	if err != nil {
		return 0, err
	}
	producer, err := NewRecordProducer(context.Background(), stream, reader, nil)
	if err != nil {
		return 0, err
	}
	seq, err := producer.Records(context.Background(), Offset{})
	if err != nil {
		return 0, err
	}
	var recs int
	var last error
	for _, rerr := range seq {
		if rerr != nil {
			last = rerr
			continue
		}
		recs++
	}
	return recs, last
}

func cutToSixTenths(b []byte) []byte {
	if len(b) < 4 {
		return b
	}
	return b[:len(b)*6/10]
}

// TestTruncation_PerCompressionFormat asserts every supported compression format that
// can distinguish a cut stream reports it as a truncated object (so the worker delivers
// the readable prefix, acks and records the truncation) rather than a broken connection
// (which would redeliver forever). The two formats that cannot self-describe a cut are
// covered explicitly below.
func TestTruncation_PerCompressionFormat(t *testing.T) {
	body := strings.Repeat("line-of-log-text-padding-padding-padding\n", 3000)

	cases := []struct {
		name string
		data []byte
	}{
		{"gzip", gzipBytes(t, body)},
		{"zlib", zlibBytes(t, body)},
		{"xz", xzBytes(t, body)},
		{"zstd", zstdBytes(t, body)},
		{"lz4", lz4Bytes(t, body)},
	}
	// bzip2 has no Go writer; use the committed fixture.
	if bz, err := os.ReadFile("testdata/hello.txt.bz2"); err == nil {
		cases = append(cases, struct {
			name string
			data []byte
		}{"bzip2", bz})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, last := driveCompressed(t, cutToSixTenths(tc.data))
			require.Error(t, last, "a cut %s stream must surface an error", tc.name)
			require.True(t, IsTruncatedObject(last), "%s cut short is a truncated object, got %v", tc.name, last)
			require.False(t, IsStreamRead(last), "%s cut short is not a broken connection", tc.name)
		})
	}
}

// TestTruncation_SnappyAndLzipRouteToDLQ documents the two formats whose cut stream
// cannot be told apart from corruption, so they route to the dead-letter queue (the safe
// outcome) rather than being acked as a plain truncation:
//   - snappy reports a cut and a corrupt frame identically ("corrupt input");
//   - lzip fails to build its reader at all because the cut mangles the size header.
func TestTruncation_SnappyAndLzipRouteToDLQ(t *testing.T) {
	body := strings.Repeat("line-of-log-text-padding-padding-padding\n", 3000)

	_, snappyLast := driveCompressed(t, cutToSixTenths(snappyFramedBytes(t, body)))
	require.Error(t, snappyLast)
	require.True(t, IsUnsupportedContent(snappyLast), "a cut snappy stream routes to the DLQ")
	require.False(t, IsStreamRead(snappyLast), "a cut snappy stream is not retryable")

	if lz, err := os.ReadFile("testdata/hello.txt.lz"); err == nil {
		_, lzipLast := driveCompressed(t, cutToSixTenths(lz))
		require.Error(t, lzipLast)
		require.True(t, IsUnsupportedContent(lzipLast), "a cut lzip stream routes to the DLQ, not a poison loop")
		require.False(t, IsStreamRead(lzipLast), "a cut lzip stream is not retryable")
	}
}

// TestTruncation_LzmaIsSilent documents a known limitation: raw (headerless) lzma decoded
// via label-assist carries no end marker or integrity check, so a stored-truncated lzma
// object cannot be told from a complete one and is delivered short with no error. An
// interrupted download is still caught upstream by the raw read error / size check.
func TestTruncation_LzmaIsSilent(t *testing.T) {
	body := strings.Repeat("line-of-log-text-padding-padding-padding\n", 3000)
	lzma := lzmaBytes(t, body)

	// Label the object so the headerless lzma path is taken.
	ce := "lzma"
	_, last := driveStream(LogStream{
		Name:            "o.lzma",
		ContentEncoding: &ce,
		Body:            io.NopCloser(bytes.NewReader(cutToSixTenths(lzma))),
		MaxLogSize:      testMaxLogSize,
		Logger:          zap.NewNop(),
		TryDecoding:     false,
	})
	// When the cut mangles the lzma header the reader fails to build and the object goes
	// to the DLQ; when the header survives, the body simply ends with no integrity check,
	// so a stored truncation is delivered short with no error. Either outcome is safe (it
	// never redelivers forever); only a broken connection is retryable, which this is not.
	require.False(t, IsStreamRead(last), "a cut lzma object is never a retryable broken stream")
}
