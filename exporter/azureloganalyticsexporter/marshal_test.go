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

package azureloganalyticsexporter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

var testTime = time.Date(2023, 1, 2, 3, 4, 5, 6, time.UTC)

func TestTransformLogsToSentinelFormat(t *testing.T) {
	// Create test logs
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()

	// Add resource attributes
	rl.Resource().Attributes().PutStr("service.name", "test-service")
	rl.Resource().Attributes().PutStr("host.name", "test-host")

	// Add scope logs
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("test-scope")
	sl.Scope().SetVersion("v1.0.0")

	// Add log record
	lr := sl.LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC)))
	lr.SetSeverityText("INFO")
	lr.SetSeverityNumber(plog.SeverityNumberInfo)
	lr.Body().SetStr("Test log message")
	lr.Attributes().PutStr("log_type", "test-log")

	// Create logger for tests
	logger := zap.NewNop()

	t.Run("Standard JSON marshaling with string body", func(t *testing.T) {
		// Create config with default settings (no raw log field)
		cfg := &Config{}

		// Create telemetry settings for tests
		telemetrySettings := component.TelemetrySettings{
			Logger: logger,
		}

		// Create marshaler with default config
		marshaler := newMarshaler(cfg, telemetrySettings)

		// Transform logs
		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), logs)
		assert.NoError(t, err)

		// Parse the resulting JSON
		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		// Verify the structure — string body should produce RawData wrapper
		assert.Len(t, result, 1)
		assert.Equal(t, "Test log message", result[0]["RawData"])
		assert.Equal(t, "INFO", result[0]["SeverityText"])
		assert.Equal(t, float64(9), result[0]["SeverityNumber"]) // SeverityNumberInfo == 9
		assert.Equal(t, "2023-01-02T03:04:05Z", result[0]["TimeGenerated"])
		// No TraceId/SpanId expected since they are empty
		assert.NotContains(t, result[0], "TraceId")
		assert.NotContains(t, result[0], "SpanId")
	})
	t.Run("Raw log field transformation using attributes", func(t *testing.T) {
		// Create config with raw log field
		cfg := &Config{
			RawLogField: `attributes`,
		}

		// Create telemetry settings for tests
		telemetrySettings := component.TelemetrySettings{
			Logger: logger,
		}

		// Create marshaler with raw log field config
		marshaler := newMarshaler(cfg, telemetrySettings)

		// Transform logs
		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), logs)
		assert.NoError(t, err)

		// Parse the resulting JSON
		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		// Verify the structure
		assert.Len(t, result, 1)
		assert.Contains(t, result[0], "RawData")

		// Parse the RawData field which should contain the attributes
		var rawData map[string]interface{}
		err = json.Unmarshal([]byte(result[0]["RawData"].(string)), &rawData)
		assert.NoError(t, err)

		// Verify the content
		assert.Contains(t, rawData, "log_type")
		assert.Equal(t, "test-log", rawData["log_type"])
	})

	t.Run("Raw log field transformation using body", func(t *testing.T) {
		// Create config with raw log field set to extract body
		cfg := &Config{
			RawLogField: `body`,
		}

		// Create telemetry settings for tests
		telemetrySettings := component.TelemetrySettings{
			Logger: logger,
		}

		// Create marshaler with raw log field config
		marshaler := newMarshaler(cfg, telemetrySettings)

		// Transform logs
		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), logs)
		assert.NoError(t, err)

		// Parse the resulting JSON
		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		// Verify the structure
		assert.Len(t, result, 1)
		assert.Contains(t, result[0], "RawData")

		// Verify the content is directly the log message (not in JSON format)
		assert.Equal(t, "Test log message", result[0]["RawData"])
	})

	t.Run("Raw log field transformation using custom expression", func(t *testing.T) {
		// Create a custom expression that returns a combination of values
		cfg := &Config{
			RawLogField: `{"message": body, "log_level": severity_text, "hostname": resource.attributes["host.name"]}`,
		}

		// Create telemetry settings for tests
		telemetrySettings := component.TelemetrySettings{
			Logger: logger,
		}

		// Create marshaler with raw log field config
		marshaler := newMarshaler(cfg, telemetrySettings)

		// Transform logs
		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), logs)
		assert.NoError(t, err)

		// Parse the resulting JSON
		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		// Verify the structure
		assert.Len(t, result, 1)
		assert.Contains(t, result[0], "RawData")

		// Parse the RawData field which should contain the custom JSON
		var rawData map[string]interface{}
		err = json.Unmarshal([]byte(result[0]["RawData"].(string)), &rawData)
		assert.NoError(t, err)

		// Verify the content
		assert.Contains(t, rawData, "message")
		assert.Contains(t, rawData, "log_level")
		assert.Contains(t, rawData, "hostname")
		assert.Equal(t, "Test log message", rawData["message"])
		assert.Equal(t, "INFO", rawData["log_level"])
		assert.Equal(t, "test-host", rawData["hostname"])
	})

	t.Run("Structured map body", func(t *testing.T) {
		mapLogs := plog.NewLogs()
		mrl := mapLogs.ResourceLogs().AppendEmpty()
		msl := mrl.ScopeLogs().AppendEmpty()
		mlr := msl.LogRecords().AppendEmpty()
		mlr.SetTimestamp(pcommon.NewTimestampFromTime(testTime))
		mlr.SetSeverityText("WARN")
		mlr.SetSeverityNumber(plog.SeverityNumberWarn)
		body := mlr.Body().SetEmptyMap()
		body.PutStr("source_ip", "10.0.0.1")
		body.PutStr("action", "ALLOW")

		cfg := &Config{}
		telemetrySettings := component.TelemetrySettings{Logger: logger}
		marshaler := newMarshaler(cfg, telemetrySettings)

		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), mapLogs)
		assert.NoError(t, err)

		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		assert.Len(t, result, 1)
		assert.Equal(t, "10.0.0.1", result[0]["source_ip"])
		assert.Equal(t, "ALLOW", result[0]["action"])
		assert.NotContains(t, result[0], "RawData")
		assert.Equal(t, "WARN", result[0]["SeverityText"])
		assert.Contains(t, result[0], "TimeGenerated")
	})

	t.Run("Unstructured string body", func(t *testing.T) {
		strLogs := plog.NewLogs()
		srl := strLogs.ResourceLogs().AppendEmpty()
		ssl := srl.ScopeLogs().AppendEmpty()
		slr := ssl.LogRecords().AppendEmpty()
		slr.SetTimestamp(pcommon.NewTimestampFromTime(testTime))
		slr.SetSeverityText("ERROR")
		slr.SetSeverityNumber(plog.SeverityNumberError)
		slr.Body().SetStr("plain text")

		cfg := &Config{}
		telemetrySettings := component.TelemetrySettings{Logger: logger}
		marshaler := newMarshaler(cfg, telemetrySettings)

		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), strLogs)
		assert.NoError(t, err)

		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		assert.Len(t, result, 1)
		assert.Equal(t, "plain text", result[0]["RawData"])
		assert.Contains(t, result[0], "TimeGenerated")
		assert.Equal(t, "ERROR", result[0]["SeverityText"])
	})

	t.Run("Mixed batch with map and string bodies", func(t *testing.T) {
		mixLogs := plog.NewLogs()
		mrl := mixLogs.ResourceLogs().AppendEmpty()
		msl := mrl.ScopeLogs().AppendEmpty()

		// First record: structured map body
		lr1 := msl.LogRecords().AppendEmpty()
		lr1.SetTimestamp(pcommon.NewTimestampFromTime(testTime))
		lr1.SetSeverityText("INFO")
		lr1.SetSeverityNumber(plog.SeverityNumberInfo)
		b1 := lr1.Body().SetEmptyMap()
		b1.PutStr("key", "value")

		// Second record: unstructured string body
		lr2 := msl.LogRecords().AppendEmpty()
		lr2.SetTimestamp(pcommon.NewTimestampFromTime(testTime))
		lr2.SetSeverityText("DEBUG")
		lr2.SetSeverityNumber(plog.SeverityNumberDebug)
		lr2.Body().SetStr("a string log")

		cfg := &Config{}
		telemetrySettings := component.TelemetrySettings{Logger: logger}
		marshaler := newMarshaler(cfg, telemetrySettings)

		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), mixLogs)
		assert.NoError(t, err)

		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		assert.Len(t, result, 2)

		// First entry: structured — no RawData, top-level key
		assert.Equal(t, "value", result[0]["key"])
		assert.NotContains(t, result[0], "RawData")

		// Second entry: unstructured — RawData present
		assert.Equal(t, "a string log", result[1]["RawData"])
	})

	t.Run("Empty body falls back to RawData with empty string", func(t *testing.T) {
		emptyLogs := plog.NewLogs()
		erl := emptyLogs.ResourceLogs().AppendEmpty()
		esl := erl.ScopeLogs().AppendEmpty()
		elr := esl.LogRecords().AppendEmpty()
		elr.SetTimestamp(pcommon.NewTimestampFromTime(testTime))

		cfg := &Config{}
		telemetrySettings := component.TelemetrySettings{Logger: logger}
		marshaler := newMarshaler(cfg, telemetrySettings)

		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), emptyLogs)
		assert.NoError(t, err)

		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		assert.Len(t, result, 1)
		assert.Equal(t, "", result[0]["RawData"])
	})

	t.Run("Map body with conflicting metadata keys logs warning and overwrites", func(t *testing.T) {
		mapLogs := plog.NewLogs()
		mrl := mapLogs.ResourceLogs().AppendEmpty()
		msl := mrl.ScopeLogs().AppendEmpty()
		mlr := msl.LogRecords().AppendEmpty()
		mlr.SetTimestamp(pcommon.NewTimestampFromTime(testTime))
		mlr.SetSeverityText("WARN")
		mlr.SetSeverityNumber(plog.SeverityNumberWarn)
		body := mlr.Body().SetEmptyMap()
		body.PutStr("SeverityText", "body-severity")
		body.PutStr("TimeGenerated", "body-time")
		body.PutStr("safe_field", "no-conflict")

		cfg := &Config{}
		core, observed := observer.New(zap.WarnLevel)
		telemetrySettings := component.TelemetrySettings{Logger: zap.New(core)}
		marshaler := newMarshaler(cfg, telemetrySettings)

		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), mapLogs)
		assert.NoError(t, err)

		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		assert.Len(t, result, 1)
		entry := result[0]

		// Metadata fields overwrite body values
		assert.Equal(t, "WARN", entry["SeverityText"])
		assert.Equal(t, "2023-01-02T03:04:05Z", entry["TimeGenerated"])

		// Non-conflicting field is untouched
		assert.Equal(t, "no-conflict", entry["safe_field"])

		// Verify warnings were logged for the two conflicting keys
		warnings := observed.FilterMessage("log body map field will be overwritten by metadata").All()
		assert.Len(t, warnings, 2)
		warnKeys := make([]string, len(warnings))
		for i, w := range warnings {
			warnKeys[i] = w.ContextMap()["key"].(string)
		}
		assert.Contains(t, warnKeys, "SeverityText")
		assert.Contains(t, warnKeys, "TimeGenerated")
	})

	t.Run("Empty logs produce empty JSON array not null", func(t *testing.T) {
		emptyLogs := plog.NewLogs()

		cfg := &Config{}
		telemetrySettings := component.TelemetrySettings{Logger: logger}
		marshaler := newMarshaler(cfg, telemetrySettings)

		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), emptyLogs)
		assert.NoError(t, err)
		assert.Equal(t, "[]", string(jsonBytes))
	})

	t.Run("Metadata fields present", func(t *testing.T) {
		metaLogs := plog.NewLogs()
		mrl := metaLogs.ResourceLogs().AppendEmpty()
		msl := mrl.ScopeLogs().AppendEmpty()
		mlr := msl.LogRecords().AppendEmpty()
		mlr.SetTimestamp(pcommon.NewTimestampFromTime(testTime))
		mlr.SetSeverityText("INFO")
		mlr.SetSeverityNumber(plog.SeverityNumberInfo)
		mlr.Body().SetStr("test")

		// Set trace context
		var traceID [16]byte
		traceID[15] = 1
		mlr.SetTraceID(pcommon.TraceID(traceID))
		var spanID [8]byte
		spanID[7] = 2
		mlr.SetSpanID(pcommon.SpanID(spanID))

		cfg := &Config{}
		telemetrySettings := component.TelemetrySettings{Logger: logger}
		marshaler := newMarshaler(cfg, telemetrySettings)

		jsonBytes, err := marshaler.transformLogsToSentinelFormat(context.Background(), metaLogs)
		assert.NoError(t, err)

		var result []map[string]interface{}
		err = json.Unmarshal(jsonBytes, &result)
		assert.NoError(t, err)

		assert.Len(t, result, 1)
		assert.Contains(t, result[0], "TimeGenerated")
		assert.Contains(t, result[0], "SeverityText")
		assert.Contains(t, result[0], "SeverityNumber")
		assert.Equal(t, "INFO", result[0]["SeverityText"])
		assert.Equal(t, float64(9), result[0]["SeverityNumber"])
		assert.Contains(t, result[0], "TraceId")
		assert.Contains(t, result[0], "SpanId")
		assert.Equal(t, "00000000000000000000000000000001", result[0]["TraceId"])
		assert.Equal(t, "0000000000000002", result[0]["SpanId"])
	})
}

