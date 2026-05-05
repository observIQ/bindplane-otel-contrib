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
	logger        *zap.Logger
	eventMappings []compiledEventMapping
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

	return &asimStandardizationProcessor{
		logger:        logger,
		eventMappings: compiled,
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
		if originalBody != nil {
			newBody[additionalFieldsColumn] = originalBody
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
