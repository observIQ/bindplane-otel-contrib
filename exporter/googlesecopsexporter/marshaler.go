package googlesecopsexporter

import (
	"context"
	"fmt"
	"strings"
	"time"

	json "github.com/goccy/go-json"

	"github.com/google/uuid"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/internal/expr"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/protos/api"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottllog"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	bodyField                      = `body`
	attrExprPattern                = `attributes["%s"]`
	chronicleNamespaceAttribute    = `chronicle_namespace`
	chronicleLogTypeAttribute      = `chronicle_log_type`
	chronicleIngestionLabelsPrefix = `chronicle_ingestion_label`
	secopsNamespaceAttribute       = `secops_namespace`
	secopsLogTypeAttribute         = `secops_log_type`
	secopsIngestionLabelsPrefix    = `secops_ingestion_label`
	logRecordOriginalAttribute     = `log.record.original`

	// catchAllLogType is the log type that is used when the log type is not found in the log types map
	catchAllLogType = "CATCH_ALL"
)

var (
	chronicleLogTypeField   = fmt.Sprintf(attrExprPattern, chronicleLogTypeAttribute)
	chronicleNamespaceField = fmt.Sprintf(attrExprPattern, chronicleNamespaceAttribute)
	secopsLogTypeField      = fmt.Sprintf(attrExprPattern, secopsLogTypeAttribute)
	secopsNamespaceField    = fmt.Sprintf(attrExprPattern, secopsNamespaceAttribute)
	logRecordOriginalField  = fmt.Sprintf(attrExprPattern, logRecordOriginalAttribute)
)

type protoMarshaler struct {
	cfg          Config
	teleSettings component.TelemetrySettings
	startTime    time.Time
	customerID   []byte
	collectorID  []byte
	telemetry    *metadata.TelemetryBuilder
	logTypes     map[string]exists
	logger       *zap.Logger
}

func newProtoMarshaler(cfg Config, teleSettings component.TelemetrySettings, telemetry *metadata.TelemetryBuilder, logger *zap.Logger) (*protoMarshaler, error) {
	customerID, err := uuid.Parse(cfg.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("parse customer ID: %w", err)
	}
	return &protoMarshaler{
		startTime:    time.Now(),
		cfg:          cfg,
		teleSettings: teleSettings,
		customerID:   customerID[:],
		collectorID:  []byte(cfg.CollectorID),
		telemetry:    telemetry,
		logger:       logger,
	}, nil
}

func (m *protoMarshaler) MarshalBackstoryRawLogs(ctx context.Context, ld plog.Logs) ([]*api.BatchCreateLogsRequest, uint, error) {
	logGrouper, totalBytes, err := m.extractBackstoryRawLogs(ctx, ld)
	if err != nil {
		return nil, 0, fmt.Errorf("extract raw logs: %w", err)
	}
	return m.constructBackstoryPayloads(logGrouper), totalBytes, nil
}

func (m *protoMarshaler) extractBackstoryRawLogs(ctx context.Context, ld plog.Logs) (*logGrouper, uint, error) {
	totalBytes := uint(0)
	logGrouper := newLogGrouper()
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		resourceLog := ld.ResourceLogs().At(i)
		for j := 0; j < resourceLog.ScopeLogs().Len(); j++ {
			scopeLog := resourceLog.ScopeLogs().At(j)
			for k := 0; k < scopeLog.LogRecords().Len(); k++ {
				logRecord := scopeLog.LogRecords().At(k)

				rawLog, logType, namespace, ingestionLabelsMap, err := m.processLogRecord(ctx, logRecord, scopeLog, resourceLog)
				if err != nil {
					m.teleSettings.Logger.Error("Error processing log record", zap.Error(err))
					continue
				}

				if rawLog == "" {
					continue
				}

				timestamp := getTimestamp(logRecord)
				collectionTime := getObservedTimestamp(logRecord)

				data := []byte(rawLog)
				entry := &api.LogEntry{
					Timestamp:      timestamppb.New(timestamp),
					CollectionTime: timestamppb.New(collectionTime),
					Data:           data,
				}
				totalBytes += uint(len(data))

				ingestionLabels := make([]*api.Label, 0, len(ingestionLabelsMap))
				for key, value := range ingestionLabelsMap {
					ingestionLabels = append(ingestionLabels, &api.Label{
						Key:   key,
						Value: value,
					})
				}
				logGrouper.Add(entry, namespace, logType, ingestionLabels)
			}
		}
	}

	return logGrouper, totalBytes, nil
}

