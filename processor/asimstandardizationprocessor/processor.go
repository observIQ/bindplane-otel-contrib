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

package asimstandardizationprocessor

import (
	"context"
	"fmt"
	"time"

	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

const (
	// sentinelStreamNameAttribute is the per-record routing key consumed by
	// the Microsoft Sentinel (Azure Log Analytics) exporter.
	sentinelStreamNameAttribute = "sentinel_stream_name"
	// eventSchemaColumn is the ASIM-mandated body column identifying the
	// schema (e.g. "Authentication").
	eventSchemaColumn = "EventSchema"
	// additionalFieldsColumn is the ASIM dynamic column where the original
	// pre-transform payload is stashed so unmapped fields stay queryable.
	additionalFieldsColumn = "AdditionalFields"
	// timeGeneratedColumn is the ingestion-time column Azure's Custom-ASim*
	// stream contract requires in the payload; auto-populated when unmapped.
	timeGeneratedColumn = "TimeGenerated"
	// sentinelStreamNamePrefix is the Custom-* prefix Azure DCRs require.
	sentinelStreamNamePrefix = "Custom-"
)

type compiledFieldMapping struct {
	from         *expr.Expression
	to           string
	defaultValue any
}

type compiledEventMapping struct {
	filter        *expr.Expression
	targetTable   string
	streamName    string
	eventSchema   string
	fieldMappings []compiledFieldMapping
}

type asimStandardizationProcessor struct {
	logger            *zap.Logger
	eventMappings     []compiledEventMapping
	runtimeValidation bool
	// attributionFields, when non-empty, is merged into AdditionalFields as
	// an "Attribution" sub-object on every transformed record. See Config.
	attributionFields map[string]string
}

func newASIMStandardizationProcessor(logger *zap.Logger, config *Config) (*asimStandardizationProcessor, error) {
	compiled := make([]compiledEventMapping, 0, len(config.EventMappings))
	for i, eventMapping := range config.EventMappings {
		schema, ok := eventSchemaByTargetTable[eventMapping.TargetTable]
		if !ok {
			return nil, fmt.Errorf("event_mappings[%d]: unknown target_table %q", i, eventMapping.TargetTable)
		}

		fieldMappings := make([]compiledFieldMapping, 0, len(eventMapping.FieldMappings))
		for _, fieldMapping := range eventMapping.FieldMappings {
			cfm := compiledFieldMapping{
				to:           fieldMapping.To,
				defaultValue: fieldMapping.Default,
			}
			if fieldMapping.From != "" {
				from, err := expr.CreateValueExpression(fieldMapping.From)
				if err != nil {
					return nil, fmt.Errorf("event_mappings[%d]: compiling from expression: %w", i, err)
				}
				cfm.from = from
			}
			fieldMappings = append(fieldMappings, cfm)
		}

		cem := compiledEventMapping{
			targetTable:   eventMapping.TargetTable,
			streamName:    sentinelStreamNamePrefix + eventMapping.TargetTable,
			eventSchema:   schema,
			fieldMappings: fieldMappings,
		}

		if eventMapping.Filter != "" {
			filter, err := expr.CreateBoolExpression(eventMapping.Filter)
			if err != nil {
				return nil, fmt.Errorf("event_mappings[%d]: compiling filter expression: %w", i, err)
			}
			cem.filter = filter
		}

		compiled = append(compiled, cem)
	}

	runtimeValidation := true
	if config.RuntimeValidation != nil {
		runtimeValidation = *config.RuntimeValidation
	}

	// Defensive copy so a later mutation of the caller's map cannot bleed
	// through into the processor's runtime state.
	var attributionFields map[string]string
	if len(config.AttributionFields) > 0 {
		attributionFields = make(map[string]string, len(config.AttributionFields))
		for k, v := range config.AttributionFields {
			attributionFields[k] = v
		}
	}

	return &asimStandardizationProcessor{
		logger:            logger,
		eventMappings:     compiled,
		runtimeValidation: runtimeValidation,
		attributionFields: attributionFields,
	}, nil
}

func (asp *asimStandardizationProcessor) processLogs(_ context.Context, ld plog.Logs) (plog.Logs, error) {
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		resource := ld.ResourceLogs().At(i)
		resourceAttrs := resource.Resource().Attributes().AsRaw()
		for j := 0; j < resource.ScopeLogs().Len(); j++ {
			scope := resource.ScopeLogs().At(j)
			scope.LogRecords().RemoveIf(func(log plog.LogRecord) bool {
				return !asp.processLogRecord(log, resourceAttrs)
			})
		}
		resource.ScopeLogs().RemoveIf(func(scope plog.ScopeLogs) bool {
			return scope.LogRecords().Len() == 0
		})
	}
	ld.ResourceLogs().RemoveIf(func(resource plog.ResourceLogs) bool {
		return resource.ScopeLogs().Len() == 0
	})
	return ld, nil
}

