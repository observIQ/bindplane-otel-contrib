package googlesecopsexporter

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/protos/api"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
)

const windowsEventString = "<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Service Control Manager' Guid='{555908d1-a6d7-4695-8e1e-26931d2012f4}' EventSourceName='Service Control Manager'/><EventID Qualifiers='16384'>7036</EventID><Version>0</Version><Level>4</Level><Task>0</Task><Opcode>0</Opcode><Keywords>0x8080000000000000</Keywords><TimeCreated SystemTime='2024-11-08T18:51:13.504187700Z'/><EventRecordID>3562</EventRecordID><Correlation/><Execution ProcessID='604' ThreadID='4792'/><Channel>System</Channel><Computer>WIN-L6PC55MPB98</Computer><Security/></System><EventData><Data Name='param1'>Print Spooler</Data><Data Name='param2'>stopped</Data><Binary>530070006F006F006C00650072002F0031000000</Binary></EventData></Event>"

func tokenWithLength(length int) []byte {
	charset := "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.IntN(len(charset))]
	}
	return b
}

func mockLogRecord(body string, attributes map[string]any) plog.LogRecord {
	lr := plog.NewLogRecord()
	lr.Body().SetStr(body)
	for k, v := range attributes {
		switch val := v.(type) {
		case string:
			lr.Attributes().PutStr(k, val)
		default:
		}
	}
	return lr
}

func mockLogs(record plog.LogRecord) plog.Logs {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()
	record.CopyTo(sl.LogRecords().AppendEmpty())
	return logs
}

type getRawFieldCase struct {
	name         string
	field        string
	logRecord    plog.LogRecord
	scope        plog.ScopeLogs
	resource     plog.ResourceLogs
	expect       string
	expectErrStr string
}

