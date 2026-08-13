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
	"bufio"
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// TestJSONArray_StopsOnTruncatedElements asserts an array cut off mid-element keeps the
// records before the cut and reports the truncation. The decoder stops reporting further
// elements either way, so a silent stop would ack the object and lose the rest.
func TestJSONArray_StopsOnTruncatedElements(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
		want []any
	}{
		{
			name: "cut inside the second element",
			body: `[{"host":"a"},{"host":`,
			want: []any{map[string]any{"host": "a"}},
		},
		{
			name: "cut right after a separator",
			body: `[{"host":"a"},`,
			want: []any{map[string]any{"host": "a"}},
		},
		{
			name: "cut before the closing bracket",
			body: `[{"host":"a"}`,
			want: []any{map[string]any{"host": "a"}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bodies, err := driveJSON(t, tc.body)
			require.Equal(t, tc.want, bodies, "records before the cut are still delivered")
			require.True(t, IsTruncatedObject(err), "the truncation must reach the caller")
		})
	}
}

// TestJSONArray_ResumesAtTheStartOffset asserts a resumed read drops the records the
// previous run already sent. Without the skip, every redelivery would duplicate them.
func TestJSONArray_ResumesAtTheStartOffset(t *testing.T) {
	t.Parallel()

	const body = `[{"n":1},{"n":2},{"n":3}]`

	// Read two records and keep the position the parser reports, which is what the
	// worker checkpoints.
	first, resumeAt := readJSONRecords(t, body, Offset{}, 2)
	require.Equal(t, []any{map[string]any{"n": float64(1)}, map[string]any{"n": float64(2)}}, first)

	// A fresh producer over the same object resumes from that position.
	rest, _ := readJSONRecords(t, body, resumeAt, 0)
	require.Equal(t, []any{map[string]any{"n": float64(3)}}, rest)
}

// readJSONRecords reads from start, stopping after limit records when limit is
// positive. It returns the record bodies and the position reached.
func readJSONRecords(t *testing.T, body string, start Offset, limit int) ([]any, Offset) {
	t.Helper()
	ctx := context.Background()

	stream := detectionStream(body, zap.NewNop(), true)
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)

	seq, err := producer.Records(ctx, start)
	require.NoError(t, err)

	var bodies []any
	for rec, rerr := range seq {
		require.NoError(t, rerr)
		lr := plog.NewLogRecord()
		require.NoError(t, producer.AppendLogBody(ctx, lr, rec))
		bodies = append(bodies, lr.Body().AsRaw())
		if limit > 0 && len(bodies) == limit {
			break
		}
	}
	return bodies, producer.Position()
}

// unreadableReader reports a full buffer holding a trailing carriage return, then
// refuses to return the byte. It drives the recovery path for a split "\r\n".
type unreadableReader struct {
	BufferedReader
	unreadErr error
}

func (r unreadableReader) ReadSlice(byte) ([]byte, error) {
	return []byte("partial\r"), bufio.ErrBufferFull
}

func (r unreadableReader) UnreadByte() error { return r.unreadErr }

// TestReadLine_ReportsAFailedUnread asserts a reader that cannot return the carriage
// return surfaces the failure instead of splitting a "\r\n" across two records.
func TestReadLine_ReportsAFailedUnread(t *testing.T) {
	t.Parallel()

	unreadErr := errors.New("invalid use of UnreadByte")
	_, _, err := readLine(unreadableReader{
		BufferedReader: NewBufferedReader(strings.NewReader(""), 16),
		unreadErr:      unreadErr,
	})
	require.ErrorIs(t, err, unreadErr)
}

// TestArchive_ReportsUnusableTempDir asserts a random-access archive that cannot be
// materialized fails with a clear error. Zip and 7z are read from a temp file, so a
// broken temp directory stops them before any entry is opened.
func TestArchive_ReportsUnusableTempDir(t *testing.T) {
	// Not parallel: the temp directory is package state.
	original := archiveTempDir
	archiveTempDir = filepath.Join(t.TempDir(), "does-not-exist")
	defer func() { archiveTempDir = original }()

	sevenZip, err := os.ReadFile("testdata/logs.7z")
	require.NoError(t, err)

	testCases := []struct {
		name string
		body []byte
	}{
		{name: "zip", body: zipBytes(t, []tarFile{{name: "a.log", body: []byte("kept\n")}})},
		{name: "7z", body: sevenZip},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := driveArchiveWithLogger(t, tc.body, zap.NewNop())
			require.ErrorContains(t, err, "create temp file")
		})
	}
}

// TestAvro_StopsWhenTheConsumerBreaks asserts the Avro iterator releases when the
// caller stops early, which is how a batch limit ends a read.
func TestAvro_StopsWhenTheConsumerBreaks(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/sample_logs.avro")
	require.NoError(t, err)

	var seen int
	for range avroRecords(t, body) {
		seen++
		break
	}
	require.Equal(t, 1, seen)
}

// TestAvro_StopsWhenTheConsumerBreaksOnAnError asserts a caller that stops on a bad
// block releases the iterator instead of reading the rest of the object.
func TestAvro_StopsWhenTheConsumerBreaksOnAnError(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/sample_logs_corrupt_block.avro")
	require.NoError(t, err)

	var readErr error
	for _, rerr := range avroRecords(t, body) {
		if rerr != nil {
			readErr = rerr
			break
		}
	}
	require.Error(t, readErr, "a corrupt block must reach the caller")
}

// avroRecords returns the record sequence for an Avro object.
func avroRecords(t *testing.T, body []byte) iter.Seq2[any, error] {
	t.Helper()
	ctx := context.Background()

	stream := LogStream{
		Name:        "logs/object.avro",
		Body:        newNopReadCloser(body),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)

	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)
	return seq
}
