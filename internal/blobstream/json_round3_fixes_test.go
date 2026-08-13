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

// TestJSONWrapper_OversizedBracketedElementRejectedRestDelivered asserts that an array
// element larger than max_log_size is rejected (max_log_size is a hard wall) while the
// elements after it still deliver — the decoder stays aligned, so the tail is not dropped.
func TestJSONWrapper_OversizedBracketedElementRejectedRestDelivered(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 6000) // > the 4096 reader max_log_size -> over the hard wall
	body := `{"Records":[{"n":1},{"big":"` + big + `"},{"n":2},{"n":3}]}`

	records, errCount := collectRecordsAndErrors(t, body)

	require.Equal(t, []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, records,
		"the oversized element is rejected but the records after it still deliver")
	require.Equal(t, 1, errCount, "the oversized element is one counted parse error")
}

// TestJSONWrapper_ConcatPrefixStreamBreakIsRetryable asserts that a stream break while
// reading a concatenated document's prefix is a retryable ErrStreamRead, not a silent-ack
// parse error that drops the recoverable tail.
func TestJSONWrapper_ConcatPrefixStreamBreakIsRetryable(t *testing.T) {
	t.Parallel()

	readErr := errors.New("read: connection reset by peer")
	// A complete first wrapper whose records run past the classification window (so
	// detection and the first document succeed), then a second document whose non-Records
	// prefix is cut mid value by a broken stream.
	var b strings.Builder
	b.WriteString(`{"Records":[`)
	const delivered = 400
	for i := 0; i < delivered; i++ {
		fmt.Fprintf(&b, `{"host":"host-%04d"},`, i)
	}
	b.WriteString(`{"host":"last"}]}`)
	require.Greater(t, b.Len(), maxRecordsSearchBytes)
	b.WriteString(`{"pad":"` + strings.Repeat("x", 50)) // second document, cut mid value
	body := &errAfterPrefix{prefix: []byte(b.String()), err: readErr}

	records, err := drainJSON(t, NewJSONParser(NewBufferedReader(body, testMaxLogSize), BodyOptions{}))

	require.Len(t, records, delivered+1, "the first document's records are delivered")
	require.ErrorIs(t, err, readErr)
	require.True(t, IsStreamRead(err), "a break in a concatenated document's prefix is retryable, not a silent ack")
	require.False(t, IsUnsupportedContent(err))
}

// countingSource counts bytes read from the underlying reader.
type countingSource struct {
	r io.Reader
	n int64
}

func (c *countingSource) Read(p []byte) (int, error) {
	m, err := c.r.Read(p)
	c.n += int64(m)
	return m, err
}

// TestJSONWrapper_OversizedTailTokenIsBounded asserts that token navigation (here a records
// wrapper's oversized tail value) does not read the whole token into memory. Before the
// fix the cap was disarmed during navigation, so a giant scalar was buffered whole (an OOM
// vector); now the read stops at the search-window budget.
func TestJSONWrapper_OversizedTailTokenIsBounded(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 8<<20) // 8 MiB tail value
	body := `{"Records":[{"n":1}],"pad":"` + big + `"}`

	src := &countingSource{r: strings.NewReader(body)}
	parser := NewJSONParser(NewBufferedReader(src, 4096), BodyOptions{})

	seq, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)
	var records int
	for _, rerr := range seq {
		if rerr == nil {
			records++
		}
	}

	require.GreaterOrEqual(t, records, 1, "the record before the oversized tail is delivered")
	require.Less(t, src.n, int64(1<<20),
		"navigation must not read the whole oversized tail token into memory (read %d bytes)", src.n)
}

// noMaxLogSizeReader is a reader that does not report max_log_size.
type noMaxLogSizeReader struct{ BufferedReader }

// TestRecordSizeLimit_FallsBackWithoutMaxLogSize asserts a reader that reports no
// max_log_size (a test double) falls back to the hard OOM limit.
func TestRecordSizeLimit_FallsBackWithoutMaxLogSize(t *testing.T) {
	t.Parallel()
	require.Equal(t, maxRecordBytesHardLimit, recordSizeLimit(noMaxLogSizeReader{}))
}

