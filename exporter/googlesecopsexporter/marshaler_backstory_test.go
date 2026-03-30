package googlesecopsexporter

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/protos/api"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProtoMarshaler_MarshalBackstoryRawLogs(t *testing.T) {
	logger := zap.NewNop()
	startTime := time.Now()

	timestamp1 := pcommon.NewTimestampFromTime(time.Date(1999, time.May, 18, 0, 0, 0, 0, time.UTC))
	timestamp2 := pcommon.NewTimestampFromTime(time.Date(2015, time.April, 1, 0, 0, 0, 0, time.UTC))

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
		logRecords   func() plog.Logs
		expectations func(t *testing.T, requests []*api.BatchCreateLogsRequest)
	}{
		{
			name: "Single log record with expected data",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Test log message", map[string]any{"secops_log_type": "WINEVTLOG", "namespace": "test", `chronicle_ingestion_label["env"]`: "prod"}))
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1)
				batch := requests[0].Batch
				require.Equal(t, "WINEVTLOG", batch.LogType)
				require.Len(t, batch.Entries, 1)

				// Convert Data (byte slice) to string for comparison
				logDataAsString := string(batch.Entries[0].Data)
				expectedLogData := `Test log message`
				require.Equal(t, expectedLogData, logDataAsString)

				require.NotNil(t, batch.StartTime)
				require.True(t, timestamppb.New(startTime).AsTime().Equal(batch.StartTime.AsTime()), "Start time should be set correctly")
			},
		},
		{
			name: "Single log record with expected data, no log type, namespace, or ingestion labels",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Test log message", nil))
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1)
				batch := requests[0].Batch
				require.Equal(t, "WINEVTLOG", batch.LogType)
				require.Equal(t, "", batch.Source.Namespace)
				require.Equal(t, 0, len(batch.Source.Labels))
				require.Len(t, batch.Entries, 1)

				// Convert Data (byte slice) to string for comparison
				logDataAsString := string(batch.Entries[0].Data)
				expectedLogData := `Test log message`
				require.Equal(t, expectedLogData, logDataAsString)

				require.NotNil(t, batch.StartTime)
				require.True(t, timestamppb.New(startTime).AsTime().Equal(batch.StartTime.AsTime()), "Start time should be set correctly")
			},
		},
		{
			name: "Multiple log records",
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
				record2 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record2.Body().SetStr("Second log message")
				return logs
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1, "Expected a single batch request")
				batch := requests[0].Batch
				require.Len(t, batch.Entries, 2, "Expected two log entries in the batch")
				// Verifying the first log entry data
				require.Equal(t, "First log message", string(batch.Entries[0].Data))
				// Verifying the second log entry data
				require.Equal(t, "Second log message", string(batch.Entries[1].Data))
			},
		},
		{
			name: "Multiple log records with and without timestamps",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				logs := plog.NewLogs()
				record1 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record1.Body().SetStr("Log 1 with collection time set")
				record1.SetObservedTimestamp(timestamp1)
				record2 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record2.SetTimestamp(timestamp1)
				record2.Body().SetStr("Log 2 with timestamp set")
				record3 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record3.SetObservedTimestamp(timestamp1)
				record3.SetTimestamp(timestamp2)
				record3.Body().SetStr("Log 3 with timestamp and collection time set")
				record4 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record4.Body().SetStr("Log 4 with no timestamp or collection time set")
				return logs
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1, "Expected a single batch request")
				batch := requests[0].Batch
				require.Len(t, batch.Entries, 4, "Expected four log entries in the batch")

				ts1 := timestamp1.AsTime().Unix()
				ts2 := timestamp2.AsTime().Unix()

				// Timestamp gets set with observed timestamp if it is not set
				require.Equal(t, batch.Entries[0].Timestamp.Seconds, ts1)
				require.Equal(t, batch.Entries[0].CollectionTime.Seconds, ts1)

				// Collection time gets set to Time.Now() instead of being default 0
				require.Equal(t, batch.Entries[1].Timestamp.Seconds, ts1)
				require.NotEqual(t, batch.Entries[1].CollectionTime.Seconds, 0)

				// Timestamp and collection time get set to their set values
				require.Equal(t, batch.Entries[2].CollectionTime.Seconds, ts1)
				require.Equal(t, batch.Entries[2].Timestamp.Seconds, ts2)

				// Collection time and timestamp get set to Time.Now() instead of being default 0
				require.NotEqual(t, batch.Entries[3].CollectionTime.Seconds, 0)
				require.NotEqual(t, batch.Entries[3].Timestamp.Seconds, 0)
			},
		},
		{
			name: "Log record with attributes",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "attributes",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("", map[string]any{"key1": "value1", "secops_log_type": "WINEVTLOG", "namespace": "test", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"}))
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1)
				batch := requests[0].Batch
				require.Len(t, batch.Entries, 1)

				// Assuming the attributes are marshaled into the Data field as a JSON string
				expectedData := `{"key1":"value1", "secops_log_type":"WINEVTLOG", "namespace":"test", "chronicle_ingestion_label[\"key1\"]": "value1", "chronicle_ingestion_label[\"key2\"]": "value2"}`
				actualData := string(batch.Entries[0].Data)
				require.JSONEq(t, expectedData, actualData, "Log attributes should match expected")
			},
		},
		{
			name: "No log records",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "DEFAULT",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				return plog.NewLogs() // No log records added
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 0, "Expected no requests due to no log records")
			},
		},
		{
			name: "No log type set in config or attributes",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Log without logType", map[string]any{"namespace": "test", `ingestion_label["realkey1"]`: "realvalue1", `ingestion_label["realkey2"]`: "realvalue2"}))
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1)
				batch := requests[0].Batch
				require.Equal(t, "CATCH_ALL", batch.LogType)
			},
		},
		{
			name: "Multiple log records with duplicate data, no log type in attributes",
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
				record1.Attributes().FromRaw(map[string]any{"chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
				record2 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record2.Body().SetStr("Second log message")
				record2.Attributes().FromRaw(map[string]any{"chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
				return logs
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				// verify one request for log type in config
				require.Len(t, requests, 1, "Expected a single batch request")
				batch := requests[0].Batch
				// verify batch source labels
				require.Len(t, batch.Source.Labels, 2)
				require.Len(t, batch.Entries, 2, "Expected two log entries in the batch")
				// Verifying the first log entry data
				require.Equal(t, "First log message", string(batch.Entries[0].Data))
				// Verifying the second log entry data
				require.Equal(t, "Second log message", string(batch.Entries[1].Data))
			},
		},
		{
			name: "Multiple log records with different data, no log type in attributes",
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
				record1.Attributes().FromRaw(map[string]any{`chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
				record2 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record2.Body().SetStr("Second log message")
				record2.Attributes().FromRaw(map[string]any{`chronicle_ingestion_label["key3"]`: "value3", `chronicle_ingestion_label["key4"]`: "value4"})
				return logs
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				// verify one request for one log type
				require.Len(t, requests, 2, "Expected two batch requests")
				batch1 := requests[0].Batch
				batch2 := requests[1].Batch
				if string(batch1.Entries[0].Data) != "First log message" {
					batch1 = requests[1].Batch
					batch2 = requests[0].Batch
				}
				require.Len(t, batch1.Entries, 1, "Expected one log entries in the batch")
				require.Equal(t, "First log message", string(batch1.Entries[0].Data))
				require.Equal(t, "WINEVTLOG", batch1.LogType)
				require.Equal(t, "", batch1.Source.Namespace)
				// verify batch source labels
				require.Len(t, batch1.Source.Labels, 2)
				expectedLabels := map[string]string{
					"key1": "value1",
					"key2": "value2",
				}
				for _, label := range batch1.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value, "Expected ingestion label to be overridden by attribute")
				}

				// Verifying the second log entry data
				require.Len(t, batch2.Entries, 1, "Expected one log entries in the batch")
				require.Equal(t, "Second log message", string(batch2.Entries[0].Data))
				require.Equal(t, "WINEVTLOG", batch2.LogType)
				require.Equal(t, "", batch2.Source.Namespace)
				require.Len(t, batch2.Source.Labels, 2)
				expectedLabels = map[string]string{
					"key3": "value3",
					"key4": "value4",
				}
				for _, label := range batch2.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value, "Expected ingestion label to be overridden by attribute")
				}
			},
		},
		{
			name: "Override log type with secops attribute",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "DEFAULT",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Log with secops type", map[string]any{"secops_log_type": "OKTA", "secops_namespace": "secops_ns", `secops_ingestion_label["env"]`: "staging"}))
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1)
				batch := requests[0].Batch
				require.Equal(t, "OKTA", batch.LogType, "Expected log type to be set by secops_log_type attribute")
				require.Equal(t, "secops_ns", batch.Source.Namespace, "Expected namespace to be set by secops_namespace attribute")
			},
		},
		{
			name: "Override log type with chronicle attribute",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "DEFAULT", // This should be overridden by the chronicle_log_type attribute
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Log with overridden type", map[string]any{"chronicle_log_type": "ASOC_ALERT", "chronicle_namespace": "test", `chronicle_ingestion_label["realkey1"]`: "realvalue1", `chronicle_ingestion_label["realkey2"]`: "realvalue2"}))
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1)
				batch := requests[0].Batch
				require.Equal(t, "ASOC_ALERT", batch.LogType, "Expected log type to be overridden by attribute")
				require.Equal(t, "test", batch.Source.Namespace, "Expected namespace to be overridden by attribute")
				expectedLabels := map[string]string{
					"realkey1": "realvalue1",
					"realkey2": "realvalue2",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value, "Expected ingestion label to be overridden by attribute")
				}
			},
		},
		{
			name: "secops_log_type takes precedence over chronicle_log_type",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "DEFAULT",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				return mockLogs(mockLogRecord("Log with both types", map[string]any{"secops_log_type": "OKTA", "chronicle_log_type": "ASOC_ALERT"}))
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1)
				batch := requests[0].Batch
				require.Equal(t, "OKTA", batch.LogType, "Expected secops_log_type to take precedence over chronicle_log_type")
			},
		},
		{
			name: "Multiple log records with duplicate data, log type in attributes",
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
				record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})

				record2 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record2.Body().SetStr("Second log message")
				record2.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
				return logs
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				// verify 1 request, 2 batches for same log type
				require.Len(t, requests, 1, "Expected a single batch request")
				batch := requests[0].Batch
				require.Len(t, batch.Entries, 2, "Expected two log entries in the batch")
				// verify batch for first log
				require.Equal(t, "WINEVTLOGS", batch.LogType)
				require.Equal(t, "test1", batch.Source.Namespace)
				require.Len(t, batch.Source.Labels, 2)
				expectedLabels := map[string]string{
					"key1": "value1",
					"key2": "value2",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value, "Expected ingestion label to be overridden by attribute")
				}
			},
		},
		{
			name: "Multiple log records with different data, log type in attributes",
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
				record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS1", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})

				record2 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record2.Body().SetStr("Second log message")
				record2.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS2", "chronicle_namespace": "test2", `chronicle_ingestion_label["key3"]`: "value3", `chronicle_ingestion_label["key4"]`: "value4"})
				return logs
			},

			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				// verify 2 requests, with 1 batch for different log types
				require.Len(t, requests, 2, "Expected a two batch request")
				batch1 := requests[0].Batch
				batch2 := requests[1].Batch
				if string(batch1.Entries[0].Data) != "First log message" {
					batch1 = requests[1].Batch
					batch2 = requests[0].Batch
				}

				require.Len(t, batch1.Entries, 1, "Expected one log entries in the batch")
				// verify batch for first log
				require.Equal(t, "WINEVTLOGS1", batch1.LogType)
				require.Equal(t, "test1", batch1.Source.Namespace)
				require.Len(t, batch1.Source.Labels, 2)
				expectedLabels := map[string]string{
					"key1": "value1",
					"key2": "value2",
				}
				for _, label := range batch1.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}

				// verify batch for second log
				require.Len(t, batch2.Entries, 1, "Expected one log entries in the batch")
				require.Equal(t, "WINEVTLOGS2", batch2.LogType)
				require.Equal(t, "test2", batch2.Source.Namespace)
				require.Len(t, batch2.Source.Labels, 2)
				expectedLabels = map[string]string{
					"key3": "value3",
					"key4": "value4",
				}
				for _, label := range batch2.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}

			},
		},

		{
			name: "Many logs, all one batch",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				logs := plog.NewLogs()
				logRecords := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
				for i := 0; i < 1000; i++ {
					record1 := logRecords.AppendEmpty()
					record1.Body().SetStr("Log message")
					record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS1", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
				}
				return logs
			},

			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				// verify 1 request, with 1 batch
				require.Len(t, requests, 1, "Expected a one-batch request")
				batch := requests[0].Batch
				require.Len(t, batch.Entries, 1000, "Expected 1000 log entries in the batch")
				// verify batch for first log
				require.Equal(t, "WINEVTLOGS1", batch.LogType)
				require.Equal(t, "test1", batch.Source.Namespace)
				require.Len(t, batch.Source.Labels, 2)

				// verify ingestion labels
				expectedLabels := map[string]string{
					"key1": "value1",
					"key2": "value2",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}
			},
		},
		{
			name: "Single batch split into multiple because request size too large",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				logs := plog.NewLogs()
				logRecords := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
				// create 640 logs with size 8192 bytes each - totalling 5242880 bytes. non-body fields put us over limit
				for i := 0; i < 640; i++ {
					record1 := logRecords.AppendEmpty()
					body := tokenWithLength(8192)
					record1.Body().SetStr(string(body))
					record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS1", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
				}
				return logs
			},

			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				// verify  request, with 1 batch
				require.Len(t, requests, 2, "Expected a two-batch request")
				batch := requests[0].Batch
				require.Len(t, batch.Entries, 320, "Expected 320 log entries in the first batch")
				// verify batch for first log
				require.Contains(t, batch.LogType, "WINEVTLOGS")
				require.Contains(t, batch.Source.Namespace, "test")
				require.Len(t, batch.Source.Labels, 2)

				batch2 := requests[1].Batch
				require.Len(t, batch2.Entries, 320, "Expected 320 log entries in the second batch")
				// verify batch for first log
				require.Contains(t, batch2.LogType, "WINEVTLOGS")
				require.Contains(t, batch2.Source.Namespace, "test")
				require.Len(t, batch2.Source.Labels, 2)

				// verify ingestion labels'
				expectedLabels := map[string]string{
					"key1": "value1",
					"key2": "value2",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}
			},
		},
		{
			name: "Recursively split batch into multiple because request size too large",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				logs := plog.NewLogs()
				logRecords := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
				// create 1280 logs with size 8192 bytes each - totalling 5242880 * 2 bytes. non-body fields put us over twice the limit
				for i := 0; i < 1280; i++ {
					record1 := logRecords.AppendEmpty()
					body := tokenWithLength(8192)
					record1.Body().SetStr(string(body))
					record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS1", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
				}
				return logs
			},

			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				// verify 1 request, with 1 batch
				require.Len(t, requests, 4, "Expected a four-batch request")
				batch := requests[0].Batch
				require.Len(t, batch.Entries, 320, "Expected 320 log entries in the first batch")
				// verify batch for first log
				require.Equal(t, "WINEVTLOGS1", batch.LogType)
				require.Equal(t, "test1", batch.Source.Namespace)
				require.Len(t, batch.Source.Labels, 2)

				batch2 := requests[1].Batch
				require.Len(t, batch2.Entries, 320, "Expected 320 log entries in the second batch")
				// verify batch for first log
				require.Equal(t, "WINEVTLOGS1", batch2.LogType)
				require.Equal(t, "test1", batch2.Source.Namespace)
				require.Len(t, batch2.Source.Labels, 2)

				batch3 := requests[2].Batch
				require.Len(t, batch3.Entries, 320, "Expected 320 log entries in the third batch")
				// verify batch for first log
				require.Equal(t, "WINEVTLOGS1", batch3.LogType)
				require.Equal(t, "test1", batch3.Source.Namespace)
				require.Len(t, batch3.Source.Labels, 2)

				batch4 := requests[3].Batch
				require.Len(t, batch4.Entries, 320, "Expected 320 log entries in the fourth batch")
				// verify batch for first log
				require.Equal(t, "WINEVTLOGS1", batch4.LogType)
				require.Equal(t, "test1", batch4.Source.Namespace)
				require.Len(t, batch4.Source.Labels, 2)

				// verify ingestion labels
				expectedLabels := map[string]string{
					"key1": "value1",
					"key2": "value2",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}
				for _, label := range batch2.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}
				for _, label := range batch3.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}
				for _, label := range batch4.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}
			},
		},
		{
			name: "Unsplittable batch, single log exceeds max request size",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				logs := plog.NewLogs()
				record1 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				body := tokenWithLength(5242881)
				record1.Body().SetStr(string(body))
				record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS1", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
				return logs
			},

			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				// verify 1 request, with 1 batch
				require.Len(t, requests, 0, "Expected a zero requests")
			},
		},
		{
			name: "Multiple valid log records + unsplittable log entries",
			cfg: Config{
				CustomerID:            uuid.New().String(),
				DefaultLogType:        "WINEVTLOG",
				RawLogField:           "body",
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				logs := plog.NewLogs()
				tooLargeBody := string(tokenWithLength(5242881))
				// first normal log, then impossible to split log
				logRecords1 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
				record1 := logRecords1.AppendEmpty()
				record1.Body().SetStr("First log message")
				tooLargeRecord1 := logRecords1.AppendEmpty()
				tooLargeRecord1.Body().SetStr(tooLargeBody)
				// first impossible to split log, then normal log
				logRecords2 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
				tooLargeRecord2 := logRecords2.AppendEmpty()
				tooLargeRecord2.Body().SetStr(tooLargeBody)
				record2 := logRecords2.AppendEmpty()
				record2.Body().SetStr("Second log message")
				return logs
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				// this is a kind of weird edge case, the overly large logs makes the final requests quite inefficient, but it's going to be so rare that the inefficiency isn't a real concern
				require.Len(t, requests, 2, "Expected two batch requests")
				batch1 := requests[0].Batch
				require.Len(t, batch1.Entries, 1, "Expected one log entry in the first batch")
				// Verifying the first log entry data
				require.Equal(t, "First log message", string(batch1.Entries[0].Data))

				batch2 := requests[1].Batch
				require.Len(t, batch2.Entries, 1, "Expected one log entry in the second batch")
				// Verifying the second log entry data
				require.Equal(t, "Second log message", string(batch2.Entries[0].Data))
			},
		},
		{
			name: "Multiple namespace, ingestion labels, and log type",
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
				record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS1", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})

				record2 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record2.Body().SetStr("Second log message")
				record2.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS2", "chronicle_namespace": "test2", `chronicle_ingestion_label["key3"]`: "value3", `chronicle_ingestion_label["key4"]`: "value4"})

				record3 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record3.Body().SetStr("Third log message")
				record3.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS3", "chronicle_namespace": "test3", `chronicle_ingestion_label["key5"]`: "value5", `chronicle_ingestion_label["key6"]`: "value6"})

				// these two should be grouped
				record4 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record4.Body().SetStr("Fourth log message")
				record4.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS4", "chronicle_namespace": "test4", `chronicle_ingestion_label["key7"]`: "value7", `chronicle_ingestion_label["key8"]`: "value8"})
				record5 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record5.Body().SetStr("Fifth log message")
				record5.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS4", "chronicle_namespace": "test4", `chronicle_ingestion_label["key7"]`: "value7", `chronicle_ingestion_label["key8"]`: "value8"})

				// same log type as record4, but different namespace
				record6 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record6.Body().SetStr("Sixth log message")
				record6.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS4", "chronicle_namespace": "test5", `chronicle_ingestion_label["key7"]`: "value7", `chronicle_ingestion_label["key8"]`: "value8"})
				return logs
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 5)

				batches := map[string]*api.BatchCreateLogsRequest{}
				for _, request := range requests {
					batches[request.Batch.Source.Namespace] = request
				}

				require.Len(t, batches, 5)

				// test1 namespace
				batch := batches["test1"].Batch
				require.Equal(t, "WINEVTLOGS1", batch.LogType)
				require.Equal(t, "test1", batch.Source.Namespace)
				require.Len(t, batch.Entries, 1)
				require.Equal(t, "First log message", string(batch.Entries[0].Data))
				expectedLabels := map[string]string{
					"key1": "value1",
					"key2": "value2",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}

				// test2 namespace
				batch = batches["test2"].Batch
				require.Equal(t, "WINEVTLOGS2", batch.LogType)
				require.Equal(t, "test2", batch.Source.Namespace)
				require.Len(t, batch.Entries, 1)
				require.Equal(t, "Second log message", string(batch.Entries[0].Data))
				expectedLabels = map[string]string{
					"key3": "value3",
					"key4": "value4",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}

				// test3 namespace
				batch = batches["test3"].Batch
				require.Equal(t, "WINEVTLOGS3", batch.LogType)
				require.Equal(t, "test3", batch.Source.Namespace)
				require.Len(t, batch.Entries, 1)
				require.Equal(t, "Third log message", string(batch.Entries[0].Data))
				expectedLabels = map[string]string{
					"key5": "value5",
					"key6": "value6",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}

				// test4 namespace
				batch = batches["test4"].Batch
				require.Equal(t, "WINEVTLOGS4", batch.LogType)
				require.Equal(t, "test4", batch.Source.Namespace)
				require.Len(t, batch.Entries, 2)
				firstLog := batch.Entries[0]
				secondLog := batch.Entries[1]

				if string(firstLog.Data) != "Fourth log message" {
					firstLog, secondLog = secondLog, firstLog
				}
				require.Equal(t, "Fourth log message", string(firstLog.Data))
				require.Equal(t, "Fifth log message", string(secondLog.Data))
				expectedLabels = map[string]string{
					"key7": "value7",
					"key8": "value8",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}

				// test5 namespace
				batch = batches["test5"].Batch
				require.Equal(t, "WINEVTLOGS4", batch.LogType)
				require.Equal(t, "test5", batch.Source.Namespace)
				require.Len(t, batch.Entries, 1)
				require.Equal(t, "Sixth log message", string(batch.Entries[0].Data))
				expectedLabels = map[string]string{
					"key7": "value7",
					"key8": "value8",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}

			},
		},
		{
			name: "Destination ingestion labels are merged with config ingestion labels",
			cfg: Config{
				CustomerID:     uuid.New().String(),
				DefaultLogType: "WINEVTLOG",
				RawLogField:    "body",
				IngestionLabels: map[string]string{
					"key1": "value3",
					"key2": "value4",
					"key3": "value5",
				},
				BatchRequestSizeLimit: 5242880,
			},
			logRecords: func() plog.Logs {
				logs := plog.NewLogs()
				record1 := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
				record1.Body().SetStr("First log message")
				record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS1", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
				return logs
			},
			expectations: func(t *testing.T, requests []*api.BatchCreateLogsRequest) {
				require.Len(t, requests, 1)
				batch := requests[0].Batch
				require.Equal(t, "WINEVTLOGS1", batch.LogType)
				require.Equal(t, "test1", batch.Source.Namespace)
				require.Len(t, batch.Entries, 1)
				require.Equal(t, "First log message", string(batch.Entries[0].Data))
				expectedLabels := map[string]string{
					"key1": "value1",
					"key2": "value2",
					"key3": "value5",
				}
				for _, label := range batch.Source.Labels {
					require.Equal(t, expectedLabels[label.Key], label.Value)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marshaler, err := newProtoMarshaler(tt.cfg, component.TelemetrySettings{Logger: logger}, telemetry, logger)
			marshaler.startTime = startTime
			require.NoError(t, err)

			logs := tt.logRecords()
			requests, _, err := marshaler.MarshalBackstoryRawLogs(context.Background(), logs)
			require.NoError(t, err)

			tt.expectations(t, requests)
		})
	}
}

func BenchmarkProtoMarshaler_MarshalBackstoryRawLogs(b *testing.B) {
	logger := zap.NewNop()
	startTime := time.Now()

	telemSettings := component.TelemetrySettings{
		Logger:        logger,
		MeterProvider: noop.NewMeterProvider(),
	}

	telemetry, err := metadata.NewTelemetryBuilder(telemSettings)
	if err != nil {
		b.Errorf("Error creating telemetry builder: %v", err)
		b.Fail()
	}

	cfg := Config{
		CustomerID:            uuid.New().String(),
		DefaultLogType:        "WINEVTLOG",
		RawLogField:           "body",
		BatchRequestSizeLimit: 5242880,
	}

	logs := plog.NewLogs()
	logRecords := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
	for i := 0; i < 1000; i++ {
		record1 := logRecords.AppendEmpty()
		record1.Body().SetStr("Log message")
		record1.Attributes().FromRaw(map[string]any{"chronicle_log_type": "WINEVTLOGS1", "chronicle_namespace": "test1", `chronicle_ingestion_label["key1"]`: "value1", `chronicle_ingestion_label["key2"]`: "value2"})
	}

	b.ResetTimer()
	marshaler, err := newProtoMarshaler(cfg, component.TelemetrySettings{Logger: logger}, telemetry, logger)
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		marshaler.startTime = startTime
		_, _, err := marshaler.MarshalBackstoryRawLogs(context.Background(), logs)
		require.NoError(b, err)
	}
}
