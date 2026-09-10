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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// flowRecordsKey is the record field holding the nested flow structure that is unrolled.
const flowRecordsKey = "flowRecords"

// AzureFlowLogsConsumer consumes a single JSON document containing a top-level
// "records" array of Azure VNet/NSG flow logs and unrolls the nested
// flowRecords.flows[].flowGroups[].flowTuples[] structure, emitting one log
// record per flow tuple. Every emitted record carries the root record's fields
// (minus flowRecords), the owning flow's aclID, the owning flow group's rule,
// the raw tuple string, and the tuple's parsed fields.
type AzureFlowLogsConsumer struct {
	nextConsumer consumer.Logs
	logger       *zap.Logger
}

// NewAzureFlowLogsConsumer creates a new Azure flow logs consumer.
func NewAzureFlowLogsConsumer(nextConsumer consumer.Logs, logger *zap.Logger) *AzureFlowLogsConsumer {
	return &AzureFlowLogsConsumer{
		nextConsumer: nextConsumer,
		logger:       logger,
	}
}

// azureFlowLogEnvelope is the on-the-wire shape of a flow log blob. The record
// fields are kept as a raw map so that fields Azure adds later are preserved
// without a code change; only the nested flow structure is typed.
type azureFlowLogEnvelope struct {
	Records []map[string]any `json:"records"`
}

type azureFlowRecords struct {
	Flows []struct {
		ACLID      string `json:"aclID"`
		FlowGroups []struct {
			Rule       string   `json:"rule"`
			FlowTuples []string `json:"flowTuples"`
		} `json:"flowGroups"`
	} `json:"flows"`
}

// Consume parses entityContent and emits one log record per flow tuple.
func (a *AzureFlowLogsConsumer) Consume(ctx context.Context, entityContent []byte) error {
	var envelope azureFlowLogEnvelope
	if err := json.Unmarshal(entityContent, &envelope); err != nil {
		return fmt.Errorf("azure-flow-logs consume: unmarshal: %w", err)
	}

	if len(envelope.Records) == 0 {
		a.logger.Debug("azure-flow-logs blob contained no records")
		return nil
	}

	logs := plog.NewLogs()
	scopeLogs := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	logRecords := scopeLogs.LogRecords()

	now := pcommon.NewTimestampFromTime(time.Now())
	skipped := 0

	for i, rec := range envelope.Records {
		flows, err := decodeFlowRecords(rec[flowRecordsKey])
		if err != nil {
			a.logger.Warn("Skipping record, failed to decode flowRecords",
				zap.Int("record_index", i),
				zap.Error(err))
			skipped++
			continue
		}

		// Root fields shared by every tuple emitted from this record.
		root := make(map[string]any, len(rec))
		for k, v := range rec {
			if k == flowRecordsKey {
				continue
			}
			root[k] = v
		}

		rootBody := pcommon.NewMap()
		if err := rootBody.FromRaw(root); err != nil {
			a.logger.Warn("Skipping record, failed to set body from record fields",
				zap.Int("record_index", i),
				zap.Error(err))
			skipped++
			continue
		}
		recordTime := recordTimestamp(root)

		emitted := 0
		for _, flow := range flows.Flows {
			for _, group := range flow.FlowGroups {
				for _, tuple := range group.FlowTuples {
					record := logRecords.AppendEmpty()
					record.SetObservedTimestamp(now)

					body := record.Body().SetEmptyMap()
					rootBody.CopyTo(body)
					body.PutStr("aclID", flow.ACLID)
					body.PutStr("rule", group.Rule)
					body.PutStr("flowTuple", tuple)

					switch tupleTime := parseFlowTuple(body, tuple); {
					case tupleTime != 0:
						record.SetTimestamp(tupleTime)
					case recordTime != 0:
						record.SetTimestamp(recordTime)
					default:
						record.SetTimestamp(now)
					}
					emitted++
				}
			}
		}

		// A record with no tuples still carries the flow log metadata, so emit
		// it rather than dropping it silently.
		if emitted == 0 {
			record := logRecords.AppendEmpty()
			record.SetObservedTimestamp(now)
			rootBody.CopyTo(record.Body().SetEmptyMap())
			if recordTime != 0 {
				record.SetTimestamp(recordTime)
			} else {
				record.SetTimestamp(now)
			}
		}
	}

	if skipped > 0 {
		a.logger.Warn("Skipped malformed records during azure-flow-logs parsing", zap.Int("skipped_count", skipped))
	}

	if logRecords.Len() == 0 {
		return nil
	}

	if err := a.nextConsumer.ConsumeLogs(ctx, logs); err != nil {
		return fmt.Errorf("azure-flow-logs consume: %w", err)
	}
	return nil
}

