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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// newArchiveProducer builds a producer over an in-memory archive.
func newArchiveProducer(t *testing.T, body []byte, logger *zap.Logger) RecordProducer {
	return newArchiveProducerInDir(t, body, logger, "")
}

// newArchiveProducerInDir is newArchiveProducer with an explicit temp dir for
// materializing random-access archives (empty uses the OS default). A test threads a
// missing dir to force a create failure, using its own stream rather than package state.
func newArchiveProducerInDir(t *testing.T, body []byte, logger *zap.Logger, tempDir string) RecordProducer {
	t.Helper()
	ctx := context.Background()

	stream := LogStream{
		Name:           "logs/object.tar",
		Body:           newNopReadCloser(body),
		MaxLogSize:     testMaxLogSize,
		Logger:         logger,
		TryDecoding:    true,
		archiveTempDir: tempDir,
	}
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)
	return producer
}

// driveArchiveWithLogger runs a body through the producer with the given logger and
// returns each record body, the final position, and any object-failing error.
func driveArchiveWithLogger(t *testing.T, body []byte, logger *zap.Logger) ([]string, Offset, error) {
	return driveArchiveWithLoggerInDir(t, body, logger, "")
}

// driveArchiveWithLoggerInDir is driveArchiveWithLogger with an explicit temp dir for
// materializing random-access archives (empty uses the OS default).
func driveArchiveWithLoggerInDir(t *testing.T, body []byte, logger *zap.Logger, tempDir string) ([]string, Offset, error) {
	t.Helper()
	ctx := context.Background()

	producer := newArchiveProducerInDir(t, body, logger, tempDir)
	seq, err := producer.Records(ctx, Offset{})
	if err != nil {
		return nil, Offset{}, err
	}

	var bodies []string
	for rec, rerr := range seq {
		if rerr != nil {
			if IsUnsupportedContent(rerr) {
				return bodies, producer.Position(), rerr
			}
			continue
		}
		lr := plog.NewLogRecord()
		require.NoError(t, producer.AppendLogBody(ctx, lr, rec))
		bodies = append(bodies, lr.Body().AsString())
	}
	return bodies, producer.Position(), nil
}

// TestArchive_StopsOnATruncatedArchive asserts a tar cut off mid-stream keeps the
// records it already yielded and surfaces the truncation. The entries read so far are
// good, so they are still delivered, but the object ending part way through is reported
// rather than silently acked.
func TestArchive_StopsOnATruncatedArchive(t *testing.T) {
	t.Parallel()

	first := tarFile{name: "a.log", body: []byte("kept1\nkept2\n")}
	full := tarBytes(t, []tarFile{first, {name: "b.log", body: []byte("lost1\nlost2\n")}})

	// A tar ends with a 1024-byte trailer, so one entry alone occupies everything
	// before it. Cutting part way into the next header leaves the first entry whole
	// and the second unreadable.
	firstEntryBytes := len(tarBytes(t, []tarFile{first})) - 1024
	truncated := full[:firstEntryBytes+100]

	core, logs := observer.New(zap.WarnLevel)
	bodies, _, err := driveArchiveWithLogger(t, truncated, zap.New(core))

	require.Equal(t, []string{"kept1", "kept2"}, bodies, "records read before the cut are still delivered")
	require.Error(t, err, "a truncated archive surfaces a truncation error, not a silent ack")
	require.True(t, IsTruncatedObject(err), "the bytes ran out, so it is a truncated object")
	require.Positive(t, logs.FilterMessageSnippet("archive iteration stopped").Len())
}

// TestArchive_SkipsEntriesWithNoUsableStructure asserts an entry the format stage
// cannot read is skipped while the rest of the archive is still processed.
func TestArchive_SkipsEntriesWithNoUsableStructure(t *testing.T) {
	t.Parallel()

	// A single JSON document larger than the classification window has no structure
	// the parser can read, and is long enough that detection classifies it as JSON
	// rather than plain text.
	unusable := []byte(`{"alpha":"` + strings.Repeat("x", maxRecordsSearchBytes+64) + `"}`)

	body := tarBytes(t, []tarFile{
		{name: "unusable.json", body: unusable},
		{name: "a.log", body: []byte("kept1\nkept2\n")},
	})

	core, logs := observer.New(zap.WarnLevel)
	bodies, _, err := driveArchiveWithLogger(t, body, zap.New(core))

	require.NoError(t, err)
	require.Equal(t, []string{"kept1", "kept2"}, bodies)
	require.Positive(t, logs.FilterMessageSnippet("skipping unparseable archive entry").Len())
}

