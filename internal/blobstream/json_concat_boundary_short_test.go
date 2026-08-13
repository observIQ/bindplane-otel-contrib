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
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// bigJSONArray builds a complete JSON array of n object records, large enough that content
// detection (which peeks maxRecordsSearchBytes) completes well before the array's end.
func bigJSONArray(n int) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"n":"%03d","pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`, i)
	}
	sb.WriteByte(']')
	return sb.String()
}

// TestJSONConcatenated_ShortDownloadAtArrayBoundaryRetries covers the concat boundary of a
// bracketed shape: the first array closes cleanly, but the source ends short of the known
// size right at the boundary where the next document would begin. json.Decoder.More()
// swallows the look-ahead read, so the loop ends like a clean finish; without consulting
// the raw source the worker would ack an incomplete download and lose the rest.
func TestJSONConcatenated_ShortDownloadAtArrayBoundaryRetries(t *testing.T) {
	t.Parallel()

	// Two concatenated arrays; the source is cut cleanly right after the first array's
	// closing ']', which is exactly the concat boundary.
	first := `[{"n":"000","pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},{"n":"001","pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}]`
	full := first + `[{"n":"002","pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}]`

	stream := LogStream{
		Name:        "logs/object.json",
		Body:        &cutAfter{data: []byte(full), n: len(first)}, // clean EOF at the boundary
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
		Size:        int64(len(full)), // known size; delivered short at the boundary
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

	require.Equal(t, 2, recs, "the first array's records are delivered before the short end")
	require.Error(t, last, "a source short of the known size at the concat boundary must surface an error")
	require.True(t, IsStreamRead(last), "an incomplete download at the boundary must retry")
	require.False(t, IsTruncatedObject(last), "a short download is not a truncated object")
}

// TestJSONConcatenated_BrokenStreamAtArrayBoundaryRetries covers the same concat boundary
// with a hard read error rather than a clean short EOF. The first array closes, then the
// source fails where the next document would begin; that must retry, not ack.
func TestJSONConcatenated_BrokenStreamAtArrayBoundaryRetries(t *testing.T) {
	t.Parallel()

	// The first array must exceed the detection peek window so the read error surfaces at
	// the concat boundary during iteration, not during detection.
	first := bigJSONArray(120)
	full := first + `[{"n":"999","pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}]`

	readErr := errors.New("connection reset by peer")
	stream := LogStream{
		Name:        "logs/object.json",
		Body:        &cutAfter{data: []byte(full), n: len(first), err: readErr},
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

	require.Equal(t, 120, recs, "the first array's records are delivered before the break")
	require.Error(t, last, "a broken stream at the concat boundary must surface an error")
	require.True(t, IsStreamRead(last), "a broken stream at the boundary must retry")
	require.ErrorIs(t, last, readErr)
}

// TestJSONConcatenated_ShortDownloadInWrapperTailRetries covers the records-wrapper tail:
// after the Records array closes, the object still holds trailing keys and its closing
// brace. If the source ends short of the known size while those are consumed, the token
// reads swallow it the same way; without consulting the raw source the worker would ack an
// incomplete download.
func TestJSONConcatenated_ShortDownloadInWrapperTailRetries(t *testing.T) {
	t.Parallel()

	// A wrapper whose Records array is followed by a trailing key, cut cleanly partway
	// through that tail (after the array's records are all delivered).
	head := `{"Records":[{"n":"000"},{"n":"001"}],"nextToken":"`
	full := head + `abcdef"}`

	stream := LogStream{
		Name:        "logs/object.json",
		Body:        &cutAfter{data: []byte(full), n: len(head)}, // clean EOF inside the tail
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
		Size:        int64(len(full)), // known size; delivered short in the tail
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

	require.Equal(t, 2, recs, "the wrapper's records are delivered before the short tail")
	require.Error(t, last, "a source short of the known size in the wrapper tail must surface an error")
	require.True(t, IsStreamRead(last), "an incomplete download in the wrapper tail must retry")
}