// Used by tests and benchmarks
var getRawFieldCases = []getRawFieldCase{
	{
		name:  "String body",
		field: bodyField,
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Body().SetStr(windowsEventString)
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   windowsEventString,
	},
	{
		name:  "Empty body",
		field: bodyField,
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Body().SetStr("")
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   "",
	},
	{
		name:  "Map body",
		field: bodyField,
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Body().SetEmptyMap()
			lr.Body().Map().PutStr("param1", "Print Spooler")
			lr.Body().Map().PutStr("param2", "stopped")
			lr.Body().Map().PutStr("binary", "530070006F006F006C00650072002F0031000000")
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   `{"binary":"530070006F006F006C00650072002F0031000000","param1":"Print Spooler","param2":"stopped"}`,
	},
	{
		name:  "Map body field",
		field: "body[\"param1\"]",
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Body().SetEmptyMap()
			lr.Body().Map().PutStr("param1", "Print Spooler")
			lr.Body().Map().PutStr("param2", "stopped")
			lr.Body().Map().PutStr("binary", "530070006F006F006C00650072002F0031000000")
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   "Print Spooler",
	},
	{
		name:  "Map body field missing",
		field: "body[\"missing\"]",
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Body().SetEmptyMap()
			lr.Body().Map().PutStr("param1", "Print Spooler")
			lr.Body().Map().PutStr("param2", "stopped")
			lr.Body().Map().PutStr("binary", "530070006F006F006C00650072002F0031000000")
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   "",
	},
	{
		name:  "Attribute chronicle_log_type",
		field: chronicleLogTypeField,
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Attributes().PutStr("status", "200")
			lr.Attributes().PutStr("log.file.name", "/var/log/containers/agent_agent_ns.log")
			lr.Attributes().PutStr("chronicle_log_type", "MICROSOFT_SQL")
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   "MICROSOFT_SQL",
	},
	{
		name:  "Attribute chronicle_namespace",
		field: chronicleNamespaceField,
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Attributes().PutStr("status", "200")
			lr.Attributes().PutStr("google_secops.log.type", "k8s-container")
			lr.Attributes().PutStr("log.file.name", "/var/log/containers/agent_agent_ns.log")
			lr.Attributes().PutStr("chronicle_log_type", "MICROSOFT_SQL")
			lr.Attributes().PutStr("chronicle_namespace", "test")
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   "test",
	},
	{
		name:  "Attribute log.record.original string",
		field: logRecordOriginalField,
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Attributes().PutStr("status", "200")
			lr.Attributes().PutStr("google_secops.log.type", "k8s-container")
			lr.Attributes().PutStr("log.file.name", "/var/log/containers/agent_agent_ns.log")
			lr.Attributes().PutStr("chronicle_log_type", "MICROSOFT_SQL")
			lr.Attributes().PutStr("chronicle_namespace", "test")
			lr.Attributes().PutStr("log.record.original", windowsEventString)
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   windowsEventString,
	},
	{
		name:  "Attribute log.record.original map",
		field: logRecordOriginalField,
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Attributes().PutStr("status", "200")
			lr.Attributes().PutStr("google_secops.log.type", "k8s-container")
			lr.Attributes().PutStr("log.file.name", "/var/log/containers/agent_agent_ns.log")
			lr.Attributes().PutStr("chronicle_log_type", "MICROSOFT_SQL")
			lr.Attributes().PutStr("chronicle_namespace", "test")
			logRecordOriginal := lr.Attributes().PutEmptyMap("log.record.original")
			logRecordOriginal.PutStr("event_id", "7036")
			logRecordOriginal.PutStr("level", "4")
			logRecordOriginal.PutStr("message", "Print Spooler stopped")
			logRecordOriginal.PutStr("process_id", "604")
			logRecordOriginal.PutStr("source", "Service Control Manager")
			logRecordOriginal.PutStr("thread_id", "4792")
			logRecordOriginal.PutStr("timestamp", "2024-11-08T18:51:13.504187700Z")
			logRecordOriginal.PutStr("user_id", "SYSTEM")
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   `{"event_id":"7036","level":"4","message":"Print Spooler stopped","process_id":"604","source":"Service Control Manager","thread_id":"4792","timestamp":"2024-11-08T18:51:13.504187700Z","user_id":"SYSTEM"}`,
	},
	{
		name:  "Attribute log.record.original missing",
		field: logRecordOriginalField,
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			return lr
		}(),
		scope:        plog.NewScopeLogs(),
		resource:     plog.NewResourceLogs(),
		expect:       "",
		expectErrStr: "",
	},
	{
		name:  "Attribute custom_attribute",
		field: "attributes[\"custom_attribute\"]",
		logRecord: func() plog.LogRecord {
			lr := plog.NewLogRecord()
			lr.Attributes().PutStr("status", "200")
			lr.Attributes().PutStr("google_secops.log.type", "k8s-container")
			lr.Attributes().PutStr("log.file.name", "/var/log/containers/agent_agent_ns.log")
			lr.Attributes().PutStr("chronicle_log_type", "MICROSOFT_SQL")
			lr.Attributes().PutStr("chronicle_namespace", "test")
			lr.Attributes().PutStr("log.record.original", windowsEventString)
			lr.Attributes().PutStr("custom_attribute", "custom_value")
			return lr
		}(),
		scope:    plog.NewScopeLogs(),
		resource: plog.NewResourceLogs(),
		expect:   "custom_value",
	},
}

func Test_getRawField(t *testing.T) {
	for _, tc := range getRawFieldCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &protoMarshaler{}
			m.teleSettings.Logger = zap.NewNop()

			ctx := context.Background()

			rawField, err := m.getRawField(ctx, tc.field, tc.logRecord, tc.scope, tc.resource)
			if tc.expectErrStr != "" {
				require.Contains(t, err.Error(), tc.expectErrStr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.expect, rawField)
		})
	}
}

