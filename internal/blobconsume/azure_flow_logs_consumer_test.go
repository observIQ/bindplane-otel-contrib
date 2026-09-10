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
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// A representative version 4 flow log blob.
const azureFlowLogBlob = `{"records":[{
  "time":"2026-01-01T00:00:00.0000000Z",
  "flowLogGUID":"11111111-2222-3333-4444-555555555555",
  "macAddress":"000D3A123456",
  "category":"FlowLogFlowEvent",
  "flowLogResourceID":"/SUBSCRIPTIONS/SUB-ID/FLOWLOGS/EXAMPLE-FL",
  "targetResourceID":"/subscriptions/sub-id/virtualNetworks/example-vnet",
  "flowLogVersion":4,
  "operationName":"FlowLogFlowEvent",
  "flowRecords":{"flows":[
    {"aclID":"00000000-0000-0000-0000-000000000000","flowGroups":[
      {"rule":"PlatformRule","flowTuples":[
        "1767225601000,10.0.1.4,10.0.0.10,42351,53,17,O,B,NX,0,0,0,0",
        "1767225602000,10.0.1.4,10.0.0.10,42351,53,17,O,E,NX,2,288,2,818"]}]},
    {"aclID":"/subscriptions/sub-id/networkSecurityGroups/example-nsg","flowGroups":[
      {"rule":"DefaultRule_AllowVnetInBound","flowTuples":[
        "1767225603000,10.0.1.5,10.0.1.4,56051,26010,6,I,C,NX,60,6300,60,6960"]}]}
  ]}
}]}`

func allRecords(t *testing.T, sink *consumertest.LogsSink) []plog.LogRecord {
	t.Helper()
	var out []plog.LogRecord
	for _, logs := range sink.AllLogs() {
		for i := 0; i < logs.ResourceLogs().Len(); i++ {
			sls := logs.ResourceLogs().At(i).ScopeLogs()
			for j := 0; j < sls.Len(); j++ {
				lrs := sls.At(j).LogRecords()
				for k := 0; k < lrs.Len(); k++ {
					out = append(out, lrs.At(k))
				}
			}
		}
	}
	return out
}

func bodyStr(t *testing.T, record plog.LogRecord, key string) string {
	t.Helper()
	value, ok := record.Body().Map().Get(key)
	require.True(t, ok, "missing body field %q", key)
	return value.Str()
}

func bodyInt(t *testing.T, record plog.LogRecord, key string) int64 {
	t.Helper()
	value, ok := record.Body().Map().Get(key)
	require.True(t, ok, "missing body field %q", key)
	return value.Int()
}

func Test_azureFlowLogsConsumer_UnrollsTuples(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewAzureFlowLogsConsumer(sink, zap.NewNop())

	require.NoError(t, con.Consume(context.Background(), []byte(azureFlowLogBlob)))

	records := allRecords(t, sink)
	require.Len(t, records, 3)

	first := records[0]
	// Root record fields are preserved on every emitted event.
	require.Equal(t, "FlowLogFlowEvent", bodyStr(t, first, "category"))
	require.Equal(t, "11111111-2222-3333-4444-555555555555", bodyStr(t, first, "flowLogGUID"))
	require.Equal(t, "/subscriptions/sub-id/virtualNetworks/example-vnet", bodyStr(t, first, "targetResourceID"))
	// Root numbers arrive from JSON as doubles and are preserved as-is.
	version, ok := first.Body().Map().Get("flowLogVersion")
	require.True(t, ok)
	require.Equal(t, float64(4), version.Double())
	require.Equal(t, "2026-01-01T00:00:00.0000000Z", bodyStr(t, first, "time"))

	// The nested structure is gone, replaced by the tuple's own fields.
	_, ok = first.Body().Map().Get("flowRecords")
	require.False(t, ok)

	require.Equal(t, "00000000-0000-0000-0000-000000000000", bodyStr(t, first, "aclID"))
	require.Equal(t, "PlatformRule", bodyStr(t, first, "rule"))
	require.Equal(t, "1767225601000,10.0.1.4,10.0.0.10,42351,53,17,O,B,NX,0,0,0,0", bodyStr(t, first, "flowTuple"))
	require.Equal(t, "10.0.1.4", bodyStr(t, first, "sourceAddress"))
	require.Equal(t, "10.0.0.10", bodyStr(t, first, "destinationAddress"))
	require.Equal(t, int64(42351), bodyInt(t, first, "sourcePort"))
	require.Equal(t, int64(53), bodyInt(t, first, "destinationPort"))
	require.Equal(t, int64(17), bodyInt(t, first, "transportProtocol"))
	require.Equal(t, "O", bodyStr(t, first, "deviceDirection"))
	require.Equal(t, "B", bodyStr(t, first, "flowState"))
	require.Equal(t, "NX", bodyStr(t, first, "flowEncryption"))
	require.Equal(t, int64(0), bodyInt(t, first, "packetsSourceToDest"))
	require.Equal(t, int64(0), bodyInt(t, first, "bytesDestToSource"))

	// Timestamp comes from the tuple, not the enclosing record.
	require.Equal(t, pcommon.NewTimestampFromTime(time.UnixMilli(1767225601000)), first.Timestamp())
	require.NotZero(t, first.ObservedTimestamp())

	second := records[1]
	require.Equal(t, "E", bodyStr(t, second, "flowState"))
	require.Equal(t, int64(2), bodyInt(t, second, "packetsSourceToDest"))
	require.Equal(t, int64(288), bodyInt(t, second, "bytesSourceToDest"))
	require.Equal(t, int64(2), bodyInt(t, second, "packetsDestToSource"))
	require.Equal(t, int64(818), bodyInt(t, second, "bytesDestToSource"))

	// The second flow's aclID and rule follow their own tuples.
	third := records[2]
	require.Equal(t, "/subscriptions/sub-id/networkSecurityGroups/example-nsg", bodyStr(t, third, "aclID"))
	require.Equal(t, "DefaultRule_AllowVnetInBound", bodyStr(t, third, "rule"))
	require.Equal(t, "I", bodyStr(t, third, "deviceDirection"))
	require.Equal(t, int64(6960), bodyInt(t, third, "bytesDestToSource"))
}