func (m *protoMarshaler) processLogRecord(ctx context.Context, logRecord plog.LogRecord, scope plog.ScopeLogs, resource plog.ResourceLogs) (string, string, string, map[string]string, error) {
	rawLog, err := m.getRawLog(ctx, logRecord, scope, resource)
	if err != nil {
		return "", "", "", nil, err
	}
	logType, err := m.getLogType(ctx, logRecord, scope, resource)
	if err != nil {
		return "", "", "", nil, err
	}
	namespace, err := m.getNamespace(ctx, logRecord, scope, resource)
	if err != nil {
		return "", "", "", nil, err
	}
	ingestionLabels, err := m.getIngestionLabelsMap(logRecord)
	if err != nil {
		return "", "", "", nil, err
	}
	return rawLog, logType, namespace, ingestionLabels, nil
}

func (m *protoMarshaler) getRawLog(ctx context.Context, logRecord plog.LogRecord, scope plog.ScopeLogs, resource plog.ResourceLogs) (string, error) {
	if m.cfg.RawLogField == "" {
		entireLogRecord := map[string]any{
			"body":                logRecord.Body().Str(),
			"attributes":          logRecord.Attributes().AsRaw(),
			"resource_attributes": resource.Resource().Attributes().AsRaw(),
		}

		bytesLogRecord, err := json.Marshal(entireLogRecord)
		if err != nil {
			return "", fmt.Errorf("marshal log record: %w", err)
		}

		return string(bytesLogRecord), nil
	}
	return m.getRawField(ctx, m.cfg.RawLogField, logRecord, scope, resource)
}

func (m *protoMarshaler) shouldValidateLogType() bool {
	return m.cfg.ValidateLogTypes && m.logTypes != nil
}

func (m *protoMarshaler) getLogType(ctx context.Context, logRecord plog.LogRecord, scope plog.ScopeLogs, resource plog.ResourceLogs) (string, error) {
	// check attributes["secops_log_type"]
	logType, err := m.getRawField(ctx, secopsLogTypeField, logRecord, scope, resource)
	if err != nil {
		return "", fmt.Errorf("get secops log type: %w", err)
	}
	if logType == "" {
		// check attributes["chronicle_log_type"]
		logType, err = m.getRawField(ctx, chronicleLogTypeField, logRecord, scope, resource)
		if err != nil {
			return "", fmt.Errorf("get chronicle log type: %w", err)
		}
	}

	if logType != "" {
		if m.shouldValidateLogType() {
			if _, ok := m.logTypes[logType]; ok {
				return logType, nil
			}
			m.logger.Warn("Log type could not be validated", zap.String("logType", logType), zap.String("logTypeField", chronicleLogTypeField))
		} else {
			return logType, nil
		}
	}

	if m.cfg.DefaultLogType == "" {
		return catchAllLogType, nil
	}

	if m.shouldValidateLogType() {
		if _, ok := m.logTypes[m.cfg.DefaultLogType]; !ok {
			m.logger.Warn("Default log type not found in log types map", zap.String("logType", m.cfg.DefaultLogType))
			return catchAllLogType, nil
		}
	}
	return m.cfg.DefaultLogType, nil
}

func (m *protoMarshaler) getNamespace(ctx context.Context, logRecord plog.LogRecord, scope plog.ScopeLogs, resource plog.ResourceLogs) (string, error) {
	// check attributes["secops_namespace"]
	secopsNamespace, err := m.getRawField(ctx, secopsNamespaceField, logRecord, scope, resource)
	if err != nil {
		return "", fmt.Errorf("get secops namespace: %w", err)
	}
	if secopsNamespace != "" {
		return secopsNamespace, nil
	}

	// check attributes["chronicle_namespace"]
	chronicleNamespace, err := m.getRawField(ctx, chronicleNamespaceField, logRecord, scope, resource)
	if err != nil {
		return "", fmt.Errorf("get chronicle namespace: %w", err)
	}
	if chronicleNamespace != "" {
		return chronicleNamespace, nil
	}

	return m.cfg.Namespace, nil
}

