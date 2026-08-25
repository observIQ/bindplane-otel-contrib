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

// TestJSONValueSequence_MalformedLineDroppedButWarns asserts genuinely malformed bytes stay
// a dropped parse error (not rescued as a string) while still marking the file as mixed.
func TestJSONValueSequence_MalformedLineDroppedButWarns(t *testing.T) {
	t.Parallel()

	parser, logs := mixedSequenceParser(t, "{\"a\":1}\nthis is not json\n{\"b\":2}\n")
	records, err := drainJSON(t, parser)
	require.ErrorContains(t, err, "decode record", "malformed bytes remain a parse error")
	require.Len(t, records, 2, "the malformed line is dropped, the objects still deliver")
	require.Equal(t, 1, logs.FilterMessage(mixedSequenceLogMsg).Len(),
		"malformed content also marks the file as mixed")
}

// TestJSONValueSequence_NonStringScalarWarnsButNotEmitted asserts a non-object, non-string
// value (a number) marks the file mixed and stays a parse error rather than being emitted.
func TestJSONValueSequence_NonStringScalarWarnsButNotEmitted(t *testing.T) {
	t.Parallel()

	parser, logs := mixedSequenceParser(t, "{\"a\":1}\n42\n{\"b\":2}\n")
	records, err := drainJSON(t, parser)
	require.ErrorContains(t, err, "expected a JSON object", "a bare number is not a record")
	require.Len(t, records, 2, "the number is not emitted; the objects still deliver")
	require.Equal(t, 1, logs.FilterMessage(mixedSequenceLogMsg).Len())
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
