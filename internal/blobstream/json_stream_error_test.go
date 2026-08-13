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
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// errAfterPrefix serves prefix and then fails every read.
type errAfterPrefix struct {
	prefix []byte
	pos    int
	err    error
}

func (r *errAfterPrefix) Read(p []byte) (int, error) {
	if r.pos >= len(r.prefix) {
		return 0, r.err
	}
	n := copy(p, r.prefix[r.pos:])
	r.pos += n
	return n, nil
}

func (r *errAfterPrefix) Close() error { return nil }

// drainJSON reads a parser to exhaustion and returns the records and the last error.
func drainJSON(t *testing.T, parser LogParser) ([]any, error) {
	t.Helper()

	seq, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var records []any
	var lastErr error
	for rec, rerr := range seq {
		if rerr != nil {
			lastErr = rerr
			continue
		}
		records = append(records, rec)
	}
	return records, lastErr
}

// TestJSONParser_MarksABrokenStream asserts a read failure part way through an array is
// marked, so the caller fails the object rather than acking a partial read.
func TestJSONParser_MarksABrokenStream(t *testing.T) {
	t.Parallel()

	readErr := errors.New("read: connection reset by peer")

	// The array runs past the classification window, so detection succeeds and the
	// break lands while records are being decoded.
	var prefix strings.Builder
	prefix.WriteString("[")
	const delivered = 400
	for i := 0; i < delivered; i++ {
		fmt.Fprintf(&prefix, `{"host":"host-%04d"},`, i)
	}
	require.Greater(t, prefix.Len(), maxRecordsSearchBytes)

	body := &errAfterPrefix{prefix: []byte(prefix.String()), err: readErr}

	records, err := drainJSON(t, NewJSONParser(NewBufferedReader(body, testMaxLogSize)))

	require.Len(t, records, delivered, "records read before the break are still delivered")
	require.ErrorIs(t, err, readErr)
	require.True(t, IsStreamRead(err), "a broken stream must be marked")
	require.False(t, IsUnsupportedContent(err), "a broken stream is retryable")
}

// TestJSONParser_DoesNotMarkMalformedBytes asserts malformed content stays a record
// error. The same bytes fail the same way on a retry, so the object must not be requeued.
func TestJSONParser_DoesNotMarkMalformedBytes(t *testing.T) {
	t.Parallel()

	body := io.NopCloser(strings.NewReader(`[{"host":"a"},{"host":,}]`))

	records, err := drainJSON(t, NewJSONParser(NewBufferedReader(body, testMaxLogSize)))

	require.Len(t, records, 1)
	require.Error(t, err)
	require.False(t, IsStreamRead(err), "malformed bytes are not a broken stream")
	require.ErrorContains(t, err, "decode record")
}
