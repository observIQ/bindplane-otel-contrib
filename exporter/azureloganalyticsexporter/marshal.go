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

package azureloganalyticsexporter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottllog"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"go.uber.org/zap"
)

// Attribute names for per-record routing. When set on the log record (or
// resource) these override the exporter's configured StreamName / RuleID.
const (
	sentinelStreamNameAttribute = "sentinel_stream_name"
	sentinelRuleIDAttribute     = "sentinel_rule_id"
)

// azureLogAnalyticsMarshaler handles transforming logs for Azure Log Analytics
type azureLogAnalyticsMarshaler struct {
	cfg          *Config
	teleSettings component.TelemetrySettings
	logger       *zap.Logger
}

// newMarshaler creates a new instance of the azureLogAnalyticsMarshaler
func newMarshaler(cfg *Config, teleSettings component.TelemetrySettings) *azureLogAnalyticsMarshaler {
	return &azureLogAnalyticsMarshaler{
		cfg:          cfg,
		teleSettings: teleSettings,
		logger:       teleSettings.Logger,
	}
}

// getRawField extracts a field value using OTTL expression
func (m *azureLogAnalyticsMarshaler) getRawField(ctx context.Context, field string, logRecord plog.LogRecord, scope plog.ScopeLogs, resource plog.ResourceLogs) (string, error) {
	lrExpr, err := expr.NewOTTLLogRecordExpression(field, m.teleSettings)
	if err != nil {
		return "", fmt.Errorf("raw_log_field is invalid: %s", err)
	}
	tCtx := ottllog.NewTransformContextPtr(resource, scope, logRecord)

	lrExprResult, err := lrExpr.Execute(ctx, tCtx)
	if err != nil {
		return "", fmt.Errorf("execute log record expression: %w", err)
	}

	if lrExprResult == nil {
		return "", nil
	}

	switch result := lrExprResult.(type) {
	case string:
		return result, nil
	case pcommon.Map:
		bytes, err := json.Marshal(result.AsRaw())
		if err != nil {
			return "", fmt.Errorf("marshal log record expression result: %w", err)
		}
		return string(bytes), nil
	default:
		return "", fmt.Errorf("unsupported log record expression result type: %T", lrExprResult)
	}
}

// lookupStringAttr returns the string value for the given key on the attribute
// map. Non-string values and missing keys return ("", false).
func lookupStringAttr(attrs pcommon.Map, key string) (string, bool) {
	v, ok := attrs.Get(key)
	if !ok {
		return "", false
	}
	if v.Type() != pcommon.ValueTypeStr {
		return "", false
	}
	return v.Str(), true
}

// getStreamName resolves the DCR stream name for a log record using the
// following precedence: log record attribute, resource attribute, config. An
// empty attribute value is treated as unset and falls through to the next
// source.
func (m *azureLogAnalyticsMarshaler) getStreamName(logRecord plog.LogRecord, _ plog.ScopeLogs, resourceLog plog.ResourceLogs) string {
	if v, ok := lookupStringAttr(logRecord.Attributes(), sentinelStreamNameAttribute); ok && v != "" {
		return v
	}
	if v, ok := lookupStringAttr(resourceLog.Resource().Attributes(), sentinelStreamNameAttribute); ok && v != "" {
		return v
	}
	return m.cfg.StreamName
}

// getRuleID resolves the DCR rule ID for a log record using the following
// precedence: log record attribute, resource attribute, config. An empty
// attribute value is treated as unset and falls through to the next source.
func (m *azureLogAnalyticsMarshaler) getRuleID(logRecord plog.LogRecord, _ plog.ScopeLogs, resourceLog plog.ResourceLogs) string {
	if v, ok := lookupStringAttr(logRecord.Attributes(), sentinelRuleIDAttribute); ok && v != "" {
		return v
	}
	if v, ok := lookupStringAttr(resourceLog.Resource().Attributes(), sentinelRuleIDAttribute); ok && v != "" {
		return v
	}
	return m.cfg.RuleID
}

