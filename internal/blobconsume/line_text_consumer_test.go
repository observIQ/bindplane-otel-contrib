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

package blobconsume //import "github.com/observiq/bindplane-otel-contrib/internal/blobconsume"

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
)

func Test_lineTextLogsConsumer(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewLineTextLogsConsumer(sink)

	// Blank and whitespace-only lines are skipped; each remaining line is its own record.
	input := "line one\n\nline two\n   \nline three"
	consumed, emitted, err := con.ConsumeCounted(context.Background(), []byte(input))
	require.NoError(t, err)
	require.Equal(t, 3, consumed, "three non-empty lines consumed")
	require.Equal(t, 3, emitted, "and all three emitted (text has no malformed drop)")

	require.Equal(t, 3, sink.LogRecordCount())
	records := sink.AllLogs()[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, "line one", records.At(0).Body().Str())
	require.Equal(t, "line two", records.At(1).Body().Str())
	require.Equal(t, "line three", records.At(2).Body().Str())
	for i := 0; i < records.Len(); i++ {
		require.NotZero(t, records.At(i).ObservedTimestamp(), "each record gets an observed timestamp")
	}
}

func Test_lineTextLogsConsumer_CRLF(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewLineTextLogsConsumer(sink)

	_, emitted, err := con.ConsumeCounted(context.Background(), []byte("line one\r\nline two\r\n"))
	require.NoError(t, err)
	require.Equal(t, 2, emitted)

	require.Equal(t, 2, sink.LogRecordCount())
	records := sink.AllLogs()[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, "line one", records.At(0).Body().Str(), "trailing CR is stripped")
	require.Equal(t, "line two", records.At(1).Body().Str())
}

func Test_lineTextLogsConsumer_EmptyContent(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewLineTextLogsConsumer(sink)

	for _, in := range []string{"", "\n  \n\n", "\r\n"} {
		consumed, emitted, err := con.ConsumeCounted(context.Background(), []byte(in))
		require.NoError(t, err)
		require.Zero(t, consumed)
		require.Zero(t, emitted)
	}
	require.Equal(t, 0, sink.LogRecordCount())
}

func Test_lineTextLogsConsumer_DownstreamError(t *testing.T) {
	con := NewLineTextLogsConsumer(consumertest.NewErr(errors.New("boom")))

	_, _, err := con.ConsumeCounted(context.Background(), []byte("a line"))
	require.ErrorContains(t, err, "line text consume")
	require.ErrorIs(t, err, ErrDownstream)
}
