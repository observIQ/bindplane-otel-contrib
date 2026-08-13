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
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestJSONParser_TruncatedObjectRoutesToDLQ asserts a JSON array cut short reports the
// truncation rather than ending like a clean read. Ending quietly makes the worker ack
// the object and drop every record after the cut.
func TestJSONParser_TruncatedObjectRoutesToDLQ(t *testing.T) {
	t.Parallel()

	var body bytes.Buffer
	body.WriteString("[")
	for i := 0; i < 400; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		fmt.Fprintf(&body, `{"host":"h-%04d","msg":"padding padding padding"}`, i)
	}

	raw := body.Bytes()
	stream := LogStream{
		Name:        "logs/object.json",
		Body:        io.NopCloser(bytes.NewReader(raw[:len(raw)/2])),
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
	require.Error(t, last, "a truncated array must reach the caller")
	require.True(t, IsTruncatedObject(last))
	require.False(t, IsStreamRead(last), "truncation is not a broken connection")
	require.NotNil(t, isDLQConditionError(last), "it must route to the dead-letter queue")
}
