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
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBufferedReader_ReadLine asserts ReadLine strips the terminator and reports
// io.EOF once the stream is spent.
func TestBufferedReader_ReadLine(t *testing.T) {
	t.Parallel()

	r := NewBufferedReader(strings.NewReader("first\nsecond\r\nthird"), 64)

	for _, want := range []string{"first", "second", "third"} {
		line, isPrefix, err := r.ReadLine()
		require.NoError(t, err)
		require.False(t, isPrefix)
		require.Equal(t, want, string(line))
	}

	_, _, err := r.ReadLine()
	require.ErrorIs(t, err, io.EOF)
}

// TestBufferedReader_ReadLineSplitsOversizedLines asserts a line longer than the
// buffer arrives in pieces flagged with isPrefix. The buffer size is max_log_size, so
// this is how an oversized record is capped.
func TestBufferedReader_ReadLineSplitsOversizedLines(t *testing.T) {
	t.Parallel()

	const bufferSize = 16
	body := strings.Repeat("x", bufferSize*2) + "\n"
	r := NewBufferedReader(strings.NewReader(body), bufferSize)

	first, isPrefix, err := r.ReadLine()
	require.NoError(t, err)
	require.True(t, isPrefix, "an oversized line must report more to come")
	require.Len(t, first, bufferSize)

	var rest int
	for {
		chunk, prefix, err := r.ReadLine()
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
		rest += len(chunk)
		if !prefix {
			break
		}
	}
	require.Equal(t, bufferSize, rest)
}

// TestBufferedReader_OffsetTracksDeliveredBytes asserts the offset counts bytes handed
// to the caller rather than bytes pulled into the buffer. The offset is checkpointed,
// so a buffered-but-undelivered byte would be skipped on resume.
func TestBufferedReader_OffsetTracksDeliveredBytes(t *testing.T) {
	t.Parallel()

	r := NewBufferedReader(strings.NewReader("first\nsecond\n"), 64)
	require.Zero(t, r.Offset())

	_, err := r.Peek(4)
	require.NoError(t, err)
	require.Zero(t, r.Offset(), "a peek delivers nothing")

	_, _, err = r.ReadLine()
	require.NoError(t, err)
	require.Equal(t, int64(len("first\n")), r.Offset())

	_, _, err = r.ReadLine()
	require.NoError(t, err)
	require.Equal(t, int64(len("first\nsecond\n")), r.Offset())
}

// TestBufferedReader_UnreadByte asserts a byte returned to the buffer is read again.
// The line parser returns a trailing carriage return this way, so a split "\r\n" is
// not broken across two records.
func TestBufferedReader_UnreadByte(t *testing.T) {
	t.Parallel()

	r := NewBufferedReader(strings.NewReader("ab\n"), 64)

	slice, err := r.ReadSlice('b')
	require.NoError(t, err)
	require.Equal(t, "ab", string(slice))

	require.NoError(t, r.UnreadByte())

	rest, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "b\n", string(rest))
}

// TestBufferedReader_Read asserts the plain reader path, which the archive and Avro
// backends use instead of line reads.
func TestBufferedReader_Read(t *testing.T) {
	t.Parallel()

	r := NewBufferedReader(strings.NewReader("payload"), 64)

	buf := make([]byte, 4)
	n, err := r.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "payl", string(buf[:n]))

	rest, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "oad", string(rest))
}