// transformLogsToSentinelFormat transforms logs to Microsoft Sentinel format
func (m *azureLogAnalyticsMarshaler) transformLogsToSentinelFormat(ctx context.Context, ld plog.Logs) ([]byte, error) {
	// Check if we're using raw log mode
	if m.cfg.RawLogField != "" {
		return m.transformRawLogsToAzureLogAnalyticsFormat(ctx, ld)
	}
	outputSlice := make([]map[string]interface{}, 0)

	// reservedKeys are metadata fields set by the exporter. If a map body
	// contains any of these keys, the body value will be overwritten by
	// the metadata value.
	reservedKeys := []string{"TimeGenerated", "SeverityText", "SeverityNumber", "TraceId", "SpanId"}

	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		resourceLog := ld.ResourceLogs().At(i)

		for j := 0; j < resourceLog.ScopeLogs().Len(); j++ {
			scopeLog := resourceLog.ScopeLogs().At(j)

			for k := 0; k < scopeLog.LogRecords().Len(); k++ {
				logRecord := scopeLog.LogRecords().At(k)

				var entry map[string]interface{}

				if logRecord.Body().Type() == pcommon.ValueTypeMap {
					entry = logRecord.Body().Map().AsRaw()
					// Warn if any body fields will be overwritten by metadata
					for _, rk := range reservedKeys {
						if _, ok := entry[rk]; ok {
							m.logger.Warn("log body map field will be overwritten by metadata",
								zap.String("key", rk))
						}
					}
				} else {
					entry = map[string]interface{}{
						"RawData": logRecord.Body().AsString(),
					}
				}

				// TimeGenerated
				ts := logRecord.Timestamp().AsTime()
				if ts.IsZero() {
					ts = logRecord.ObservedTimestamp().AsTime()
				}
				if ts.IsZero() {
					ts = time.Now()
				}
				entry["TimeGenerated"] = ts.Format(time.RFC3339)

				// Severity
				entry["SeverityText"] = logRecord.SeverityText()
				entry["SeverityNumber"] = int32(logRecord.SeverityNumber())

				// Trace context (only if non-empty)
				if traceID := logRecord.TraceID().String(); traceID != "" && traceID != "00000000000000000000000000000000" {
					entry["TraceId"] = traceID
				}
				if spanID := logRecord.SpanID().String(); spanID != "" && spanID != "0000000000000000" {
					entry["SpanId"] = spanID
				}

				outputSlice = append(outputSlice, entry)
			}
		}
	}

	jsonData, err := json.Marshal(outputSlice)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return jsonData, nil
}

// transformRawLogsToAzureLogAnalyticsFormat transforms logs to Azure Log Analytics format using the raw log approach
func (m *azureLogAnalyticsMarshaler) transformRawLogsToAzureLogAnalyticsFormat(ctx context.Context, ld plog.Logs) ([]byte, error) {
	azureLogAnalyticsLogs := make([]map[string]interface{}, 0)

	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		resourceLog := ld.ResourceLogs().At(i)

		for j := 0; j < resourceLog.ScopeLogs().Len(); j++ {
			scopeLog := resourceLog.ScopeLogs().At(j)

			for k := 0; k < scopeLog.LogRecords().Len(); k++ {
				logRecord := scopeLog.LogRecords().At(k)

				// Escape any unescaped newlines (if body is a string)
				logBody := logRecord.Body()
				if logBody.Type() == pcommon.ValueTypeStr {
					logBody.SetStr(strings.ReplaceAll(logBody.AsString(), "\n", "\\n"))
				}

				// Extract raw log using the getRawField method
				rawLogStr, err := m.getRawField(ctx, m.cfg.RawLogField, logRecord, scopeLog, resourceLog)
				if err != nil {
					m.logger.Error("Error extracting raw log", zap.Error(err))
					continue
				}

				azureLogAnalyticsLogs = append(azureLogAnalyticsLogs, map[string]interface{}{
					"RawData": rawLogStr,
				})

			}
		}
	}

	jsonLogs, err := json.Marshal(azureLogAnalyticsLogs)
	if err != nil {
		return nil, fmt.Errorf("failed to convert logs to JSON: %w", err)
	}
	return jsonLogs, nil
}
