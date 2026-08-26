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

// corruptGzip gzip-compresses body, then flips a byte in the trailing CRC32 so the
// stream decodes to its records and then fails the checksum at the end. That failure
// is deterministic: every retry reads the same bytes and fails identically.
func corruptGzip(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := gw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	data := buf.Bytes()
	data[len(data)-5] ^= 0xff // corrupt the last CRC32 byte
	return data
}

// TestLineParser_CorruptGzipIsNotRetryable asserts a gzip object with a bad checksum
// delivers the records before the failure and then reports a non-retryable, unusable
// error, rather than a broken stream that redelivers and reproduces the same failure
// forever.
func TestLineParser_CorruptGzipIsNotRetryable(t *testing.T) {
	t.Parallel()

	data := corruptGzip(t, "line1\nline2\nline3\n")
	stream := LogStream{
		Name:        "o.gz",
		Body:        io.NopCloser(bytes.NewReader(data)),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: false,
	}

	ctx := context.Background()
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)
	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)
	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var recs int
	var last error
	for _, rerr := range seq {
		if rerr != nil {
			last = rerr
			continue
		}
		recs++
	}

	require.Positive(t, recs, "records before the corrupt checksum are still delivered")
	require.Error(t, last)
	require.False(t, IsStreamRead(last), "deterministic decompression corruption must not be retryable")
	require.True(t, isUnusableContent(last), "it must route to the DLQ / be skipped, not poison-loop")
}

// TestNewRecordProducer_CorruptGzipTryDecodingIsNotRetryable covers the production
// path the test above does not: with TryDecoding set, NewRecordProducer peeks the
// decompressed stream to detect an archive. For a small object the peek reaches the
// gzip trailer and fails the checksum. That failure must be classified (a corrupt,
// non-retryable object) rather than returned bare, which the worker cannot tell from
// a transient error and so redelivers forever.
func TestNewRecordProducer_CorruptGzipTryDecodingIsNotRetryable(t *testing.T) {
	t.Parallel()

	// Small enough that the detection peek decompresses the whole body and hits the
	// bad CRC32 in the trailer rather than stopping short at detectionPeekBytes.
	data := corruptGzip(t, "line1\nline2\nline3\n")
	stream := LogStream{
		Name:        "o.gz",
		Body:        io.NopCloser(bytes.NewReader(data)),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}

	ctx := context.Background()
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	_, err = NewRecordProducer(ctx, stream, reader, nil)
	require.Error(t, err, "a corrupt gzip must fail producer selection, not proceed")
	require.False(t, IsStreamRead(err), "deterministic decompression corruption must not be retryable")
	require.True(t, IsUnsupportedContent(err), "it must route to the DLQ, not poison-loop")
}
