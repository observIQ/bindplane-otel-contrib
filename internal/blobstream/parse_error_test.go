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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// TestParseErrorFunc_CountsSkippedArchiveEntries asserts the receiver's counter runs
// once per skipped entry. The object still succeeds, so the count is the only signal
// that an entry was dropped.
func TestParseErrorFunc_CountsSkippedArchiveEntries(t *testing.T) {
	t.Parallel()

	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	}
	body := tarBytes(t, []tarFile{
		{name: "first.png", body: png},
		{name: "second.png", body: png},
		{name: "a.log", body: []byte("kept1\nkept2\n")},
	})

	var parseErrors atomic.Int64
	bodies := driveArchiveWithParseErrors(t, body, func(context.Context) {
		parseErrors.Add(1)
	})

	require.Equal(t, []string{"kept1", "kept2"}, bodies)
	require.Equal(t, int64(2), parseErrors.Load())
}

// TestParseErrorFunc_NilDisablesCounting asserts a nil func is safe. A caller that
// tracks no metric passes nil.
func TestParseErrorFunc_NilDisablesCounting(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	body := tarBytes(t, []tarFile{
		{name: "img.png", body: png},
		{name: "a.log", body: []byte("kept\n")},
	})

	require.Equal(t, []string{"kept"}, driveArchiveWithParseErrors(t, body, nil))
}

// driveArchiveWithParseErrors runs a body through the producer with the given parse
// error counter, and returns each record body.
func driveArchiveWithParseErrors(t *testing.T, body []byte, onParseError ParseErrorFunc) []string {
	t.Helper()
	ctx := context.Background()

	stream := LogStream{
		Name:        "logs/object",
		Body:        newNopReadCloser(body),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}
	br, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, br, onParseError)
	require.NoError(t, err)

	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var bodies []string
	for rec, rerr := range seq {
		if rerr != nil {
			continue
		}
		lr := plog.NewLogRecord()
		require.NoError(t, producer.AppendLogBody(ctx, lr, rec))
		bodies = append(bodies, lr.Body().AsString())
	}
	return bodies
}
