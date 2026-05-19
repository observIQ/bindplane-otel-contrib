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
	"time"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// RecordsJSONLogsConsumer consumes a single JSON document containing a
// top-level "records" array (the format used by Azure NSG flow logs and
// several Azure diagnostic-settings exports) and emits one log record per
// element.
type RecordsJSONLogsConsumer struct {
	nextConsumer consumer.Logs
	logger       *zap.Logger
}

// NewRecordsJSONLogsConsumer creates a new records-json logs consumer.
func NewRecordsJSONLogsConsumer(nextConsumer consumer.Logs, logger *zap.Logger) *RecordsJSONLogsConsumer {
	return &RecordsJSONLogsConsumer{
		nextConsumer: nextConsumer,
		logger:       logger,
	}
}

// Consume parses entityContent as a JSON object with a "records" array and
// emits one log record per element.
func (r *RecordsJSONLogsConsumer) Consume(ctx context.Context, entityContent []byte) error {
	var envelope struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(entityContent, &envelope); err != nil {
		return fmt.Errorf("records-json consume: unmarshal: %w", err)
	}

	if len(envelope.Records) == 0 {
		r.logger.Debug("records-json blob contained no records")
		return nil
	}

	logs := plog.NewLogs()
	resourceLogs := logs.ResourceLogs().AppendEmpty()
	scopeLogs := resourceLogs.ScopeLogs().AppendEmpty()
	logRecords := scopeLogs.LogRecords()

	now := pcommon.NewTimestampFromTime(time.Now())
	skipped := 0

	for i, rec := range envelope.Records {
		body := pcommon.NewMap()
		if err := body.FromRaw(rec); err != nil {
			r.logger.Warn("Skipping record, failed to set body",
				zap.Int("record_index", i),
				zap.Error(err))
			skipped++
			continue
		}

		record := logRecords.AppendEmpty()
		record.SetObservedTimestamp(now)
		body.CopyTo(record.Body().SetEmptyMap())
	}

	if skipped > 0 {
		r.logger.Warn("Skipped malformed records during records-json parsing", zap.Int("skipped_count", skipped))
	}

	if logRecords.Len() == 0 {
		return nil
	}

	if err := r.nextConsumer.ConsumeLogs(ctx, logs); err != nil {
		return fmt.Errorf("records-json consume: %w", err)
	}
	return nil
}
