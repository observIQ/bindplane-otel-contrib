// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package restapireceiver

import (
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// logRecordOriginalAttribute holds the original text. It matches the attribute
// Bindplane generates for stanza-based sources.
const logRecordOriginalAttribute = "log.record.original"

// convertJSONToLogs converts an array of JSON objects to plog.Logs.
// Each JSON object becomes one log record.
//
// originals holds each record's exact response bytes, positionally aligned with
// data. It is nil when neither body option is enabled, and also when the
// receiver could not recover the original text for the page; in that case both
// options degrade to the parsed-map body rather than emitting a re-encoded
// approximation of what the server sent.
func convertJSONToLogs(data []map[string]any, originals [][]byte, cfg *Config, logger *zap.Logger) plog.Logs {
	logs := plog.NewLogs()
	resourceLogs := logs.ResourceLogs().AppendEmpty()
	scopeLogs := resourceLogs.ScopeLogs().AppendEmpty()

	now := pcommon.NewTimestampFromTime(time.Now())

	for i, item := range data {
		logRecord := scopeLogs.LogRecords().AppendEmpty()

		// Set observed timestamp
		logRecord.SetObservedTimestamp(now)

		var original string
		if i < len(originals) {
			original = string(originals[i])
		}

		// Set the body: the original text when raw mode has it, the parsed map
		// otherwise.
		if cfg.Raw && original != "" {
			logRecord.Body().SetStr(original)
		} else if err := logRecord.Body().SetEmptyMap().FromRaw(item); err != nil {
			logger.Warn("unable to set log body", zap.Error(err))
			// Drop the log record
			scopeLogs.LogRecords().RemoveIf(func(lr plog.LogRecord) bool {
				return lr.Body().Equal(logRecord.Body())
			})
			continue
		}

		if cfg.IncludeLogRecordOriginal && original != "" {
			logRecord.Attributes().PutStr(logRecordOriginalAttribute, original)
		}

		// Try to extract timestamp from common field names. This reads the parsed
		// record, so raw mode keeps the same timestamps as a parsed body.
		timestamp := extractTimestamp(item)
		if timestamp > 0 {
			logRecord.SetTimestamp(timestamp)
		} else {
			// Use observed timestamp as fallback
			logRecord.SetTimestamp(now)
		}
	}

	return logs
}

// extractTimestamp attempts to extract a timestamp from the JSON object.
// It checks common field names like "timestamp", "time", "created_at", etc.
func extractTimestamp(item map[string]any) pcommon.Timestamp {
	// Common timestamp field names
	timestampFields := []string{"timestamp", "time", "created_at", "createdAt", "date", "datetime", "@timestamp"}

	for _, fieldName := range timestampFields {
		if val, exists := item[fieldName]; exists {
			if timestamp := parseTimestamp(val); timestamp > 0 {
				return timestamp
			}
		}
	}

	return 0
}

// parseTimestamp attempts to parse a timestamp from various formats.
func parseTimestamp(val any) pcommon.Timestamp {
	switch v := val.(type) {
	case string:
		// Try RFC3339 first
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return pcommon.NewTimestampFromTime(t)
		}
		// Try RFC3339Nano
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return pcommon.NewTimestampFromTime(t)
		}
		// Try Unix timestamp as string
		if t, err := time.Parse(time.UnixDate, v); err == nil {
			return pcommon.NewTimestampFromTime(t)
		}

	case float64:
		// Unix timestamp in seconds
		if v > 0 {
			return pcommon.NewTimestampFromTime(time.Unix(int64(v), 0))
		}

	case int64:
		// Unix timestamp in seconds
		if v > 0 {
			return pcommon.NewTimestampFromTime(time.Unix(v, 0))
		}

	case int:
		// Unix timestamp in seconds
		if v > 0 {
			return pcommon.NewTimestampFromTime(time.Unix(int64(v), 0))
		}
	}

	return 0
}