// newTestMarshaler returns a marshaler wired with the given config and a nop
// logger, suitable for exercising attribute-resolution helpers.
func newTestMarshaler(cfg *Config) *azureLogAnalyticsMarshaler {
	return newMarshaler(cfg, component.TelemetrySettings{Logger: zap.NewNop()})
}

// newTestTriple builds an empty ResourceLogs/ScopeLogs/LogRecord triple so the
// resolver helpers can be called directly without any extra setup.
func newTestTriple() (plog.ResourceLogs, plog.ScopeLogs, plog.LogRecord) {
	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()
	lr := sl.LogRecords().AppendEmpty()
	return rl, sl, lr
}

func TestGetStreamName(t *testing.T) {
	t.Run("falls back to config when no attribute", func(t *testing.T) {
		m := newTestMarshaler(&Config{StreamName: "Custom-Default"})
		rl, sl, lr := newTestTriple()
		assert.Equal(t, "Custom-Default", m.getStreamName(lr, sl, rl))
	})

	t.Run("uses log record attribute when present", func(t *testing.T) {
		m := newTestMarshaler(&Config{StreamName: "Custom-Default"})
		rl, sl, lr := newTestTriple()
		lr.Attributes().PutStr(sentinelStreamNameAttribute, "Custom-Override")
		assert.Equal(t, "Custom-Override", m.getStreamName(lr, sl, rl))
	})

	t.Run("uses resource attribute when log attribute absent", func(t *testing.T) {
		m := newTestMarshaler(&Config{StreamName: "Custom-Default"})
		rl, sl, lr := newTestTriple()
		rl.Resource().Attributes().PutStr(sentinelStreamNameAttribute, "Custom-Resource")
		assert.Equal(t, "Custom-Resource", m.getStreamName(lr, sl, rl))
	})

	t.Run("log record attribute wins over resource attribute", func(t *testing.T) {
		m := newTestMarshaler(&Config{StreamName: "Custom-Default"})
		rl, sl, lr := newTestTriple()
		rl.Resource().Attributes().PutStr(sentinelStreamNameAttribute, "Custom-Resource")
		lr.Attributes().PutStr(sentinelStreamNameAttribute, "Custom-Record")
		assert.Equal(t, "Custom-Record", m.getStreamName(lr, sl, rl))
	})

	t.Run("empty attribute string falls back to config", func(t *testing.T) {
		m := newTestMarshaler(&Config{StreamName: "Custom-Default"})
		rl, sl, lr := newTestTriple()
		lr.Attributes().PutStr(sentinelStreamNameAttribute, "")
		assert.Equal(t, "Custom-Default", m.getStreamName(lr, sl, rl))
	})

	t.Run("non-string attribute falls back to config", func(t *testing.T) {
		m := newTestMarshaler(&Config{StreamName: "Custom-Default"})
		rl, sl, lr := newTestTriple()
		lr.Attributes().PutInt(sentinelStreamNameAttribute, 42)
		assert.Equal(t, "Custom-Default", m.getStreamName(lr, sl, rl))
	})
}

