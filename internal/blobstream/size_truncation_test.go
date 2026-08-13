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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// halfGzip returns a gzip stream cut to half its length, plus the full uncompressed
// length. Read through a bytes.Reader the cut ends in a clean io.EOF (no raw read
// error), so only the object's known Size can reveal that the download is incomplete.
func halfGzip(t *testing.T) (data []byte, fullLen int) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := io.WriteString(zw, strings.Repeat("line-of-log-text-padding-padding\n", 5000))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	full := buf.Bytes()
	return full[:len(full)/2], len(full)
}

// TestSize_ShortDownloadIsRetryable asserts that when the object's Size is known and the
// raw source delivered fewer bytes than that Size (an incomplete download that ended in a
// clean io.EOF, so RawReadErr is nil), the failure is a retryable broken stream, exercising
// the RawTruncated size comparison. Without Size the same bytes read as a stored truncation
// (see TestTruncatedGzipIsNotRetryable) — this is the size-based distinction.
func TestSize_ShortDownloadIsRetryable(t *testing.T) {
	t.Parallel()

	cut, fullLen := halfGzip(t)
	stream := LogStream{
		Name:        "logs/object.gz",
		Body:        io.NopCloser(bytes.NewReader(cut)),
		MaxLogSize:  testMaxLogSize,
		Size:        int64(fullLen), // the full object is larger than what was delivered
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

	require.Positive(t, recs, "records read before the cut are still delivered")
	require.Error(t, last)
	require.True(t, IsStreamRead(last), "a download short of the known Size retries, it is not a stored truncation")
	require.False(t, IsTruncatedObject(last))
	require.True(t, reader.RawTruncated(), "the raw source ended short of Size")
}

// TestSize_ConstructionShortDownloadIsRetryable asserts the same size-based rule at reader
// construction: a gzip cut inside its header (so gzip.NewReader fails) with a known larger
// Size is a retryable incomplete download, not a corrupt container.
func TestSize_ConstructionShortDownloadIsRetryable(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := io.WriteString(zw, "hello world hello world")
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	full := buf.Bytes()

	// Deliver only the first 5 bytes (inside the 10-byte gzip header) with a clean EOF.
	stream := LogStream{
		Name:        "logs/object.gz",
		Body:        io.NopCloser(bytes.NewReader(full[:5])),
		MaxLogSize:  testMaxLogSize,
		Size:        int64(len(full)),
		Logger:      zap.NewNop(),
		TryDecoding: false,
	}

	_, err = stream.BufferedReader(context.Background())
	require.Error(t, err)
	require.True(t, IsStreamRead(err), "a header cut short of Size is an incomplete download, not corruption")
	require.False(t, IsUnsupportedContent(err), "it must not also route to the DLQ")
}
