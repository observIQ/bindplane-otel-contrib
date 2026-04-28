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

// Attribute names the processor writes on transformed log records. The
// Microsoft Sentinel (Azure Log Analytics) exporter consumes
// sentinel_stream_name to route each record to a specific DCR stream.
const (
	sentinelStreamNameAttribute = "sentinel_stream_name"
	eventSchemaAttribute        = "EventSchema"

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
	requiredCols  []string
}

type asimStandardizationProcessor struct {
	logger            *zap.Logger
	eventMappings     []compiledEventMapping
	runtimeValidation bool
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
			requiredCols:  commonRequiredColumns,
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

	runtimeValidation := false
	if config.RuntimeValidation != nil {
		runtimeValidation = *config.RuntimeValidation
	}

	return &asimStandardizationProcessor{
		logger:            logger,
		eventMappings:     compiled,
		runtimeValidation: runtimeValidation,
	}, nil
}

func (asp *asimStandardizationProcessor) processLogs(_ context.Context, ld plog.Logs) (plog.Logs, error) {
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		resource := ld.ResourceLogs().At(i)
		resourceAttrs := resource.Resource().Attributes().AsRaw()
		for j := 0; j < resource.ScopeLogs().Len(); j++ {
			scope := resource.ScopeLogs().At(j)
			scope.LogRecords().RemoveIf(func(log plog.LogRecord) bool {
				shouldDrop := !asp.processLogRecord(log, resourceAttrs)
				if shouldDrop {
					asp.logger.Debug("Dropping log record", zap.String("reason", "no match"))
				}
				return shouldDrop
			})
		}
		resource.ScopeLogs().RemoveIf(func(scope plog.ScopeLogs) bool {
			records := scope.LogRecords().Len()
			if records == 0 {
				asp.logger.Debug("Dropping scope", zap.String("reason", "no records"))
			}
			return records == 0
		})
	}
	ld.ResourceLogs().RemoveIf(func(resource plog.ResourceLogs) bool {
		scopes := resource.ScopeLogs().Len()
		if scopes == 0 {
			asp.logger.Debug("Dropping resource", zap.String("reason", "no scopes"))
		}
		return scopes == 0
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

		newBody := map[string]any{}

		for _, fieldMapping := range eventMapping.fieldMappings {
			var value any
			if fieldMapping.from != nil {
				val, err := fieldMapping.from.Evaluate(record)
				if err != nil || val == nil {
					if err != nil {
						asp.logger.Error("Failed to evaluate expression",
							zap.String("field", fieldMapping.to),
							zap.Error(err),
						)
					}
					if val == nil {
						asp.logger.Debug(
							"No value found for field, using default",
							zap.String("field", fieldMapping.to),
							zap.Any("default", fieldMapping.defaultValue),
						)
					}
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

		// Always set EventSchema for ASIM-compliance and routing.
		newBody[eventSchemaAttribute] = eventMapping.eventSchema

		if asp.runtimeValidation {
			if missing := missingRequiredColumns(newBody, eventMapping.requiredCols); len(missing) > 0 {
				asp.logger.Debug("ASIM record missing required columns",
					zap.String("target_table", eventMapping.targetTable),
					zap.Strings("missing", missing),
				)
			}
		}

		if err := log.Body().SetEmptyMap().FromRaw(newBody); err != nil {
			asp.logger.Error("failed to set log body",
				zap.Error(err),
				zap.String("target_table", eventMapping.targetTable),
			)
			return false
		}

		attrs := log.Attributes()
		attrs.PutStr(sentinelStreamNameAttribute, eventMapping.streamName)
		attrs.PutStr(eventSchemaAttribute, eventMapping.eventSchema)

		return true
	}

	return false
}

// missingRequiredColumns returns the list of required columns that are not
// present (as keys) in the body map.
func missingRequiredColumns(body map[string]any, required []string) []string {
	var missing []string
	for _, col := range required {
		if _, ok := body[col]; !ok {
			missing = append(missing, col)
		}
	}
	return missing
}