// TestJSONArray_ConcatStringStreamBreakIsRetryable asserts that a break while reading a
// concatenated top-level token after an array (here a cut string) is retryable, not a
// silent-ack parse error.
func TestJSONArray_ConcatStringStreamBreakIsRetryable(t *testing.T) {
	t.Parallel()

	readErr := errors.New("read: connection reset by peer")
	var b strings.Builder
	b.WriteString("[")
	const delivered = 400
	for i := 0; i < delivered; i++ {
		fmt.Fprintf(&b, `{"host":"host-%04d"},`, i)
	}
	b.WriteString(`{"host":"last"}]`)
	require.Greater(t, b.Len(), maxRecordsSearchBytes)
	b.WriteString(`"` + strings.Repeat("x", 50)) // a second top-level string, cut mid value
	body := &errAfterPrefix{prefix: []byte(b.String()), err: readErr}

	records, err := drainJSON(t, NewJSONParser(NewBufferedReader(body, testMaxLogSize), BodyOptions{}))

	require.Len(t, records, delivered+1, "the array's records are delivered")
	require.ErrorIs(t, err, readErr)
	require.True(t, IsStreamRead(err), "a break reading the next concatenated token is retryable")
}

// TestJSONWrapper_ConcatPrefixOversizedValueIsBounded asserts that an oversized non-Records
// value in a concatenated document's prefix is bounded (not buffered whole) and treated as
// a counted parse error, not the line-fallback sentinel.
func TestJSONWrapper_ConcatPrefixOversizedValueIsBounded(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString(`{"Records":[`)
	const delivered = 400
	for i := 0; i < delivered; i++ {
		fmt.Fprintf(&b, `{"host":"host-%04d"},`, i)
	}
	b.WriteString(`{"host":"last"}]}`)
	require.Greater(t, b.Len(), maxRecordsSearchBytes)
	// Second document: a non-Records prefix value far larger than the search window.
	b.WriteString(`{"pad":"` + strings.Repeat("x", 8192) + `","Records":[{"n":2}]}`)

	records, err := drainJSON(t, NewJSONParser(NewBufferedReader(strings.NewReader(b.String()), 4096), BodyOptions{}))

	require.Len(t, records, delivered+1, "only the first document's records are delivered")
	require.Error(t, err)
	require.False(t, IsStreamRead(err), "a bounded oversized prefix is a parse error, not a stream break")
	require.NotErrorIs(t, err, ErrNotArrayOrKnownObject, "must be a non-sentinel error, not the line-fallback trigger")
}

// TestJSONSequence_OverOOMBackstopResyncs asserts that a value-sequence value too large to
// decode within the OOM backstop is skipped and the sequence resyncs to the records after
// it. The backstop is lowered here so the test need not allocate a real 128 MiB value.
func TestJSONSequence_OverOOMBackstopResyncs(t *testing.T) {
	// Not parallel: temporarily lowers maxRecordBytesHardLimit, a package var.
	orig := maxRecordBytesHardLimit
	maxRecordBytesHardLimit = 4096
	defer func() { maxRecordBytesHardLimit = orig }()

	big := strings.Repeat("x", 6000) // exceeds the lowered backstop, cannot be decoded
	body := `{"n":1}` + "\n" + `{"big":"` + big + `"}` + "\n" + `{"n":2}` + "\n"

	records, errCount := collectRecordsAndErrors(t, body)
	require.Equal(t, []string{`{"n":1}`, `{"n":2}`}, records, "the over-backstop value is skipped, the sequence resyncs")
	require.Equal(t, 1, errCount)
}

// TestJSONArray_OverOOMBackstopStops asserts that a bracketed element too large to decode
// within the OOM backstop cannot realign, so the array stops after it (only the extreme
// case; a merely-over-max_log_size element decodes and is rejected while the rest deliver).
func TestJSONArray_OverOOMBackstopStops(t *testing.T) {
	// Not parallel: temporarily lowers maxRecordBytesHardLimit, a package var.
	orig := maxRecordBytesHardLimit
	maxRecordBytesHardLimit = 4096
	defer func() { maxRecordBytesHardLimit = orig }()

	big := strings.Repeat("x", 6000)
	body := `[{"n":1},{"big":"` + big + `"},{"n":2}]`

	records, errCount := collectRecordsAndErrors(t, body)
	require.Equal(t, []string{`{"n":1}`}, records, "a bracketed element over the backstop cannot realign; the array stops")
	require.Equal(t, 1, errCount)
}

// TestJSON_ConsumerStopAtOversizedRecord asserts the parser stops cleanly when the consumer
// breaks iteration on the parse error yielded for an over-max_log_size record.
func TestJSON_ConsumerStopAtOversizedRecord(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 6000) // > 4096 max_log_size
	body := `{"n":1}` + "\n" + `{"big":"` + big + `"}` + "\n" + `{"m":2}` + "\n"

	reader := NewBufferedReader(strings.NewReader(body), 4096)
	parser := NewJSONParser(reader, BodyOptions{})
	seq, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var errs int
	for _, rerr := range seq {
		if rerr != nil {
			errs++
			break // stop at the oversized-record parse error, so yield returns false
		}
	}
	require.Equal(t, 1, errs)
}
