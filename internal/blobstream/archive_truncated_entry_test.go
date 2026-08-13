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
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestArchive_TruncatedEntryCountsAsTruncationNotParseError asserts that when a streaming
// archive's stream ends within an entry, the records read before the cut are delivered and
// the object is surfaced as a single truncated object, not as a per-entry parse error.
// Counting it as a parse error double-counts the truncation, which the backend also
// reports when it then cannot advance to the next entry.
func TestArchive_TruncatedEntryCountsAsTruncationNotParseError(t *testing.T) {
	t.Parallel()

	good := tarFile{name: "a.log", body: []byte("kept1\nkept2\n")}
	big := tarFile{name: "b.log", body: []byte(strings.Repeat("padding-line\n", 200))}
	full := tarBytes(t, []tarFile{good, big})

	// Cut inside the second entry's data, so its header reads and its body does not.
	cut := 512 + roundUp512(len(good.body)) + 512 + 100
	require.Less(t, cut, len(full))

	// The backend reads through a BufferedReader so its ReadErr/AtEOF classify why
	// iteration stopped (matching the production path).
	r := NewBufferedReader(bytes.NewReader(full[:cut]), testMaxLogSize)
	var parseErrors int
	producer := &archiveProducer{
		stream:       LogStream{Name: "o", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open:         func() (archiveBackend, error) { return newTarBackend(r), nil },
		limits:       defaultArchiveLimits(),
		onParseError: func(context.Context) { parseErrors++ },
		reader:       r,
		format:       "application/x-tar",
	}

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

	// The readable entry is still delivered, and the object cut short surfaces as a
	// truncation (an object ending part way through) rather than being silently acked.
	require.GreaterOrEqual(t, len(bodies), 2)
	require.Equal(t, []string{"kept1", "kept2"}, bodies[:2], "the readable entry is still delivered")
	require.Error(t, last, "an archive cut short must surface an error, not silently ack")
	require.True(t, IsTruncatedObject(last), "the bytes ran out, so it is a truncated object")
	require.False(t, IsStreamRead(last), "no read failure occurred, so it is not a broken stream")
	require.Zero(t, parseErrors, "a stream that ends within an entry is one truncated object, not a per-entry parse error")
}

func roundUp512(n int) int {
	if n%512 == 0 {
		return n
	}
	return n + (512 - n%512)
}

// TestIsUnusableEntry covers each condition that ends an entry without ending the
// object, and confirms a broken connection is not one of them.
func TestIsUnusableContent(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "truncated object", err: ErrTruncatedObject{Err: io.ErrUnexpectedEOF}, want: true},
		{name: "no usable structure", err: ErrNotArrayOrKnownObject, want: true},
		{name: "unsupported content", err: ErrUnsupportedContent{MIMEType: "image/png"}, want: true},
		{name: "corrupt nested archive", err: ErrCorruptArchive{Type: "zip", Err: errors.New("bad header")}, want: true},
		{
			name: "wrapped unsupported content",
			err:  fmt.Errorf("read entry: %w", ErrUnsupportedContent{MIMEType: "application/pdf"}),
			want: true,
		},
		{name: "broken connection", err: ErrStreamRead{Err: errors.New("connection reset by peer")}, want: false},
		{name: "archive limit", err: ErrArchiveLimitExceeded{Reason: "entry count exceeded"}, want: false},
		{name: "unrelated", err: errors.New("i/o timeout"), want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, isUnusableContent(tc.err))
		})
	}
}

// TestArchive_MalformedEntryContentIsSkipped asserts that an archive entry whose content
// parses but then reports unusable content mid-iteration (a JSON array that decodes its
// elements and then runs out before its closing delimiter) ends that entry alone, while
// the object's other entries still read. The failure is not a broken read and the outer
// archive stream stays intact, so this exercises the isUnusableContent branch rather than
// the parse-time, stream-read, or object-level-truncation branches the other cases take.
func TestArchive_MalformedEntryContentIsSkipped(t *testing.T) {
	t.Parallel()

	// A JSON array missing its closing bracket: the decoder yields each complete element
	// and then reports the array ran out early as a truncated object - unusable content
	// that ends the entry, not the object.
	bad := tarFile{name: "a.json", body: []byte(`[{"m":"j1"},{"m":"j2"}`)}
	good := tarFile{name: "b.log", body: []byte("good1\ngood2\n")}
	full := tarBytes(t, []tarFile{bad, good})

	r := NewBufferedReader(bytes.NewReader(full), testMaxLogSize)
	var parseErrors int
	producer := &archiveProducer{
		stream:       LogStream{Name: "o.tar", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open:         func() (archiveBackend, error) { return newTarBackend(r), nil },
		limits:       defaultArchiveLimits(),
		onParseError: func(context.Context) { parseErrors++ },
		reader:       r,
		format:       "application/x-tar",
	}

	seq, err := producer.Records(context.Background(), Offset{})
	require.NoError(t, err)

	var bodies []string
	var fatal error
	for rec, rerr := range seq {
		if rerr != nil {
			fatal = rerr
			continue
		}
		// The malformed JSON entry yields json.RawMessage elements before it fails;
		// only the plain-text entry yields string bodies.
		if text, ok := rec.(string); ok {
			bodies = append(bodies, text)
		}
	}

	require.Contains(t, bodies, "good1", "the intact entry is still delivered")
	require.Contains(t, bodies, "good2")
	require.NoError(t, fatal, "unusable entry content ends the entry, not the object")
	require.Positive(t, parseErrors, "the unusable entry is counted as skipped")
}
