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
	"compress/gzip"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestDecompressBombCapped verifies a decompression bomb (a tiny gzip that
// expands far past the cap) fails the object with a decompress-limit error that
// routes to the unsupported-file DLQ condition, rather than buffering the whole
// expansion into memory.
func TestDecompressBombCapped(t *testing.T) {
	old := maxDecompressedBytes
	maxDecompressedBytes = 100
	t.Cleanup(func() { maxDecompressedBytes = old })

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, err := gz.Write(bytes.Repeat([]byte("A"), 4096))
	require.NoError(t, err)
	require.NoError(t, gz.Close())

	stream := LogStream{
		Name:       "bomb.gz",
		Body:       newNopReadCloser(buf.Bytes()),
		MaxLogSize: testMaxLogSize,
		Logger:     zap.NewNop(),
	}
	br, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	_, err = io.ReadAll(br)
	require.Error(t, err)
	var limit ErrDecompressLimitExceeded
	require.ErrorAs(t, err, &limit)
	require.True(t, IsUnsupportedContent(err))
}

// TestDecompressUncompressedNotCapped verifies uncompressed passthrough content is
// not subject to the decompression cap: a plain body larger than the cap reads in
// full, since only decompressed output can inflate a bomb.
func TestDecompressUncompressedNotCapped(t *testing.T) {
	old := maxDecompressedBytes
	maxDecompressedBytes = 100
	t.Cleanup(func() { maxDecompressedBytes = old })

	body := bytes.Repeat([]byte("plain\n"), 100) // 600 bytes, uncompressed
	stream := LogStream{
		Name:       "plain.log",
		Body:       newNopReadCloser(body),
		MaxLogSize: testMaxLogSize,
		Logger:     zap.NewNop(),
	}
	br, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	got, err := io.ReadAll(br)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

// TestDecompressLimitReader_TripIsSticky verifies the reader trips once its output
// exceeds the cap and then fails every subsequent read, so a parser cannot keep
// pulling bytes past the limit.
func TestDecompressLimitReader_TripIsSticky(t *testing.T) {
	t.Parallel()

	r := &decompressLimitReader{r: bytes.NewReader(bytes.Repeat([]byte("A"), 100)), limit: 10}
	buf := make([]byte, 50)

	_, err := r.Read(buf)
	var limit ErrDecompressLimitExceeded
	require.ErrorAs(t, err, &limit)

	// A subsequent read stays tripped.
	_, err = r.Read(buf)
	require.ErrorAs(t, err, &limit)
	require.Contains(t, err.Error(), "decompressed output exceeded")
}