func Benchmark_getRawField(b *testing.B) {
	m := &protoMarshaler{}
	m.teleSettings.Logger = zap.NewNop()

	ctx := context.Background()

	for _, tc := range getRawFieldCases {
		b.ResetTimer()
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = m.getRawField(ctx, tc.field, tc.logRecord, tc.scope, tc.resource)
			}
		})
	}
}

// smallLog creates a minimal log record
func smallLog() (plog.LogRecord, plog.ScopeLogs, plog.ResourceLogs) {
	logRecord := plog.NewLogRecord()
	scope := plog.NewScopeLogs()
	resource := plog.NewResourceLogs()

	// Small body
	logRecord.Body().SetStr("Application started")

	// Minimal attributes
	logRecord.Attributes().PutStr("chronicle_log_type", "WINEVTLOG")
	logRecord.Attributes().PutStr("chronicle_namespace", "production")
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["env"]`, `{"region":"us-east-1"}`)

	// Minimal resource attributes
	resource.Resource().Attributes().PutStr("host.name", "host-001")

	return logRecord, scope, resource
}

// mediumLog creates a medium-sized log record (equivalent to the original representativeLog)
func mediumLog() (plog.LogRecord, plog.ScopeLogs, plog.ResourceLogs) {
	logRecord := plog.NewLogRecord()
	scope := plog.NewScopeLogs()
	resource := plog.NewResourceLogs()

	// Set body as a map to trigger json.Marshal in getRawField when field is "body"
	logRecord.Body().SetEmptyMap()
	logRecord.Body().Map().PutStr("event_id", "7036")
	logRecord.Body().Map().PutStr("level", "4")
	logRecord.Body().Map().PutStr("message", "Print Spooler stopped")
	logRecord.Body().Map().PutStr("timestamp", "2024-11-08T18:51:13.504187700Z")

	// Add regular attributes
	logRecord.Attributes().PutStr("status", "200")
	logRecord.Attributes().PutStr("log.file.name", "/var/log/containers/agent_agent_ns.log")
	logRecord.Attributes().PutStr("chronicle_log_type", "WINEVTLOG")
	logRecord.Attributes().PutStr("chronicle_namespace", "production")

	// Add ingestion labels - some as JSON strings (to trigger json.Unmarshal) and some as regular strings
	// This JSON string will trigger json.Unmarshal in getRawNestedFields
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["env"]`, `{"region":"us-east-1","cluster":"prod-001"}`)
	// This regular string will not trigger json.Unmarshal
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["team"]`, "platform")
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["service"]`, "api-server")

	// Add resource attributes
	resource.Resource().Attributes().PutStr("host.name", "host-001")
	resource.Resource().Attributes().PutStr("service.name", "chronicle-exporter")

	return logRecord, scope, resource
}

