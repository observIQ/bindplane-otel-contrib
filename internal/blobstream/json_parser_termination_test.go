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

// Internal test file — uses package blobstream to access unexported symbols.
package blobstream

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestJSONParser_MalformedElementTerminatesTheStream asserts a malformed array element
// stops the read instead of spinning.
//
// A json.Decoder cannot resync after a syntax error. Every later Decode fails on the
// same byte, and More() reports more input.
func TestJSONParser_MalformedElementTerminatesTheStream(t *testing.T) {
	t.Parallel()

	const body = `[{"host":"a"},{"host":"b"},NOTJSON,{"host":"c"}]`

	stream := LogStream{
		Name:        "object",
		Body:        io.NopCloser(strings.NewReader(body)),
		MaxLogSize:  4096,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}

	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	producer, err := NewRecordProducer(context.Background(), stream, reader, nil)
	require.NoError(t, err)

	records, err := producer.Records(context.Background(), Offset{})
	require.NoError(t, err)

	var bodies []any
	var errs []error
	iterations := 0
	exhausted := false
	for record, err := range records {
		iterations++
		if iterations > 1000 {
			exhausted = true
			break
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		bodies = append(bodies, record)
	}

	require.False(t, exhausted, "a wedged decoder must not spin on the same error")
	require.Len(t, bodies, 2, "elements decoded before the corruption are still delivered")
	require.Len(t, errs, 1, "the decode error should be surfaced exactly once")
}
