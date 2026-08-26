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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// mixedSequenceParser builds a value-sequence parser over input with an observing logger.
func mixedSequenceParser(t *testing.T, input string) (LogParser, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.WarnLevel)
	reader := NewBufferedReader(strings.NewReader(input), 4096)
	return NewJSONParser(reader, zap.New(core), BodyOptions{}), logs
}

// jsonBodyStr runs AppendLogBody and returns the resulting body string.
func jsonBodyStr(t *testing.T, parser LogParser, rec any) string {
	t.Helper()
	lr := plog.NewLogRecord()
	require.NoError(t, parser.AppendLogBody(context.Background(), lr, rec))
	return lr.Body().Str()
}

// TestJSONValueSequence_EmitsStringLineAsBody pins Finding A: a value sequence that starts
// with an object (so it classifies as NDJSON) and then holds a valid JSON string value must
// emit that string as a string body rather than dropping it.
func TestJSONValueSequence_EmitsStringLineAsBody(t *testing.T) {
	t.Parallel()

	parser, logs := mixedSequenceParser(t, "{\"a\":1}\n\"plain text line\"\n{\"b\":2}\n")
	records, err := drainJSON(t, parser)
	require.NoError(t, err)
	require.Len(t, records, 3, "the string line must be emitted, not dropped")
	require.Equal(t, "plain text line", jsonBodyStr(t, parser, records[1]),
		"a top-level JSON string is emitted as its string body")
	require.Equal(t, 1, logs.FilterMessage(mixedSequenceLogMsg).Len(),
		"a mixed value sequence warns exactly once")
}

// TestJSONValueSequence_WarnsOncePerFile asserts the mixed-content warning fires a single
// time even when several non-object lines appear.
func TestJSONValueSequence_WarnsOncePerFile(t *testing.T) {
	t.Parallel()

	parser, logs := mixedSequenceParser(t, "{\"a\":1}\n\"one\"\n\"two\"\n{\"b\":2}\n")
	records, err := drainJSON(t, parser)
	require.NoError(t, err)
	require.Len(t, records, 4, "both string lines are emitted")
	require.Equal(t, 1, logs.FilterMessage(mixedSequenceLogMsg).Len(),
		"the warning is emitted once per file, not once per line")
}

// TestJSONValueSequence_EmitsUnquotedTextLineAsBody asserts a plain (unquoted) text line is a
// true string and is emitted as a string body rather than dropped as a parse error.
func TestJSONValueSequence_EmitsUnquotedTextLineAsBody(t *testing.T) {
	t.Parallel()

	parser, logs := mixedSequenceParser(t, "{\"a\":1}\n2026-01-01 INFO started\n{\"b\":2}\n")
	records, err := drainJSON(t, parser)
	require.NoError(t, err, "an unquoted text line is not an error")
	require.Len(t, records, 3, "the timestamped text line is emitted, not dropped")
	require.Equal(t, "2026-01-01 INFO started", jsonBodyStr(t, parser, records[1]),
		"a text line whose prefix looks like a JSON number is still emitted whole")
	require.Equal(t, 1, logs.FilterMessage(mixedSequenceLogMsg).Len(),
		"a mixed value sequence warns exactly once")
}

// TestJSONValueSequence_CorruptedJSONDroppedButWarns asserts a broken JSON object (not a text
// line) stays a dropped parse error while still marking the file as mixed. This is the case
// whose classification depends on the resync capturing the value from its opening byte.
func TestJSONValueSequence_CorruptedJSONDroppedButWarns(t *testing.T) {
	t.Parallel()

	parser, logs := mixedSequenceParser(t, "{\"a\":1}\n{bad json here}\n{\"b\":2}\n")
	records, err := drainJSON(t, parser)
	require.ErrorContains(t, err, "decode record", "corrupted JSON stays a parse error")
	require.Len(t, records, 2, "the corrupted object is dropped, the valid objects still deliver")
	require.Equal(t, 1, logs.FilterMessage(mixedSequenceLogMsg).Len(),
		"corrupted content also marks the file as mixed")
}

