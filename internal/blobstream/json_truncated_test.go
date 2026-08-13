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
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// truncatedJSONArray returns the first half of a large JSON array.
func truncatedJSONArray(t *testing.T) []byte {
	t.Helper()

	var body bytes.Buffer
	body.WriteString("[")
	for i := 0; i < 400; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		fmt.Fprintf(&body, `{"host":"h-%04d","msg":"padding padding padding"}`, i)
	}
	raw := body.Bytes()
	return raw[:len(raw)/2]
}

// TestJSONParser_TruncatedObjectRoutesToDLQ asserts a JSON array cut short reports the
// truncation rather than ending like a clean read. The decoder stops reporting further
// elements either way, so without the check the worker acks the object and drops every
// record after the cut.
func TestJSONParser_TruncatedObjectRoutesToDLQ(t *testing.T) {
	t.Parallel()

	stream := LogStream{
		Name:        "logs/object.json",
		Body:        io.NopCloser(bytes.NewReader(truncatedJSONArray(t))),
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
	require.Error(t, last, "a truncated array must reach the caller")
	require.True(t, IsTruncatedObject(last))
	require.False(t, IsStreamRead(last), "truncation is not a broken connection")
	require.True(t, IsUnsupportedContent(last), "it must route to the dead-letter queue")
}
