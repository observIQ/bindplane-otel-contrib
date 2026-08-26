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

func TestJSONAppendLogBody_RawEmitsOriginalBytes(t *testing.T) {
	t.Parallel()

	parser := newJSONParserForBody(t, BodyOptions{Raw: true})
	lr := plog.NewLogRecord()

	err := parser.AppendLogBody(context.Background(), lr, json.RawMessage(`{"host":"a","msg":"first"}`))
	require.NoError(t, err)
	require.Equal(t, `{"host":"a","msg":"first"}`, lr.Body().Str(),
		"raw mode emits this record's original bytes as the body, not a parsed structure")
}

func TestJSONAppendLogBody_RawWithIncludeOriginalSetsBoth(t *testing.T) {
	t.Parallel()

	parser := newJSONParserForBody(t, BodyOptions{Raw: true, IncludeLogRecordOriginal: true})
	lr := plog.NewLogRecord()

	err := parser.AppendLogBody(context.Background(), lr, json.RawMessage(`{"host":"a"}`))
	require.NoError(t, err)
	require.Equal(t, `{"host":"a"}`, lr.Body().Str())
	orig, ok := lr.Attributes().Get(logRecordOriginalAttribute)
	require.True(t, ok, "include_log_record_original records the original in raw mode too")
	require.Equal(t, `{"host":"a"}`, orig.Str())
}