// TestJSONValueSequence_BareScalarEmittedAsText asserts a bare non-string scalar that is the
// whole line (a number here) is emitted as its own text rather than dropped.
func TestJSONValueSequence_BareScalarEmittedAsText(t *testing.T) {
	t.Parallel()

	parser, logs := mixedSequenceParser(t, "{\"a\":1}\n42\n{\"b\":2}\n")
	records, err := drainJSON(t, parser)
	require.NoError(t, err)
	require.Len(t, records, 3, "the bare scalar line is emitted as text")
	require.Equal(t, "42", jsonBodyStr(t, parser, records[1]))
	require.Equal(t, 1, logs.FilterMessage(mixedSequenceLogMsg).Len())
}

// TestJSONValueSequence_ConcatenatedObjectsStillParse guards that same-line concatenated
// objects remain records (the whole-line rule applies only to non-object values).
func TestJSONValueSequence_ConcatenatedObjectsStillParse(t *testing.T) {
	t.Parallel()

	parser, _ := mixedSequenceParser(t, "{\"a\":1}{\"b\":2}\n{\"c\":3}\n")
	records, err := drainJSON(t, parser)
	require.NoError(t, err)
	require.Len(t, records, 3, "two objects on one line plus one more are three records")
}

// TestJSONValueSequence_TextLineAtEOF covers a text line with no terminating newline.
func TestJSONValueSequence_TextLineAtEOF(t *testing.T) {
	t.Parallel()

	parser, _ := mixedSequenceParser(t, "{\"a\":1}\nhello at eof")
	records, err := drainJSON(t, parser)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "hello at eof", jsonBodyStr(t, parser, records[1]))
}

// TestJSONValueSequence_BareScalarAtEOF covers a bare scalar at end of input (no newline).
func TestJSONValueSequence_BareScalarAtEOF(t *testing.T) {
	t.Parallel()

	parser, _ := mixedSequenceParser(t, "{\"a\":1}\n5")
	records, err := drainJSON(t, parser)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "5", jsonBodyStr(t, parser, records[1]))
}

// TestJSONValueSequence_ScalarPrefixTextAtEOF covers a scalar-prefixed text line at EOF.
func TestJSONValueSequence_ScalarPrefixTextAtEOF(t *testing.T) {
	t.Parallel()

	parser, _ := mixedSequenceParser(t, "{\"a\":1}\n2026-01-01 INFO")
	records, err := drainJSON(t, parser)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "2026-01-01 INFO", jsonBodyStr(t, parser, records[1]))
}

// TestJSONValueSequence_PreservesInteriorWhitespace covers whitespace between a scalar prefix
// and its trailing text (e.g. "5 items processed").
func TestJSONValueSequence_PreservesInteriorWhitespace(t *testing.T) {
	t.Parallel()

	parser, _ := mixedSequenceParser(t, "{\"a\":1}\n5 items processed\n{\"b\":2}\n")
	records, err := drainJSON(t, parser)
	require.NoError(t, err)
	require.Len(t, records, 3)
	require.Equal(t, "5 items processed", jsonBodyStr(t, parser, records[1]))
}

// TestJSONValueSequence_TrailingCorruptedDropped covers a value whose whole line, once the
// trailing is captured, is corrupted JSON structure (an array prefix) rather than text.
func TestJSONValueSequence_TrailingCorruptedDropped(t *testing.T) {
	t.Parallel()

	parser, _ := mixedSequenceParser(t, "{\"a\":1}\n[1,2]xyz\n{\"b\":2}\n")
	records, err := drainJSON(t, parser)
	require.ErrorContains(t, err, "decode record")
	require.Len(t, records, 2, "the corrupted-array line is dropped")
}

// TestJSONValueSequence_CorruptedAtEOF covers a corrupted line with no terminating newline.
func TestJSONValueSequence_CorruptedAtEOF(t *testing.T) {
	t.Parallel()

	parser, _ := mixedSequenceParser(t, "{\"a\":1}\n{bad")
	records, err := drainJSON(t, parser)
	require.ErrorContains(t, err, "decode record")
	require.Len(t, records, 1)
}

