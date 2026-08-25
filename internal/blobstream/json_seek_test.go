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
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// splitStreamReader answers classification from header and serves the decoder from
// body. Classification peeks and the decoder reads, so the two can disagree. That is
// what a stream breaking between the two stages looks like.
type splitStreamReader struct {
	BufferedReader
	header  []byte
	body    io.Reader
	readErr error
}

func (r *splitStreamReader) Peek(n int) ([]byte, error) {
	if n > len(r.header) {
		return r.header, nil
	}
	return r.header[:n], nil
}

func (r *splitStreamReader) Read(p []byte) (int, error) {
	if r.readErr != nil {
		return 0, r.readErr
	}
	return r.body.Read(p)
}

// newSplitParser builds a parser that classifies from header and decodes from body.
func newSplitParser(header, body string, readErr error) LogParser {
	return NewJSONParser(&splitStreamReader{
		BufferedReader: NewBufferedReader(strings.NewReader(""), 4096),
		header:         []byte(header),
		body:           strings.NewReader(body),
		readErr:        readErr,
	}, nil, BodyOptions{})
}

const recordsWrapperHeader = `{"Records":[`

// TestOpensRecordsArray_RejectsNonObjects asserts the window check refuses anything that
// does not open an object. Only an object can carry a "Records" key.
func TestOpensRecordsArray_RejectsNonObjects(t *testing.T) {
	t.Parallel()

	for _, window := range []string{`[1,2]`, `"a string"`, `42`, ``, `not json`} {
		t.Run(window, func(t *testing.T) {
			t.Parallel()
			require.False(t, opensRecordsArray([]byte(window)))
		})
	}
}

// TestParse_ReportsAFailedArrayStep asserts an array whose first token cannot be read
// surfaces the read error. Classification already saw the bracket, so the failure is the
// stream breaking underneath it.
func TestParse_ReportsAFailedArrayStep(t *testing.T) {
	t.Parallel()

	readErr := errors.New("connection reset by peer")
	parser := newSplitParser(`[{"host":"a"}]`, "", readErr)

	_, err := parser.Parse(context.Background(), 0)
	require.ErrorIs(t, err, readErr)
	require.ErrorContains(t, err, "read first token")
}

// TestSeekRecordsArray_ReportsReadFailures asserts every read failure while walking to
// the "Records" array is reported. Classification saw the key in the peek window, so a
// failure here is the stream breaking rather than a shape the parser refuses.
func TestSeekRecordsArray_ReportsReadFailures(t *testing.T) {
	t.Parallel()

	readErr := errors.New("connection reset by peer")

	testCases := []struct {
		name    string
		body    string
		readErr error
		want    string
	}{
		{
			name:    "no bytes at all",
			readErr: readErr,
			want:    "read first token",
		},
		{
			name: "cut inside a key",
			body: `{"abc`,
			want: "read token",
		},
		{
			name: "cut inside a skipped value",
			body: `{"meta":{"a":`,
			want: "skip value",
		},
		{
			name: "cut after the records key",
			body: `{"Records":`,
			want: "read token",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parser := newSplitParser(recordsWrapperHeader, tc.body, tc.readErr)
			_, err := parser.Parse(context.Background(), 0)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestSeekRecordsArray_RejectsUnusableWrappers asserts an object that classification
// read as a wrapper but that holds no usable array is refused. The caller answers
// ErrNotArrayOrKnownObject with a line-parse retry.
func TestSeekRecordsArray_RejectsUnusableWrappers(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "empty object", body: `{}`},
		{name: "no records key", body: `{"host":"a"}`},
		{name: "records holding a string", body: `{"Records":"not an array"}`},
		{name: "records holding an object", body: `{"Records":{"host":"a"}}`},
		{
			name: "records past the search budget",
			body: `{"padding":"` + strings.Repeat("x", maxRecordsSearchBytes+64) +
				`","owner":"team","Records":[{"host":"a"}]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parser := newSplitParser(recordsWrapperHeader, tc.body, nil)
			_, err := parser.Parse(context.Background(), 0)
			require.ErrorIs(t, err, ErrNotArrayOrKnownObject)
		})
	}
}
