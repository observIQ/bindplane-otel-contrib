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

// TestJSONValueSequence_BrokenStreamRetries asserts that a value sequence (NDJSON)
// whose source breaks at a record boundary surfaces as a broken stream. json.Decoder's
// More() swallows the read error from its look-ahead, so the loop ends like a clean
// finish; without consulting the raw source the worker would deliver the records read
// and ack, losing the rest with no retry.
func TestJSONValueSequence_BrokenStreamRetries(t *testing.T) {
	t.Parallel()

	// Fixed-width NDJSON so the cut lands exactly on a record boundary, which is where
	// More() ends the loop rather than a Decode returning the error.
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, `{"n":"%03d","pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`+"\n", i)
	}
	body := []byte(sb.String())
	lineLen := strings.Index(sb.String(), "\n") + 1

	readErr := errors.New("connection reset by peer")
	stream := LogStream{
		Name:        "logs/object.json",
		Body:        &cutAfter{data: body, n: 250 * lineLen, err: readErr},
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

	require.Positive(t, recs, "records before the break are still delivered")
	require.Error(t, last, "a broken value-sequence stream must surface an error, not end silently")
	require.True(t, IsStreamRead(last), "a broken stream must be retryable")
	require.ErrorIs(t, last, readErr)
}

// TestJSONValueSequence_ShortDownloadRetries covers the size-based branch: the raw
// source ends short of the object's known size with a clean EOF, so the download was
// incomplete and must retry rather than being treated as a clean end.
func TestJSONValueSequence_ShortDownloadRetries(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, `{"n":"%03d","pad":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`+"\n", i)
	}
	body := []byte(sb.String())
	lineLen := strings.Index(sb.String(), "\n") + 1

	stream := LogStream{
		Name:        "logs/object.json",
		Body:        &cutAfter{data: body, n: 250 * lineLen}, // clean EOF at a boundary
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
		Size:        int64(len(body)), // known size; delivered short
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

	require.Positive(t, recs, "records before the short end are delivered")
	require.Error(t, last, "a source short of the known size must surface an error")
	require.True(t, IsStreamRead(last), "an incomplete download must retry")
}
