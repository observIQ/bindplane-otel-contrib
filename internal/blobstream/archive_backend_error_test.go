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
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// errAfterReader serves data, then returns err on every read once data is exhausted
// (rather than io.EOF). It models a stream failure landing at an archive-entry
// boundary — the read of the next tar header fails.
type errAfterReader struct {
	data []byte
	pos  int
	err  error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// TestArchive_TransientBackendErrorIsNotSilentlyAcked asserts that a stream failure
// while advancing to the next archive entry surfaces as ErrStreamRead (so the worker
// fails the object and resumes from the entry offset), rather than being logged and
// swallowed — which would ack the message and lose every entry after the break.
func TestArchive_TransientBackendErrorIsNotSilentlyAcked(t *testing.T) {
	t.Parallel()

	// A first entry large enough that content detection (peek 3072) completes within
	// it, followed by a second entry the reader never gets to.
	e1 := tarFile{name: "a.log", body: []byte(strings.Repeat("line\n", 900))}
	e2 := tarFile{name: "b.log", body: []byte("more\n")}
	full := tarBytes(t, []tarFile{e1, e2})
	boundary := 512 + roundUp512(len(e1.body)) // end of the first entry's padded block

	connReset := errors.New("connection reset by peer")
	stream := LogStream{
		Name:        "o.tar",
		Body:        io.NopCloser(&errAfterReader{data: full[:boundary], err: connReset}),
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
	var streamReadErr error
	for rec, rerr := range seq {
		if rerr != nil {
			if IsStreamRead(rerr) {
				streamReadErr = rerr
			}
			continue
		}
		_ = rec
		recs++
	}

	require.Positive(t, recs, "the readable first entry is still delivered")
	require.Error(t, streamReadErr, "a transient backend read error must surface as ErrStreamRead, not be silently acked")
}

// TestArchive_EntryErrorWithNilReaderIsCorruptArchive asserts that when the backend
// fails to advance to the next entry and there is no object-level reader to classify the
// fault (a materialized/test construction), the object fails as a corrupt archive rather
// than being silently acked — the records read before the fault are still delivered.
func TestArchive_EntryErrorWithNilReaderIsCorruptArchive(t *testing.T) {
	t.Parallel()

	backend := &stubBackend{
		entries: []archiveEntry{stubEntry{name: "a.log", body: "kept1\nkept2\n"}},
		nextErr: errors.New("unexpected end of archive header"),
	}

	producer := newStubArchive(backend, zap.NewNop())
	seq, err := producer.Records(context.Background(), Offset{})
	require.NoError(t, err)

	var bodies []string
	var last error
	for rec, rerr := range seq {
		if rerr != nil {
			last = rerr
			continue
		}
		bodies = append(bodies, rec.(string))
	}

	require.Equal(t, []string{"kept1", "kept2"}, bodies, "records read before the fault are still delivered")
	require.Error(t, last)
	var corrupt ErrCorruptArchive
	require.ErrorAs(t, last, &corrupt, "with no reader to classify the fault, it is a corrupt archive")
	require.False(t, IsStreamRead(last))
	require.False(t, IsTruncatedObject(last))
}

// TestArchive_EntryErrorWithCleanReaderIsCorruptArchive asserts the default
// classification: the backend fails to advance, but the object-level reader shows no read
// error, is not at EOF, and is not truncated — so the archive structure itself is
// malformed and the object is failed as a corrupt archive (routed to the DLQ) rather than
// retried as a broken stream.
func TestArchive_EntryErrorWithCleanReaderIsCorruptArchive(t *testing.T) {
	t.Parallel()

	// A fresh reader over unread data reports no read error, not-at-EOF, and
	// not-truncated: the clean mid-stream state that selects the default branch.
	reader := NewBufferedReader(bytes.NewReader([]byte(strings.Repeat("x", 4096))), testMaxLogSize)
	require.NoError(t, reader.RawReadErr())
	require.False(t, reader.RawAtEOF())
	require.False(t, reader.RawTruncated())

	backend := &stubBackend{
		entries: []archiveEntry{stubEntry{name: "a.log", body: "kept1\nkept2\n"}},
		nextErr: errors.New("malformed archive structure"),
	}

	producer := newStubArchive(backend, zap.NewNop())
	producer.reader = reader
	producer.format = "application/x-tar"

	seq, err := producer.Records(context.Background(), Offset{})
	require.NoError(t, err)

	var last error
	for _, rerr := range seq {
		if rerr != nil {
			last = rerr
		}
	}

	require.Error(t, last)
	var corrupt ErrCorruptArchive
	require.ErrorAs(t, last, &corrupt)
	require.Equal(t, "application/x-tar", corrupt.Type)
	require.False(t, IsStreamRead(last))
	require.False(t, IsTruncatedObject(last))
}
