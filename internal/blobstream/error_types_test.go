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
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestErrorMessages pins the text of each error this package returns. The messages
// reach the collector log, where they are the only record of why an object stopped.
func TestErrorMessages(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "unsupported content",
			err:  ErrUnsupportedContent{MIMEType: "image/png"},
			want: "unsupported content type: image/png",
		},
		{
			name: "archive limit",
			err:  ErrArchiveLimitExceeded{Reason: "total uncompressed size exceeded"},
			want: "archive limit exceeded: total uncompressed size exceeded",
		},
		{
			name: "broken stream",
			err:  ErrStreamRead{Err: errors.New("read: connection reset by peer")},
			want: "read object stream: read: connection reset by peer",
		},
		{
			name: "truncated object",
			err:  ErrTruncatedObject{Err: io.ErrUnexpectedEOF},
			want: "object ends mid-record: unexpected EOF",
		},
		{
			name: "corrupt container",
			err:  ErrCorruptContainer{Format: "avro", Err: errors.New("sync marker mismatch")},
			want: "corrupt avro object: sync marker mismatch",
		},
		{
			name: "corrupt archive with a type",
			err:  ErrCorruptArchive{Type: "zip", Err: errors.New("bad header")},
			want: "corrupt zip archive: bad header",
		},
		{
			name: "corrupt archive without a type",
			err:  ErrCorruptArchive{Err: errors.New("bad header")},
			want: "corrupt archive: bad header",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.EqualError(t, tc.err, tc.want)
		})
	}
}

// TestErrCorruptArchive_Unwrap asserts the cause stays reachable. Callers match on
// the library error underneath.
func TestErrCorruptArchive_Unwrap(t *testing.T) {
	t.Parallel()

	cause := errors.New("unexpected EOF reading central directory")
	err := error(ErrCorruptArchive{Type: "zip", Err: cause})

	require.ErrorIs(t, err, cause)
	require.Equal(t, cause, errors.Unwrap(err))
}

// TestIsUnsupportedContent covers every error this package routes to the dead-letter
// queue, and confirms a transient error is not one of them. A wrong answer here either
// discards good data or redelivers an object forever.
func TestIsUnsupportedContent(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "not array or known object", err: ErrNotArrayOrKnownObject, want: true},
		{
			name: "wrapped not array or known object",
			err:  fmt.Errorf("parse object: %w", ErrNotArrayOrKnownObject),
			want: true,
		},
		{name: "unsupported content", err: ErrUnsupportedContent{MIMEType: "application/pdf"}, want: true},
		{
			name: "wrapped unsupported content",
			err:  fmt.Errorf("build parser: %w", ErrUnsupportedContent{MIMEType: "application/pdf"}),
			want: true,
		},
		{name: "archive limit", err: ErrArchiveLimitExceeded{Reason: "entry count exceeded"}, want: true},
		{
			name: "wrapped archive limit",
			err:  fmt.Errorf("read archive: %w", ErrArchiveLimitExceeded{Reason: "entry count exceeded"}),
			want: true,
		},
		{name: "corrupt archive", err: ErrCorruptArchive{Type: "tar", Err: errors.New("bad header")}, want: true},
		{
			name: "corrupt container",
			err:  ErrCorruptContainer{Format: "avro", Err: errors.New("sync marker mismatch")},
			want: true,
		},
		{
			name: "wrapped corrupt container",
			err:  fmt.Errorf("read object: %w", ErrCorruptContainer{Format: "avro", Err: errors.New("bad")}),
			want: true,
		},
		{
			name: "wrapped corrupt archive",
			err:  fmt.Errorf("open archive: %w", ErrCorruptArchive{Type: "tar", Err: errors.New("bad header")}),
			want: true,
		},
		{name: "transient network error", err: errors.New("connection reset by peer"), want: false},
		{name: "wrapped transient error", err: fmt.Errorf("read object: %w", errors.New("i/o timeout")), want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsUnsupportedContent(tc.err))
		})
	}
}