func Test_azureFlowLogsConsumer_Version2Tuple(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewAzureFlowLogsConsumer(sink, zap.NewNop())

	// Version 2 has no flowEncryption field, and "B" state tuples omit the counters.
	input := []byte(`{"records":[{"time":"2026-05-16T16:00:00Z","flowLogVersion":2,"flowRecords":{"flows":[
	  {"aclID":"acl","flowGroups":[{"rule":"r","flowTuples":[
	    "1747411200000,10.0.0.1,10.0.0.2,1234,443,T,O,E,4,600,3,400",
	    "1747411201000,10.0.0.1,10.0.0.2,1235,443,T,O,B"]}]}]}}]}`)

	require.NoError(t, con.Consume(context.Background(), input))

	records := allRecords(t, sink)
	require.Len(t, records, 2)

	withCounters := records[0]
	require.Equal(t, "T", bodyStr(t, withCounters, "transportProtocol")) // non-numeric protocol stays a string
	require.Equal(t, "E", bodyStr(t, withCounters, "flowState"))
	require.Equal(t, int64(4), bodyInt(t, withCounters, "packetsSourceToDest"))
	require.Equal(t, int64(400), bodyInt(t, withCounters, "bytesDestToSource"))
	_, ok := withCounters.Body().Map().Get("flowEncryption")
	require.False(t, ok)

	noCounters := records[1]
	require.Equal(t, "B", bodyStr(t, noCounters, "flowState"))
	_, ok = noCounters.Body().Map().Get("packetsSourceToDest")
	require.False(t, ok)
	require.Equal(t, pcommon.NewTimestampFromTime(time.UnixMilli(1747411201000)), noCounters.Timestamp())
}

func Test_azureFlowLogsConsumer_RecordWithoutTuples(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewAzureFlowLogsConsumer(sink, zap.NewNop())

	input := []byte(`{"records":[{"time":"2026-05-16T16:00:00Z","category":"FlowLogFlowEvent","flowRecords":{"flows":[]}}]}`)
	require.NoError(t, con.Consume(context.Background(), input))

	records := allRecords(t, sink)
	require.Len(t, records, 1)
	require.Equal(t, "FlowLogFlowEvent", bodyStr(t, records[0], "category"))

	// Falls back to the record's own time when there is no tuple timestamp.
	expected, err := time.Parse(time.RFC3339Nano, "2026-05-16T16:00:00Z")
	require.NoError(t, err)
	require.Equal(t, pcommon.NewTimestampFromTime(expected), records[0].Timestamp())
}

func Test_azureFlowLogsConsumer_MalformedFlowRecordsSkipped(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewAzureFlowLogsConsumer(sink, zap.NewNop())

	input := []byte(`{"records":[
	  {"time":"2026-05-16T16:00:00Z","flowRecords":"not-an-object"},
	  {"time":"2026-05-16T16:00:01Z","flowRecords":{"flows":[{"aclID":"acl","flowGroups":[{"rule":"r","flowTuples":["1747411200000,10.0.0.1,10.0.0.2,1,2,6,O,E,NX,1,2,3,4"]}]}]}}
	]}`)

	require.NoError(t, con.Consume(context.Background(), input))
	records := allRecords(t, sink)
	require.Len(t, records, 1)
	require.Equal(t, "acl", bodyStr(t, records[0], "aclID"))
}

func Test_azureFlowLogsConsumer_EmptyAndInvalid(t *testing.T) {
	sink := &consumertest.LogsSink{}
	con := NewAzureFlowLogsConsumer(sink, zap.NewNop())

	require.NoError(t, con.Consume(context.Background(), []byte(`{"records":[]}`)))
	require.NoError(t, con.Consume(context.Background(), []byte(`{"other":"value"}`)))
	require.Equal(t, 0, sink.LogRecordCount())

	require.Error(t, con.Consume(context.Background(), []byte(`{not json`)))
}