// decodeFlowRecords re-encodes the raw flowRecords value into the typed shape.
// A record without a flowRecords field decodes to zero flows, not an error.
func decodeFlowRecords(raw any) (azureFlowRecords, error) {
	var flows azureFlowRecords
	if raw == nil {
		return flows, nil
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return flows, fmt.Errorf("marshal flowRecords: %w", err)
	}
	if err := json.Unmarshal(encoded, &flows); err != nil {
		return flows, fmt.Errorf("unmarshal flowRecords: %w", err)
	}
	return flows, nil
}

// recordTimestamp returns the root record's "time" field as a timestamp, or 0
// if it is absent or unparseable.
func recordTimestamp(root map[string]any) pcommon.Timestamp {
	timeStr, ok := root["time"].(string)
	if !ok {
		return 0
	}

	parsed, err := time.Parse(time.RFC3339Nano, timeStr)
	if err != nil {
		return 0
	}
	return pcommon.NewTimestampFromTime(parsed)
}

// flowTupleFields are the fixed leading fields of a flow tuple, in order.
var flowTupleFields = []string{
	"flowTimestamp",
	"sourceAddress",
	"destinationAddress",
	"sourcePort",
	"destinationPort",
	"transportProtocol",
	"deviceDirection",
	"flowState",
}

// flowTupleCounters are the trailing counter fields, in order. They are absent
// on tuples in the "B" (begin) state written by older flow log versions.
var flowTupleCounters = []string{
	"packetsSourceToDest",
	"bytesSourceToDest",
	"packetsDestToSource",
	"bytesDestToSource",
}

// flowTupleNumericFields are the tuple fields emitted as integers rather than strings.
var flowTupleNumericFields = map[string]bool{
	"sourcePort":        true,
	"destinationPort":   true,
	"transportProtocol": true,
}

// parseFlowTuple splits a comma-separated flow tuple into named body fields and
// returns the tuple's timestamp, or 0 if it could not be parsed. Fields beyond
// the known layout are preserved under flowTupleExtra so nothing is lost when
// Azure extends the format.
func parseFlowTuple(body pcommon.Map, tuple string) pcommon.Timestamp {
	parts := strings.Split(tuple, ",")

	for i, name := range flowTupleFields {
		if i >= len(parts) {
			return 0
		}
		value := parts[i]
		if name == "flowTimestamp" {
			continue // handled below as the record timestamp
		}
		if flowTupleNumericFields[name] {
			if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
				body.PutInt(name, parsed)
				continue
			}
		}
		body.PutStr(name, value)
	}

	// Version 4 inserts flowEncryption between the flow state and the counters;
	// version 2 goes straight to the counters. Tell them apart by whether the
	// field parses as a number.
	rest := parts[len(flowTupleFields):]
	if len(rest) > 0 {
		if _, err := strconv.ParseInt(rest[0], 10, 64); err != nil {
			body.PutStr("flowEncryption", rest[0])
			rest = rest[1:]
		}
	}

	for i, name := range flowTupleCounters {
		if i >= len(rest) {
			break
		}
		if parsed, err := strconv.ParseInt(rest[i], 10, 64); err == nil {
			body.PutInt(name, parsed)
			continue
		}
		body.PutStr(name, rest[i])
	}

	if len(rest) > len(flowTupleCounters) {
		extra := body.PutEmptySlice("flowTupleExtra")
		for _, value := range rest[len(flowTupleCounters):] {
			extra.AppendEmpty().SetStr(value)
		}
	}

	epochMillis, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0
	}
	body.PutInt("flowTimestamp", epochMillis)
	return pcommon.NewTimestampFromTime(time.UnixMilli(epochMillis))
}
