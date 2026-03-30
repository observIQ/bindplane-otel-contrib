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
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottllog"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

const (
	bodyField                      = `body`
	attrExprPattern                = `attributes["%s"]`
	chronicleNamespaceAttribute    = `chronicle_namespace`
	chronicleLogTypeAttribute      = `chronicle_log_type`
	chronicleIngestionLabelsPrefix = `chronicle_ingestion_label`
	secopsNamespaceAttribute       = `google_secops.namespace`
	secopsLogTypeAttribute         = `google_secops.log.type`
	secopsIngestionLabelsPrefix    = `google_secops.ingestion_label`
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

// processedLog holds the extracted data from a single log record,
// shared between backstory and chronicle marshal paths.
type processedLog struct {
	rawLog          string
	logType         string
	namespace       string
	ingestionLabels map[string]string
	timestamp       time.Time
	collectionTime  time.Time
	data            []byte
}

func (m *protoMarshaler) forEachLogRecord(ctx context.Context, ld plog.Logs, fn func(p processedLog)) (uint, error) {
	totalBytes := uint(0)
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

				data := []byte(rawLog)
				totalBytes += uint(len(data))
				fn(processedLog{
					rawLog:          rawLog,
					logType:         logType,
					namespace:       namespace,
					ingestionLabels: ingestionLabelsMap,
					timestamp:       getTimestamp(logRecord),
					collectionTime:  getObservedTimestamp(logRecord),
					data:            data,
				})
			}
		}
	}
	return totalBytes, nil
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
	// check attributes["google_secops.log.type"]
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
	// check attributes["google_secops.namespace"]
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
	// check for labels in attributes["google_secops.ingestion_labels"]
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
		if _, exists := mergedLabels[key]; !exists {
			mergedLabels[key] = value
		}
	}

	return mergedLabels, nil
}

// commonStringFields maps OTTL field expressions to their underlying attribute keys
// for fields that are always string attribute lookups, avoiding OTTL parsing overhead.
var commonStringFields = map[string]string{
	chronicleLogTypeField:   chronicleLogTypeAttribute,
	chronicleNamespaceField: chronicleNamespaceAttribute,
	secopsLogTypeField:      secopsLogTypeAttribute,
	secopsNamespaceField:    secopsNamespaceAttribute,
}

// getRawField is a helper function to get the raw value of a field from a log record
func (m *protoMarshaler) getRawField(ctx context.Context, field string, logRecord plog.LogRecord, scope plog.ScopeLogs, resource plog.ResourceLogs) (string, error) {
	if attrKey, ok := commonStringFields[field]; ok {
		if v, ok := logRecord.Attributes().AsRaw()[attrKey]; ok {
			if s, ok := v.(string); ok {
				return s, nil
			}
		}
		return "", nil
	}

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