// TestJSONValueSequence_StopsWhenConsumerBreaksOnEmittedLine covers the early returns taken
// when the consumer stops iterating on an emitted line reached by each path: a malformed text
// line, a bare scalar, and a scalar-prefixed text line.
func TestJSONValueSequence_StopsWhenConsumerBreaksOnEmittedLine(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"{\"a\":1}\nplain text\n{\"b\":2}\n",   // malformed-path text line
		"{\"a\":1}\n5\n{\"b\":2}\n",            // bare scalar
		"{\"a\":1}\n2026-01-01 x\n{\"b\":2}\n", // scalar-prefix trailing text
	} {
		parser, _ := mixedSequenceParser(t, in)
		seq, err := parser.Parse(context.Background(), 0)
		require.NoError(t, err)
		count := 0
		for _, rerr := range seq {
			require.NoError(t, rerr)
			count++
			if count == 2 {
				break
			}
		}
		require.Equal(t, 2, count, "iteration stops on the emitted line for %q", in)
	}
}

// TestJSONValueSequence_StopsWhenConsumerBreaksOnString covers the early return taken when
// the consumer stops iterating on an emitted string line.
func TestJSONValueSequence_StopsWhenConsumerBreaksOnString(t *testing.T) {
	t.Parallel()

	parser, _ := mixedSequenceParser(t, "{\"a\":1}\n\"stop here\"\n{\"b\":2}\n")
	seq, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	var got []any
	for rec, rerr := range seq {
		require.NoError(t, rerr)
		got = append(got, rec)
		if isJSONString(rec.(json.RawMessage)) {
			break
		}
	}
	require.Len(t, got, 2, "iteration stops on the string, before the trailing object")
	require.Equal(t, "stop here", jsonBodyStr(t, parser, got[1]))
}

// TestJSONValueSequence_StopsWhenConsumerBreaksOnCorruptedError covers the early returns taken
// when the consumer stops on a corrupted line's parse error, reached by the malformed path and
// by the reject path (trailing corrupted).
func TestJSONValueSequence_StopsWhenConsumerBreaksOnCorruptedError(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"{\"a\":1}\n{bad}\n{\"b\":2}\n",    // corrupted line via the malformed path
		"{\"a\":1}\n[1,2]xyz\n{\"b\":2}\n", // trailing-corrupted via the reject path
	} {
		parser, _ := mixedSequenceParser(t, in)
		seq, err := parser.Parse(context.Background(), 0)
		require.NoError(t, err)
		n := 0
		for _, rerr := range seq {
			_ = rerr
			n++
			if n == 2 {
				break
			}
		}
		require.Equal(t, 2, n, "iteration stops on the corrupted line for %q", in)
	}
}

// drainBrokenSeqAfter builds a value sequence of many objects then lastLine with no newline,
// with the source breaking at the end so the resync scan hits the read error.
func drainBrokenSeqAfter(t *testing.T, lastLine string) (recs int, last error) {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&sb, `{"n":%d}`+"\n", i)
	}
	sb.WriteString(lastLine)
	body := []byte(sb.String())
	require.Greater(t, len(body), maxRecordsSearchBytes+256, "body must exceed the classification window")
	stream := LogStream{
		Name:        "logs/object.json",
		Body:        &cutAfter{data: body, n: len(body), err: errors.New("connection reset by peer")},
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
	for _, rerr := range seq {
		if rerr != nil {
			last = rerr
			continue
		}
		recs++
	}
	return recs, last
}

// TestJSONValueSequence_BrokenStreamAfterTextLine asserts a source that breaks while capturing
// a trailing text line surfaces a retryable read error rather than acking a partial read.
func TestJSONValueSequence_BrokenStreamAfterTextLine(t *testing.T) {
	t.Parallel()

	recs, last := drainBrokenSeqAfter(t, "broken text line")
	require.Positive(t, recs, "records before the break are delivered")
	require.True(t, IsStreamRead(last), "a broken stream after a text line must retry")
}

// TestJSONValueSequence_BrokenStreamAfterScalarPrefix asserts the same for a scalar-prefixed
// text line, where the break lands during the trailing capture.
func TestJSONValueSequence_BrokenStreamAfterScalarPrefix(t *testing.T) {
	t.Parallel()

	recs, last := drainBrokenSeqAfter(t, "42 trailing text")
	require.Positive(t, recs, "records before the break are delivered")
	require.True(t, IsStreamRead(last), "a broken stream during the trailing capture must retry")
}
