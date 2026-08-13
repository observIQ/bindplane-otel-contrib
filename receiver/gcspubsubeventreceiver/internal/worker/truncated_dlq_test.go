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
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestTruncatedObjectRoutesToDLQ asserts a truncated object is a dead-letter condition.
// Redelivering it reads the same bytes, so retrying never drains the queue.
func TestTruncatedObjectRoutesToDLQ(t *testing.T) {
	t.Parallel()

	err := error(ErrTruncatedObject{Err: io.ErrUnexpectedEOF})
	require.True(t, isDLQConditionError(err), "a truncated object must be a DLQ condition")
	require.True(t, isDLQConditionError(fmt.Errorf("parse logs: %w", err)),
		"the condition must survive wrapping")
	require.Equal(t, dlqErrorKindUnsupportedFile, dlqConditionKind(err))
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
		MaxLogSize:  4096,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}

	ctx := context.Background()
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	parser, err := newParser(ctx, stream, reader)
	require.NoError(t, err)

	logs, err := parser.Parse(ctx, 0)
	require.NoError(t, err)

	var records int
	var last error
	for _, rerr := range logs {
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
	require.True(t, isDLQConditionError(last), "it must route to the dead-letter queue")
}