// largeLog creates a large log record with many attributes and a larger body
func largeLog() (plog.LogRecord, plog.ScopeLogs, plog.ResourceLogs) {
	logRecord := plog.NewLogRecord()
	scope := plog.NewScopeLogs()
	resource := plog.NewResourceLogs()

	// Large body with more fields
	logRecord.Body().SetEmptyMap()
	logRecord.Body().Map().PutStr("event_id", "7036")
	logRecord.Body().Map().PutStr("level", "4")
	logRecord.Body().Map().PutStr("message", "Print Spooler stopped")
	logRecord.Body().Map().PutStr("timestamp", "2024-11-08T18:51:13.504187700Z")
	logRecord.Body().Map().PutStr("source", "Service Control Manager")
	logRecord.Body().Map().PutStr("user_id", "SYSTEM")
	logRecord.Body().Map().PutStr("process_id", "604")
	logRecord.Body().Map().PutStr("thread_id", "4792")

	// Many attributes
	logRecord.Attributes().PutStr("status", "200")
	logRecord.Attributes().PutStr("log.file.name", "/var/log/containers/agent_agent_ns.log")
	logRecord.Attributes().PutStr("chronicle_log_type", "WINEVTLOG")
	logRecord.Attributes().PutStr("chronicle_namespace", "production")
	logRecord.Attributes().PutStr("http.method", "POST")
	logRecord.Attributes().PutStr("http.status_code", "200")
	logRecord.Attributes().PutStr("http.url", "/api/v1/logs")
	logRecord.Attributes().PutStr("user_agent", "Mozilla/5.0")
	logRecord.Attributes().PutStr("request_id", "abc123def456")
	logRecord.Attributes().PutStr("duration_ms", "45")

	// Multiple ingestion labels with JSON
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["env"]`, `{"region":"us-east-1","cluster":"prod-001","zone":"us-east-1a"}`)
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["team"]`, "platform")
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["service"]`, "api-server")
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["version"]`, "v1.2.3")
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["deployment"]`, "production")

	// More resource attributes
	resource.Resource().Attributes().PutStr("host.name", "host-001")
	resource.Resource().Attributes().PutStr("service.name", "chronicle-exporter")
	resource.Resource().Attributes().PutStr("service.version", "1.87.4")
	resource.Resource().Attributes().PutStr("os.type", "linux")
	resource.Resource().Attributes().PutStr("os.version", "5.15.0")

	return logRecord, scope, resource
}

// veryLargeLog creates a very large log record with many attributes and a very large body
func veryLargeLog() (plog.LogRecord, plog.ScopeLogs, plog.ResourceLogs) {
	logRecord := plog.NewLogRecord()
	scope := plog.NewScopeLogs()
	resource := plog.NewResourceLogs()

	// Very large body with many fields
	logRecord.Body().SetEmptyMap()
	for i := 0; i < 50; i++ {
		logRecord.Body().Map().PutStr(fmt.Sprintf("field_%d", i), fmt.Sprintf("value_%d_%s", i, strings.Repeat("x", 100)))
	}
	logRecord.Body().Map().PutStr("event_id", "7036")
	logRecord.Body().Map().PutStr("level", "4")
	logRecord.Body().Map().PutStr("message", "Print Spooler stopped with detailed error information")
	logRecord.Body().Map().PutStr("timestamp", "2024-11-08T18:51:13.504187700Z")

	// Many attributes
	logRecord.Attributes().PutStr("status", "200")
	logRecord.Attributes().PutStr("log.file.name", "/var/log/containers/agent_agent_ns.log")
	logRecord.Attributes().PutStr("chronicle_log_type", "WINEVTLOG")
	logRecord.Attributes().PutStr("chronicle_namespace", "production")
	for i := 0; i < 30; i++ {
		logRecord.Attributes().PutStr(fmt.Sprintf("attr_%d", i), fmt.Sprintf("value_%d", i))
	}

	// Multiple ingestion labels with complex JSON
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["env"]`, `{"region":"us-east-1","cluster":"prod-001","zone":"us-east-1a","datacenter":"dc1"}`)
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["team"]`, "platform")
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["service"]`, "api-server")
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["version"]`, "v1.2.3")
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["deployment"]`, "production")
	logRecord.Attributes().PutStr(`chronicle_ingestion_label["metadata"]`, `{"tier":"critical","owner":"team-platform","cost_center":"eng-001"}`)

	// Many resource attributes
	resource.Resource().Attributes().PutStr("host.name", "host-001")
	resource.Resource().Attributes().PutStr("service.name", "chronicle-exporter")
	resource.Resource().Attributes().PutStr("service.version", "1.87.4")
	resource.Resource().Attributes().PutStr("os.type", "linux")
	resource.Resource().Attributes().PutStr("os.version", "5.15.0")
	for i := 0; i < 20; i++ {
		resource.Resource().Attributes().PutStr(fmt.Sprintf("resource_attr_%d", i), fmt.Sprintf("value_%d", i))
	}

	return logRecord, scope, resource
}