func (m *protoMarshaler) getIngestionLabelsMap(logRecord plog.LogRecord) (map[string]string, error) {
	// check for labels in attributes["secops_ingestion_labels"]
	secopsIngestionLabels, err := m.getRawNestedFields(secopsIngestionLabelsPrefix, logRecord)
	if err != nil {
		return nil, fmt.Errorf("get secops ingestion labels: %w", err)
	}

	// check for labels in attributes["chronicle_ingestion_labels"]
	chronicleIngestionLabels, err := m.getRawNestedFields(chronicleIngestionLabelsPrefix, logRecord)
	if err != nil {
		return nil, fmt.Errorf("get chronicle ingestion labels: %w", err)
	}

	// merge labels prioritizing secops ingestion labels > chronicle ingestion labels > config ingestion labels
	mergedLabels := secopsIngestionLabels
	for key, value := range chronicleIngestionLabels {
		if _, exists := mergedLabels[key]; !exists {
			mergedLabels[key] = value
		}
	}
	for key, value := range m.cfg.IngestionLabels {
		if _, exists := chronicleIngestionLabels[key]; !exists {
			mergedLabels[key] = value
		}
	}

	return mergedLabels, nil
}

// getRawField is a helper function to get the raw value of a field from a log record
func (m *protoMarshaler) getRawField(ctx context.Context, field string, logRecord plog.LogRecord, scope plog.ScopeLogs, resource plog.ResourceLogs) (string, error) {
	switch field {
	case bodyField:
		switch logRecord.Body().Type() {
		case pcommon.ValueTypeStr:
			return logRecord.Body().Str(), nil
		case pcommon.ValueTypeMap:
			bytes, err := json.Marshal(logRecord.Body().AsRaw())
			if err != nil {
				return "", fmt.Errorf("marshal log body: %w", err)
			}
			return string(bytes), nil
		}
	case chronicleLogTypeField:
		attributes := logRecord.Attributes().AsRaw()
		if logType, ok := attributes[chronicleLogTypeAttribute]; ok {
			if v, ok := logType.(string); ok {
				return v, nil
			}
		}
		return "", nil
	case chronicleNamespaceField:
		attributes := logRecord.Attributes().AsRaw()
		if namespace, ok := attributes[chronicleNamespaceAttribute]; ok {
			if v, ok := namespace.(string); ok {
				return v, nil
			}
		}
		return "", nil
	case logRecordOriginalField:
		attributes := logRecord.Attributes().AsRaw()
		if logRecordOriginal, ok := attributes[logRecordOriginalAttribute]; ok {
			switch logRecordOriginal := logRecordOriginal.(type) {
			case string:
				return logRecordOriginal, nil
			case map[string]any:
				bytes, err := json.Marshal(logRecordOriginal)
				if err != nil {
					return "", fmt.Errorf("marshal log record original: %w", err)
				}
				return string(bytes), nil
			default:
				return "", fmt.Errorf("unsupported log record original type: %T", logRecordOriginal)
			}
		}
		return "", nil
	}

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

func (m *protoMarshaler) getRawNestedFields(field string, logRecord plog.LogRecord) (map[string]string, error) {
	nestedFields := make(map[string]string)
	logRecord.Attributes().Range(func(key string, value pcommon.Value) bool {
		if !strings.HasPrefix(key, field) {
			return true
		}
		// Extract the key name from the nested field
		cleanKey := strings.Trim(key[len(field):], `[]"`)
		var jsonMap map[string]string

		// If needs to be parsed as JSON
		if err := json.Unmarshal([]byte(value.AsString()), &jsonMap); err == nil {
			for k, v := range jsonMap {
				nestedFields[k] = v
			}
		} else {
			nestedFields[cleanKey] = value.AsString()
		}
		return true
	})
	return nestedFields, nil
}

func (m *protoMarshaler) constructBackstoryPayloads(logGrouper *logGrouper) []*api.BatchCreateLogsRequest {
	payloads := make([]*api.BatchCreateLogsRequest, 0, len(logGrouper.groups))

	metricCtx := context.Background()

	logGrouper.ForEach(func(entries []*api.LogEntry, namespace, logType string, ingestionLabels []*api.Label) {
		if namespace == "" {
			namespace = m.cfg.Namespace
		}

		request := m.buildBackstoryRequest(entries, logType, namespace, ingestionLabels)

		payloads = append(payloads, m.enforceMaximumsBackstoryRequest(request)...)
		for _, payload := range payloads {
			m.telemetry.GoogleSecopsExporterBatchSize.Record(metricCtx, int64(len(payload.Batch.Entries)))
			m.telemetry.GoogleSecopsExporterPayloadSize.Record(metricCtx, int64(proto.Size(payload)))
		}
	})
	return payloads
}

func (m *protoMarshaler) enforceMaximumsBackstoryRequest(request *api.BatchCreateLogsRequest) []*api.BatchCreateLogsRequest {
	size := proto.Size(request)
	entries := request.Batch.Entries
	if size <= m.cfg.BatchRequestSizeLimit {
		return []*api.BatchCreateLogsRequest{
			request,
		}
	}

	if len(entries) < 2 {
		m.teleSettings.Logger.Error("Single entry exceeds max request size. Dropping entry", zap.Int("size", size))
		return []*api.BatchCreateLogsRequest{}
	}

	// split request into two
	mid := len(entries) / 2
	leftHalf := entries[:mid]
	rightHalf := entries[mid:]

	request.Batch.Entries = leftHalf
	otherHalfRequest := m.buildBackstoryRequest(rightHalf, request.Batch.LogType, request.Batch.Source.Namespace, request.Batch.Source.Labels)

	// re-enforce max size restriction on each half
	enforcedRequest := m.enforceMaximumsBackstoryRequest(request)
	enforcedOtherHalfRequest := m.enforceMaximumsBackstoryRequest(otherHalfRequest)

	return append(enforcedRequest, enforcedOtherHalfRequest...)
}

func (m *protoMarshaler) buildBackstoryRequest(entries []*api.LogEntry, logType, namespace string, ingestionLabels []*api.Label) *api.BatchCreateLogsRequest {
	return &api.BatchCreateLogsRequest{
		Batch: &api.LogEntryBatch{
			StartTime: timestamppb.New(m.startTime),
			Entries:   entries,
			LogType:   logType,
			Source: &api.EventSource{
				CollectorId: m.collectorID,
				CustomerId:  m.customerID,
				Labels:      ingestionLabels,
				Namespace:   namespace,
			},
		},
	}
}

func (m *protoMarshaler) MarshalChronicleAPIRawLogs(ctx context.Context, ld plog.Logs) (map[string][]*api.ImportLogsRequest, uint, error) {
	rawLogs, totalBytes, err := m.extractChronicleAPIRawLogs(ctx, ld)
	if err != nil {
		return nil, 0, fmt.Errorf("extract raw logs: %w", err)
	}
	return m.constructChronicleAPIPayloads(rawLogs), totalBytes, nil
}

func (m *protoMarshaler) extractChronicleAPIRawLogs(ctx context.Context, ld plog.Logs) (map[string][]*api.Log, uint, error) {
	totalBytes := uint(0)
	entries := make(map[string][]*api.Log)
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		resourceLog := ld.ResourceLogs().At(i)
		for j := 0; j < resourceLog.ScopeLogs().Len(); j++ {
			scopeLog := resourceLog.ScopeLogs().At(j)
			for k := 0; k < scopeLog.LogRecords().Len(); k++ {
				logRecord := scopeLog.LogRecords().At(k)
				rawLog, logType, namespace, ingestionLabelsMap, err := m.processLogRecord(ctx, logRecord, scopeLog, resourceLog)
				if err != nil {
					m.teleSettings.Logger.Error("Error processing log record", zap.Error(err))
					continue
				}

				if rawLog == "" {
					continue
				}

				timestamp := getTimestamp(logRecord)
				collectionTime := getObservedTimestamp(logRecord)

				ingestionLabels := make(map[string]*api.Log_LogLabel, len(ingestionLabelsMap))
				for key, value := range ingestionLabelsMap {
					ingestionLabels[key] = &api.Log_LogLabel{
						Value: value,
					}
				}

				data := []byte(rawLog)
				entry := &api.Log{
					LogEntryTime:         timestamppb.New(timestamp),
					CollectionTime:       timestamppb.New(collectionTime),
					Data:                 data,
					EnvironmentNamespace: namespace,
					Labels:               ingestionLabels,
				}
				totalBytes += uint(len(data))
				entries[logType] = append(entries[logType], entry)
			}
		}
	}

	return entries, totalBytes, nil
}

