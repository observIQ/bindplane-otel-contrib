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

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// jsonArrayNoClose builds a JSON array of complete records with no closing ']', large
// enough that content detection completes before the end.
func jsonArrayNoClose() []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i := 0; i < 120; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"host":"h","msg":"padding padding padding"}`)
	}
	return b.Bytes()
}

// TestJSONArray_BrokenStreamAtBoundaryRetries asserts a stream failure landing exactly on
// a JSON array element boundary is reported as a broken stream (retry), not as a truncated
// object (which would dead-letter a recoverable download). The trailing closing-delimiter
// check must classify its error the same way the per-element decode does.
func TestJSONArray_BrokenStreamAtBoundaryRetries(t *testing.T) {
	t.Parallel()

	connReset := errors.New("connection reset by peer")
	stream := LogStream{
		Name:        "o.json",
		Body:        io.NopCloser(&errAfterReader{data: jsonArrayNoClose(), err: connReset}),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}

	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)
	producer, err := NewRecordProducer(context.Background(), stream, reader, nil)
	require.NoError(t, err)
	seq, err := producer.Records(context.Background(), Offset{})
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

	require.Positive(t, recs, "records read before the break are still delivered")
	require.Error(t, last)
	require.True(t, IsStreamRead(last), "a broken stream at an element boundary retries")
	require.False(t, IsTruncatedObject(last), "a broken download is not a truncated object")
}

// TestJSONArray_TruncatedAtBoundaryIsTruncation asserts an array whose bytes simply run out
// at an element boundary (clean source EOF, no closing ']') is a truncated object.
func TestJSONArray_TruncatedAtBoundaryIsTruncation(t *testing.T) {
	t.Parallel()

	stream := LogStream{
		Name:        "o.json",
		Body:        io.NopCloser(bytes.NewReader(jsonArrayNoClose())),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}

	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)
	producer, err := NewRecordProducer(context.Background(), stream, reader, nil)
	require.NoError(t, err)
	seq, err := producer.Records(context.Background(), Offset{})
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

	require.Positive(t, recs)
	require.Error(t, last)
	require.True(t, IsTruncatedObject(last), "a clean-EOF array with no closing bracket is truncated")
	require.False(t, IsStreamRead(last))
}
