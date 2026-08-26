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

// TestJSONSequence_BrokenStreamDuringResyncRetries asserts that when the source breaks
// while the parser is scanning forward for the newline that ends a malformed line, the
// object is reported as a broken stream (retry), not silently acked. resyncAfterNewline
// gives up on any read error; without consulting the raw source the worker would ack an
// incomplete object and lose every record after the break.
func TestJSONSequence_BrokenStreamDuringResyncRetries(t *testing.T) {
	t.Parallel()

	// Good NDJSON records, then a malformed line with no terminating newline, then the
	// stream breaks — so the resync scan hits the read error before finding a newline.
	// The body must exceed the detection/classification window (4096 bytes) so the read
	// error lands during the resync scan rather than during content detection.
	var sb strings.Builder
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&sb, `{"n":%d}`+"\n", i)
	}
	sb.WriteString(`{bad`)
	body := []byte(sb.String())
	require.Greater(t, len(body), maxRecordsSearchBytes+256, "body must exceed the classification window")
	readErr := errors.New("connection reset by peer")

	stream := LogStream{
		Name:        "logs/object.json",
		Body:        &cutAfter{data: body, n: len(body), err: readErr},
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

	require.Positive(t, recs, "records before the break are delivered")
	require.Error(t, last, "a broken stream during resync must surface an error")
	require.True(t, IsStreamRead(last), "a broken stream during resync must retry")
	require.ErrorIs(t, last, readErr)
}
