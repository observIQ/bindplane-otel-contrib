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
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// driveLines runs plain-text content through the line parser and returns the delivered
// records and the final error. deliver is how many bytes the source hands over before a
// clean EOF; size is the object's known size (0 = unknown).
func driveLines(t *testing.T, body []byte, deliver int, size int64) ([]string, error) {
	t.Helper()
	ctx := context.Background()

	stream := LogStream{
		Name:        "logs/object.log",
		Body:        &cutAfter{data: body, n: deliver},
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: false,
		Size:        size,
	}
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)

	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var recs []string
	var last error
	for rec, rerr := range seq {
		if rerr != nil {
			last = rerr
			continue
		}
		recs = append(recs, rec.(string))
	}
	return recs, last
}

// TestLine_TruncatedDownloadIsReportedNotAcked asserts that a text object whose download
// ends cleanly short of its known size reports the truncation (so the worker retries)
// rather than emitting the cut final fragment as a record and acking a partial object.
func TestLine_TruncatedDownloadIsReportedNotAcked(t *testing.T) {
	t.Parallel()

	body := []byte("line1\nline2\nline3-was-cut")
	size := int64(len(body) + 100) // the stored object is larger than what was delivered

	recs, err := driveLines(t, body, len(body), size)

	require.Error(t, err, "a download that ends short of the known size must be reported")
	require.True(t, IsStreamRead(err), "a short download is retryable, got %v", err)
	require.NotContains(t, recs, "line3-was-cut", "the cut final fragment must not be emitted as a record")
	require.Equal(t, []string{"line1", "line2"}, recs)
}

// TestLine_CompleteObjectEmitsFinalUnterminatedLine asserts the fix does not regress a
// legitimate final line that simply has no trailing newline in a complete object.
func TestLine_CompleteObjectEmitsFinalUnterminatedLine(t *testing.T) {
	t.Parallel()

	body := []byte("line1\nline2\nline3-no-newline")

	// Delivered in full; size matches (a complete object).
	recs, err := driveLines(t, body, len(body), int64(len(body)))

	require.NoError(t, err)
	require.Equal(t, []string{"line1", "line2", "line3-no-newline"}, recs)
}

// TestLine_StaleOffsetPastShrunkObjectIsClassified asserts that a saved resume offset
// beyond the end of a now-shorter object yields a classified condition (so the worker
// dead-letters or retries) rather than a bare "discard to offset" error that redelivers
// the object forever.
func TestLine_StaleOffsetPastShrunkObjectIsClassified(t *testing.T) {
	t.Parallel()

	body := []byte("only-a-little\n")
	ctx := context.Background()
	stream := LogStream{
		Name:        "logs/object.log",
		Body:        &cutAfter{data: body, n: len(body)},
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: false,
		Size:        int64(len(body)),
	}
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)
	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)

	seq, err := producer.Records(ctx, Offset{Offset: 10000})
	out := err
	if out == nil {
		for _, rerr := range seq {
			if rerr != nil {
				out = rerr
			}
		}
	}
	require.Error(t, out, "a stale offset past the object must be reported")
	require.True(t, IsUnsupportedContent(out) || IsStreamRead(out),
		"the stale-offset failure must be classified (DLQ or retry), not a bare error, got %v", out)
}
