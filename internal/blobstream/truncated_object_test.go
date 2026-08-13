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

// TestLineParser_TruncatedObjectIsNotRetryable asserts a stream cut short mid-record is
// reported as content the receiver cannot use, rather than as a broken connection.
// Retrying reads the same truncated bytes, so a retryable answer redelivers forever.
func TestLineParser_TruncatedObjectIsNotRetryable(t *testing.T) {
	t.Parallel()

	reader := &errAfterPrefix{prefix: []byte("first\nsecond\n"), err: io.ErrUnexpectedEOF}
	parser := NewLineParser(NewBufferedReader(reader, testMaxLogSize))

	logs, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var records []any
	var last error
	for rec, rerr := range logs {
		if rerr != nil {
			last = rerr
			continue
		}
		records = append(records, rec)
	}

	require.Equal(t, []any{"first", "second"}, records, "records read before the cut are still delivered")
	require.Error(t, last)
	require.False(t, IsStreamRead(last), "a truncated object is not a broken connection")
	require.True(t, IsTruncatedObject(last))
	require.True(t, IsUnsupportedContent(last), "it must route to the dead-letter queue")
}

// TestTruncatedGzipIsNotRetryable covers the reported case end to end. A gzip object cut
// short decompresses to a partial record, and that must not requeue the message.
func TestTruncatedGzipIsNotRetryable(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for i := 0; i < 5000; i++ {
		_, err := zw.Write([]byte("line-of-text-padding-padding-padding\n"))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	full := buf.Bytes()
	stream := LogStream{
		Name:        "logs/object.gz",
		Body:        io.NopCloser(bytes.NewReader(full[:len(full)/2])),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}

	ctx := context.Background()
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)

	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var records int
	var last error
	for _, rerr := range seq {
		if rerr != nil {
			last = rerr
			continue
		}
		records++
	}

	require.Positive(t, records, "records before the cut are still delivered")
	require.Error(t, last)
	require.False(t, IsStreamRead(last), "a truncated gzip must not be retried")
	require.True(t, IsTruncatedObject(last))
	require.True(t, IsUnsupportedContent(last), "it must route to the dead-letter queue")
}