func getTimestamp(logRecord plog.LogRecord) time.Time {
	if logRecord.Timestamp() != 0 {
		return logRecord.Timestamp().AsTime()
	}
	return getObservedTimestamp(logRecord)
}

func getObservedTimestamp(logRecord plog.LogRecord) time.Time {
	if logRecord.ObservedTimestamp() != 0 {
		return logRecord.ObservedTimestamp().AsTime()
	}
	return time.Now()
}

func (m *protoMarshaler) buildForwarderString() string {
	format := "projects/%s/locations/%s/instances/%s/forwarders/%s"
	return fmt.Sprintf(format, m.cfg.ProjectNumber, m.cfg.Location, m.cfg.CustomerID, string(m.collectorID[:]))
}

func (m *protoMarshaler) constructChronicleAPIPayloads(rawLogs map[string][]*api.Log) map[string][]*api.ImportLogsRequest {
	payloads := make(map[string][]*api.ImportLogsRequest, len(rawLogs))

	metricCtx := context.Background()

	for logType, entries := range rawLogs {
		if len(entries) > 0 {
			request := m.buildChronicleAPIRequest(entries)

			payloads[logType] = m.enforceMaximumsChronicleAPIRequest(request)
			for _, payload := range payloads[logType] {
				m.telemetry.GoogleSecopsExporterBatchSize.Record(metricCtx, int64(len(payload.GetInlineSource().Logs)))
				m.telemetry.GoogleSecopsExporterPayloadSize.Record(metricCtx, int64(proto.Size(payload)))
			}
		}
	}
	return payloads
}