func Benchmark_processLogRecord(b *testing.B) {
	logger := zap.NewNop()
	telemSettings := component.TelemetrySettings{
		Logger:        logger,
		MeterProvider: noop.NewMeterProvider(),
	}

	telemetry, err := metadata.NewTelemetryBuilder(telemSettings)
	if err != nil {
		b.Fatalf("Error creating telemetry builder: %v", err)
	}

	cfg := Config{
		CustomerID:            uuid.New().String(),
		RawLogField:           "", // Empty RawLogField forces json.Marshal of entire log record
		BatchRequestSizeLimit: 5242880,
	}

	ctx := context.Background()

	sizes := []struct {
		name string
		log  func() (plog.LogRecord, plog.ScopeLogs, plog.ResourceLogs)
	}{
		{"small", smallLog},
		{"medium", mediumLog},
		{"large", largeLog},
		{"very_large", veryLargeLog},
	}

	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			logRecord, scope, resource := size.log()

			m := &protoMarshaler{
				cfg:          cfg,
				teleSettings: telemSettings,
				telemetry:    telemetry,
				logger:       logger,
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, _, _, _ = m.processLogRecord(ctx, logRecord, scope, resource)
			}
		})
	}
}

func Test_getLogType(t *testing.T) {
	logger := zap.NewNop()
	startTime := time.Now()

	telemSettings := component.TelemetrySettings{
		Logger:        logger,
		MeterProvider: noop.NewMeterProvider(),
	}

	telemetry, err := metadata.NewTelemetryBuilder(telemSettings)
	if err != nil {
		t.Errorf("Error creating telemetry builder: %v", err)
		t.Fail()
	}

	tests := []struct {
		name         string
		cfg          Config
		labels       []*api.Label
		logRecords   func() plog.Logs
		expectedType string
		logTypes     map[string]struct{}
	}{
		{
			name: "Single log record with expected data",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				API:                   chronicleAPI,
				ProjectNumber:         "test-project",
				Location:              "us",
				BatchRequestSizeLimit: 5242880,
			},
			labels: []*api.Label{
				{Key: "env", Value: "prod"},
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Test log message", map[string]any{"google_secops.log.type": "WINEVTLOG", "google_secops.namespace": "test"}))
			},
			expectedType: "WINEVTLOG",
		},
		{
			name: "Single log record with expected data, with validation",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				API:                   chronicleAPI,
				ProjectNumber:         "test-project",
				Location:              "us",
				BatchRequestSizeLimit: 5242880,
				ValidateLogTypes:      true,
			},
			logTypes: map[string]struct{}{
				"WINEVTLOG": {},
			},
			labels: []*api.Label{
				{Key: "env", Value: "prod"},
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Test log message", map[string]any{"google_secops.log.type": "WINEVTLOG", "google_secops.namespace": "test"}))
			},
			expectedType: "WINEVTLOG",
		},
		{
			name: "Single log record with expected data, fails validation",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				RawLogField:           "body",
				API:                   chronicleAPI,
				ProjectNumber:         "test-project",
				Location:              "us",
				BatchRequestSizeLimit: 5242880,
				ValidateLogTypes:      true,
			},
			logTypes: map[string]struct{}{
				"CATCH_ALL": {},
			},
			labels: []*api.Label{
				{Key: "env", Value: "prod"},
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Test log message", map[string]any{"google_secops.log.type": "WINEVTLOG", "google_secops.namespace": "test"}))
			},
			expectedType: "CATCH_ALL",
		},
		{
			name: "Log record with attributes",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "attributes",
				BatchRequestSizeLimit: 5242880,
			},
			labels: []*api.Label{},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("", map[string]any{"key1": "value1", "google_secops.log.type": "WINEVTLOG", "google_secops.namespace": "test", `google_secops.ingestion_label["key1"]`: "value1", `google_secops.ingestion_label["key2"]`: "value2"}))
			},
			expectedType: "WINEVTLOG",
		},
		{
			name: "No log records",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "DEFAULT",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			labels: []*api.Label{},
			logRecords: func() plog.Logs {
				return plog.NewLogs() // No log records added
			},
		},
		{
			name: "Log type in chronicle attribute",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				logs := plog.NewLogs()
				record1 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record1.Body().SetStr("First log message")
				record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS1", "chronicle_namespace": "test1", `google_secops.ingestion_label["key1"]`: "value1", `google_secops.ingestion_label["key2"]`: "value2"})
				return logs
			},
			expectedType: "WINEVTLOGS1",
		},
		{
			name: "google_secops.log.type takes precedence over chronicle_log_type",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "DEFAULT",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Log with both", map[string]any{"google_secops.log.type": "OKTA", "chronicle_log_type": "ASOC_ALERT"}))
			},
			expectedType: "OKTA",
		},
		{
			name: "Log type unset",
			cfg: Config{
				CustomerID:  uuid.New().String(),
				RawLogField: "body",
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Log without logtype", map[string]any{"chronicle_namespace": "test", `chronicle_ingestion_label["realkey1"]`: "realvalue1", `chronicle_ingestion_label["realkey2"]`: "realvalue2"}))
			},
			expectedType: "CATCH_ALL",
		},
		{
			name: "Log type unset, validation",
			cfg: Config{
				CustomerID:       uuid.New().String(),
				RawLogField:      "body",
				ValidateLogTypes: true,
			},
			logTypes: map[string]struct{}{
				"CATCH_ALL": {},
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Log without logtype", map[string]any{"chronicle_namespace": "test", `chronicle_ingestion_label["realkey1"]`: "realvalue1", `chronicle_ingestion_label["realkey2"]`: "realvalue2"}))
			},
			expectedType: "CATCH_ALL",
		},
		{
			name: "Log type set, fails validation",
			cfg: Config{
				CustomerID:       uuid.New().String(),
				RawLogField:      "body",
				ValidateLogTypes: true,
			},
			logTypes: map[string]struct{}{
				"CATCH_ALL": {},
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Log with logtype", map[string]any{"chronicle_log_type": "MISSING_TYPE", "chronicle_namespace": "test", `chronicle_ingestion_label["realkey1"]`: "realvalue1", `chronicle_ingestion_label["realkey2"]`: "realvalue2"}))
			},
			expectedType: "CATCH_ALL",
		},
		{
			name: "Log type configured, fails validation",
			cfg: Config{
				CustomerID:       uuid.New().String(),
				RawLogField:      "body",
				DefaultLogType:   "MISSING_TYPE",
				ValidateLogTypes: true,
			},
			logTypes: map[string]struct{}{
				"CATCH_ALL": {},
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Log with logtype", map[string]any{"chronicle_namespace": "test", `chronicle_ingestion_label["realkey1"]`: "realvalue1", `chronicle_ingestion_label["realkey2"]`: "realvalue2"}))
			},
			expectedType: "CATCH_ALL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marshaler, err := newProtoMarshaler(tt.cfg, component.TelemetrySettings{Logger: logger}, telemetry, logger)
			marshaler.startTime = startTime
			marshaler.logTypes = tt.logTypes
			require.NoError(t, err)

			logs := tt.logRecords()
			for i := 0; i < logs.ResourceLogs().Len(); i++ {
				resourceLogs := logs.ResourceLogs().At(i)
				for j := 0; j < resourceLogs.ScopeLogs().Len(); j++ {
					scopeLogs := resourceLogs.ScopeLogs().At(j)
					for k := 0; k < scopeLogs.LogRecords().Len(); k++ {
						logRecord := scopeLogs.LogRecords().At(k)
						logType, err := marshaler.getLogType(context.Background(), logRecord, scopeLogs, resourceLogs)
						require.NoError(t, err)
						require.Equal(t, tt.expectedType, logType)
					}
				}
			}
		})
	}
}