// TestArchive_ForwardsPerRecordErrors asserts a malformed record inside an entry is
// reported to the caller rather than failing the object. The worker skips that record
// and keeps the rest, matching the non-archive path.
func TestArchive_ForwardsPerRecordErrors(t *testing.T) {
	t.Parallel()

	body := tarBytes(t, []tarFile{
		{name: "records.json", body: []byte(`[{"host":"a"},{"host":,}]`)},
		{name: "a.log", body: []byte("kept\n")},
	})

	ctx := context.Background()
	producer := newArchiveProducer(t, body, zap.NewNop())

	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var bodies []string
	var recordErrs int
	for rec, rerr := range seq {
		if rerr != nil {
			require.False(t, IsUnsupportedContent(rerr),
				"a bad record inside an entry must not fail the object")
			recordErrs++
			continue
		}
		lr := plog.NewLogRecord()
		require.NoError(t, producer.AppendLogBody(ctx, lr, rec))
		bodies = append(bodies, lr.Body().AsString())
	}

	require.Positive(t, recordErrs, "the malformed record must reach the caller")
	require.Contains(t, bodies, "kept", "later entries must still be read")
}

// TestArchive_StopsWhenTheConsumerBreaks asserts the iterator releases mid-entry when
// the caller stops early, which is how a batch limit ends a read.
func TestArchive_StopsWhenTheConsumerBreaks(t *testing.T) {
	t.Parallel()

	body := tarBytes(t, []tarFile{
		{name: "a.log", body: []byte("one\ntwo\nthree\n")},
		{name: "b.log", body: []byte("four\n")},
	})

	ctx := context.Background()
	producer := newArchiveProducer(t, body, zap.NewNop())

	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var seen int
	for range seq {
		seen++
		break
	}
	require.Equal(t, 1, seen)
}

// TestArchive_AppendLogBodyBeforeReading asserts appending a body before any entry is
// open is an error rather than a panic. Each entry brings its own parser, so there is
// nothing to append through until iteration starts.
func TestArchive_AppendLogBodyBeforeReading(t *testing.T) {
	t.Parallel()

	body := tarBytes(t, []tarFile{{name: "a.log", body: []byte("kept\n")}})
	producer := newArchiveProducer(t, body, zap.NewNop())

	err := producer.AppendLogBody(context.Background(), plog.NewLogRecord(), "record")
	require.ErrorContains(t, err, "no active archive entry parser")
}

// TestArchive_StopsWhenTheConsumerBreaksOnAnError asserts a caller that stops on a bad
// record inside an entry releases the iterator instead of reading the rest of the
// archive.
func TestArchive_StopsWhenTheConsumerBreaksOnAnError(t *testing.T) {
	t.Parallel()

	body := tarBytes(t, []tarFile{
		{name: "records.json", body: []byte(`[{"host":"a"},{"host":,}]`)},
		{name: "a.log", body: []byte("never read\n")},
	})

	ctx := context.Background()
	producer := newArchiveProducer(t, body, zap.NewNop())
	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var recordErr error
	var bodies []string
	for rec, rerr := range seq {
		if rerr != nil {
			recordErr = rerr
			break
		}
		lr := plog.NewLogRecord()
		require.NoError(t, producer.AppendLogBody(ctx, lr, rec))
		bodies = append(bodies, lr.Body().AsString())
	}

	require.Error(t, recordErr)
	require.NotContains(t, bodies, "never read", "breaking must stop before the next entry")
}

// TestCappingReader_TripsAndStaysTripped asserts each byte cap fires and then fails
// every later read. A reader that recovered would let a decompression bomb keep going
// after its limit was reached.
func TestCappingReader_TripsAndStaysTripped(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		entryLimit int64
		totalLimit int64
		want       string
	}{
		{name: "per entry", entryLimit: 8, totalLimit: 1 << 30, want: "per-entry uncompressed size exceeded"},
		{name: "total", entryLimit: 1 << 30, totalLimit: 8, want: "total uncompressed size exceeded"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var total int64
			capped := &cappingReader{
				r:          strings.NewReader(strings.Repeat("x", 64)),
				entryLimit: tc.entryLimit,
				total:      &total,
				totalLimit: tc.totalLimit,
			}

			buf := make([]byte, 32)
			_, err := capped.Read(buf)
			require.Error(t, err)

			var limitErr ErrArchiveLimitExceeded
			require.ErrorAs(t, err, &limitErr)
			require.Equal(t, tc.want, limitErr.Reason)
			require.True(t, IsUnsupportedContent(err))

			// Every later read repeats the same failure.
			_, again := capped.Read(buf)
			require.Equal(t, err, again)
		})
	}
}