func TestGetRuleID(t *testing.T) {
	t.Run("falls back to config when no attribute", func(t *testing.T) {
		m := newTestMarshaler(&Config{RuleID: "dcr-default"})
		rl, sl, lr := newTestTriple()
		assert.Equal(t, "dcr-default", m.getRuleID(lr, sl, rl))
	})

	t.Run("uses log record attribute when present", func(t *testing.T) {
		m := newTestMarshaler(&Config{RuleID: "dcr-default"})
		rl, sl, lr := newTestTriple()
		lr.Attributes().PutStr(sentinelRuleIDAttribute, "dcr-record")
		assert.Equal(t, "dcr-record", m.getRuleID(lr, sl, rl))
	})

	t.Run("uses resource attribute when log attribute absent", func(t *testing.T) {
		m := newTestMarshaler(&Config{RuleID: "dcr-default"})
		rl, sl, lr := newTestTriple()
		rl.Resource().Attributes().PutStr(sentinelRuleIDAttribute, "dcr-resource")
		assert.Equal(t, "dcr-resource", m.getRuleID(lr, sl, rl))
	})

	t.Run("log record attribute wins over resource attribute", func(t *testing.T) {
		m := newTestMarshaler(&Config{RuleID: "dcr-default"})
		rl, sl, lr := newTestTriple()
		rl.Resource().Attributes().PutStr(sentinelRuleIDAttribute, "dcr-resource")
		lr.Attributes().PutStr(sentinelRuleIDAttribute, "dcr-record")
		assert.Equal(t, "dcr-record", m.getRuleID(lr, sl, rl))
	})

	t.Run("empty attribute string falls back to config", func(t *testing.T) {
		m := newTestMarshaler(&Config{RuleID: "dcr-default"})
		rl, sl, lr := newTestTriple()
		lr.Attributes().PutStr(sentinelRuleIDAttribute, "")
		assert.Equal(t, "dcr-default", m.getRuleID(lr, sl, rl))
	})
}

func TestLookupStringAttr(t *testing.T) {
	attrs := pcommon.NewMap()
	attrs.PutStr("str", "value")
	attrs.PutInt("num", 1)

	v, ok := lookupStringAttr(attrs, "str")
	require.True(t, ok)
	assert.Equal(t, "value", v)

	_, ok = lookupStringAttr(attrs, "num")
	assert.False(t, ok, "non-string attributes should report not found")

	_, ok = lookupStringAttr(attrs, "missing")
	assert.False(t, ok)
}