// processLogRecord applies the first matching event mapping to a single log
// record. Returns true if the record should be kept, false if it should be
// dropped (no event mapping matched).
func (asp *asimStandardizationProcessor) processLogRecord(log plog.LogRecord, resourceAttrs map[string]any) bool {
	record := expr.ConvertToRecord(log, resourceAttrs)

	for _, eventMapping := range asp.eventMappings {
		if eventMapping.filter != nil && !eventMapping.filter.MatchRecord(record) {
			continue
		}

		originalBody := log.Body().AsRaw()
		newBody := map[string]any{}

		for _, fieldMapping := range eventMapping.fieldMappings {
			var value any
			if fieldMapping.from != nil {
				val, err := fieldMapping.from.Evaluate(record)
				if err != nil {
					asp.logger.Error("Failed to evaluate expression",
						zap.String("field", fieldMapping.to),
						zap.Error(err),
					)
					value = fieldMapping.defaultValue
				} else if val == nil {
					value = fieldMapping.defaultValue
				} else {
					value = val
				}
			} else {
				value = fieldMapping.defaultValue
			}

			if value == nil {
				continue
			}
			newBody[fieldMapping.to] = value
		}

		newBody[eventSchemaColumn] = eventMapping.eventSchema
		if len(asp.attributionFields) > 0 {
			// Wrap so the original body stays queryable as
			// AdditionalFields.OriginalEvent while the constant attribution
			// markers live under AdditionalFields.Attribution.
			wrapped := map[string]any{}
			if originalBody != nil {
				wrapped["OriginalEvent"] = originalBody
			}
			attribution := make(map[string]any, len(asp.attributionFields))
			for k, v := range asp.attributionFields {
				attribution[k] = v
			}
			wrapped["Attribution"] = attribution
			newBody[additionalFieldsColumn] = wrapped
		} else if originalBody != nil {
			newBody[additionalFieldsColumn] = originalBody
		}

		// Azure's Custom-ASim* stream contract requires TimeGenerated in the
		// upload payload, but users routinely assume Azure stamps it and leave
		// it unmapped — runtime_validation then drops 100% of matched records.
		// Auto-populate it when no field mapping set it. Reusing the raw
		// (pre-coercion) EventEndTime/EventStartTime values is intentional:
		// the coercion loop below coerces TimeGenerated through the same
		// datetime path (→ RFC3339Nano) as those columns.
		if v, ok := newBody[timeGeneratedColumn]; !ok || v == nil {
			switch {
			case newBody["EventEndTime"] != nil:
				newBody[timeGeneratedColumn] = newBody["EventEndTime"]
			case newBody["EventStartTime"] != nil:
				newBody[timeGeneratedColumn] = newBody["EventStartTime"]
			case log.Timestamp() != 0:
				newBody[timeGeneratedColumn] = log.Timestamp().AsTime().UTC().Format(time.RFC3339Nano)
			case log.ObservedTimestamp() != 0:
				newBody[timeGeneratedColumn] = log.ObservedTimestamp().AsTime().UTC().Format(time.RFC3339Nano)
			default:
				newBody[timeGeneratedColumn] = time.Now().UTC().Format(time.RFC3339Nano)
			}
		}

		// Type-coerce mapped fields against the target ASIM table's column
		// types so the Azure DCR upload doesn't reject the batch with
		// InvalidTransformOutput. Coercion failures drop the offending field
		// (and, when runtime_validation is enabled, the record).
		colTypes := asimColumnTypes[eventMapping.targetTable]
		for k, v := range newBody {
			if k == additionalFieldsColumn {
				continue
			}
			want, ok := colTypes[k]
			if !ok {
				continue
			}
			coerced, ok := coerceValue(v, want)
			if !ok {
				asp.logger.Warn("ASIM column coercion failed; dropping field",
					zap.String("target_table", eventMapping.targetTable),
					zap.String("column", k),
					zap.String("want", string(want)),
					zap.Any("value", v),
				)
				delete(newBody, k)
				continue
			}
			newBody[k] = coerced
		}

		if asp.runtimeValidation {
			if missing := missingRequiredColumns(newBody); len(missing) > 0 {
				asp.logger.Warn("ASIM record missing required columns; dropping",
					zap.String("target_table", eventMapping.targetTable),
					zap.Strings("missing", missing),
				)
				return false
			}
		}

		if err := log.Body().SetEmptyMap().FromRaw(newBody); err != nil {
			asp.logger.Error("failed to set log body",
				zap.Error(err),
				zap.String("target_table", eventMapping.targetTable),
			)
			return false
		}

		log.Attributes().PutStr(sentinelStreamNameAttribute, eventMapping.streamName)
		return true
	}

	return false
}

// missingRequiredColumns returns the names of any commonRequiredColumns
// that are not populated (or are populated with a nil value) in the new
// body.
func missingRequiredColumns(body map[string]any) []string {
	var missing []string
	for _, col := range commonRequiredColumns {
		v, ok := body[col]
		if !ok || v == nil {
			missing = append(missing, col)
		}
	}
	return missing
}