func (m *protoMarshaler) enforceMaximumsChronicleAPIRequest(request *api.ImportLogsRequest) []*api.ImportLogsRequest {
	size := proto.Size(request)
	logs := request.GetInlineSource().Logs
	if size <= m.cfg.BatchRequestSizeLimit {
		return []*api.ImportLogsRequest{
			request,
		}
	}

	if len(logs) < 2 {
		m.teleSettings.Logger.Error("Single entry exceeds max request size. Dropping entry", zap.Int("size", size))
		return []*api.ImportLogsRequest{}
	}

	// split request into two
	mid := len(logs) / 2
	leftHalf := logs[:mid]
	rightHalf := logs[mid:]

	request.GetInlineSource().Logs = leftHalf
	otherHalfRequest := m.buildChronicleAPIRequest(rightHalf)

	// re-enforce max size restriction on each half
	enforcedRequest := m.enforceMaximumsChronicleAPIRequest(request)
	enforcedOtherHalfRequest := m.enforceMaximumsChronicleAPIRequest(otherHalfRequest)

	return append(enforcedRequest, enforcedOtherHalfRequest...)
}

func (m *protoMarshaler) buildChronicleAPIRequest(entries []*api.Log) *api.ImportLogsRequest {
	return &api.ImportLogsRequest{
		// TODO: Add hint?
		// No solid guidance on what this should be
		Hint: "",

		Source: &api.ImportLogsRequest_InlineSource{
			InlineSource: &api.ImportLogsRequest_LogsInlineSource{
				Forwarder: m.buildForwarderString(),
				Logs:      entries,
			},
		},
	}
}
