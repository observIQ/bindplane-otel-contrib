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
)

// newJSONParserForBody builds a parser directly, so each AppendLogBody branch can
// be driven without a full stream.
func newJSONParserForBody(t *testing.T, opts BodyOptions) LogParser {
	t.Helper()
	reader := NewBufferedReader(strings.NewReader("[]"), 4096)
	return NewJSONParser(reader, opts)
}

func TestJSONAppendLogBody_RejectsForeignRecordType(t *testing.T) {
	t.Parallel()

	parser := newJSONParserForBody(t, BodyOptions{})
	lr := plog.NewLogRecord()

	err := parser.AppendLogBody(context.Background(), lr, "not a json record")
	require.ErrorContains(t, err, "expected json record, got string")
}

func TestJSONAppendLogBody_RejectsMalformedRecordBytes(t *testing.T) {
	t.Parallel()

	parser := newJSONParserForBody(t, BodyOptions{})
	lr := plog.NewLogRecord()

	err := parser.AppendLogBody(context.Background(), lr, json.RawMessage(`{"broken"`))
	require.ErrorContains(t, err, "decode record body")
}

// TestJSONAppendLogBody_RawEmitsOriginalBytes covers the raw branch of the JSON
// parser. NewRecordProducer routes raw mode to the line parser, so this branch only
// runs for a caller that builds a JSON parser directly. The assertion pins the
// contract of the exported constructor.
func TestJSONAppendLogBody_RawEmitsOriginalBytes(t *testing.T) {
	t.Parallel()

	parser := newJSONParserForBody(t, BodyOptions{Raw: true, IncludeLogRecordOriginal: true})
	lr := plog.NewLogRecord()
	record := json.RawMessage(`{"level":"warn","msg":"disk full"}`)

	require.NoError(t, parser.AppendLogBody(context.Background(), lr, record))
	require.Equal(t, string(record), lr.Body().Str())

	original, ok := lr.Attributes().Get(logRecordOriginalAttribute)
	require.True(t, ok)
	require.Equal(t, string(record), original.Str())
}
