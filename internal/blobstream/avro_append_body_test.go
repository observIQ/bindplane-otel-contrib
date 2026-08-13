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
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// newAvroParserForBody builds a parser directly, so each AppendLogBody branch can be
// driven without an Avro object.
func newAvroParserForBody(t *testing.T, opts BodyOptions, logger *zap.Logger) LogParser {
	t.Helper()
	return NewAvroOcfParser(NewBufferedReader(strings.NewReader(""), 4096), logger, opts)
}

// TestAvroAppendLogBody_ParsedRecordCarriesItsOriginal asserts log.record.original works
// on the parsed path. The option is independent of raw mode, so a structured body still
// carries the attribute.
func TestAvroAppendLogBody_ParsedRecordCarriesItsOriginal(t *testing.T) {
	t.Parallel()

	parser := newAvroParserForBody(t, BodyOptions{IncludeLogRecordOriginal: true}, zap.NewNop())
	lr := plog.NewLogRecord()
	record := map[string]any{"msg": "disk full"}

	require.NoError(t, parser.AppendLogBody(context.Background(), lr, record))
	require.Equal(t, record, lr.Body().Map().AsRaw(), "the body stays a map")

	original, ok := lr.Attributes().Get(logRecordOriginalAttribute)
	require.True(t, ok)
	require.JSONEq(t, `{"msg":"disk full"}`, original.Str())
}

// TestAvroAppendLogBody_ParsedRecordWithoutTheOption asserts the attribute is absent
// unless the option asks for it.
func TestAvroAppendLogBody_ParsedRecordWithoutTheOption(t *testing.T) {
	t.Parallel()

	parser := newAvroParserForBody(t, BodyOptions{}, zap.NewNop())
	lr := plog.NewLogRecord()

	require.NoError(t, parser.AppendLogBody(context.Background(), lr, map[string]any{"msg": "ok"}))
	_, ok := lr.Attributes().Get(logRecordOriginalAttribute)
	require.False(t, ok)
}

// TestAvroAppendLogBody_RejectsRecordsRawModeCannotEncode asserts raw mode reports a
// record it cannot render as text, rather than emitting an empty body.
func TestAvroAppendLogBody_RejectsRecordsRawModeCannotEncode(t *testing.T) {
	t.Parallel()

	parser := newAvroParserForBody(t, BodyOptions{Raw: true}, zap.NewNop())

	err := parser.AppendLogBody(context.Background(), plog.NewLogRecord(), make(chan int))
	require.ErrorContains(t, err, "encode avro record as text")
}

// TestAvroAppendLogBody_RejectsRecordsTheBodyCannotHold asserts a record pdata refuses
// surfaces as an error rather than an empty body.
func TestAvroAppendLogBody_RejectsRecordsTheBodyCannotHold(t *testing.T) {
	t.Parallel()

	parser := newAvroParserForBody(t, BodyOptions{}, zap.NewNop())

	err := parser.AppendLogBody(context.Background(), plog.NewLogRecord(), map[string]any{"ch": make(chan int)})
	require.Error(t, err)
}

// TestAvroAppendLogBody_WarnsWhenTheOriginalCannotBeEncoded asserts a record the JSON
// encoder refuses is still delivered, with a warning and no attribute. The body is the
// payload, so losing the attribute must not lose the record.
func TestAvroAppendLogBody_WarnsWhenTheOriginalCannotBeEncoded(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	parser := newAvroParserForBody(t, BodyOptions{IncludeLogRecordOriginal: true}, zap.New(core))
	lr := plog.NewLogRecord()

	// pdata holds an infinite float, and JSON has no way to write one.
	record := map[string]any{"ratio": math.Inf(1)}

	require.NoError(t, parser.AppendLogBody(context.Background(), lr, record))
	_, ok := lr.Attributes().Get(logRecordOriginalAttribute)
	require.False(t, ok, "the attribute is skipped when it cannot be encoded")
	require.Positive(t, logs.FilterMessageSnippet("log.record.original").Len())
}
