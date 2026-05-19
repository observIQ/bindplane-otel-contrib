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
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap"
)

func Test_recordsJSONLogsConsumer_MultipleRecords(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewRecordsJSONLogsConsumer(sink, zap.NewNop())

	// Simplified shape of an Azure NSG flow log blob.
	input := []byte(`{
	  "records": [
	    {"time":"2026-05-16T16:00:00Z","category":"NetworkSecurityGroupFlowEvent","resourceId":"/X/Y","properties":{"Version":2}},
	    {"time":"2026-05-16T16:01:00Z","category":"NetworkSecurityGroupFlowEvent","resourceId":"/X/Y","properties":{"Version":2}}
	  ]
	}`)

	err := con.Consume(context.Background(), input)
	require.NoError(t, err)

	require.Equal(t, 2, sink.LogRecordCount())
	first := sink.AllLogs()[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	timeVal, ok := first.Body().Map().Get("time")
	require.True(t, ok)
	require.Equal(t, "2026-05-16T16:00:00Z", timeVal.Str())
}

func Test_recordsJSONLogsConsumer_EmptyRecords(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewRecordsJSONLogsConsumer(sink, zap.NewNop())

	input := []byte(`{"records": []}`)
	err := con.Consume(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 0, sink.LogRecordCount())
}

func Test_recordsJSONLogsConsumer_MissingRecordsKey(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewRecordsJSONLogsConsumer(sink, zap.NewNop())

	// Document without a top-level "records" key — treated as zero records.
	input := []byte(`{"some_other_key": [1,2,3]}`)
	err := con.Consume(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 0, sink.LogRecordCount())
}

func Test_recordsJSONLogsConsumer_InvalidJSON(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewRecordsJSONLogsConsumer(sink, zap.NewNop())

	input := []byte(`not json`)
	err := con.Consume(context.Background(), input)
	require.Error(t, err)
	require.Equal(t, 0, sink.LogRecordCount())
}
