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

package sentinelstandardizationprocessor

import (
	"context"
	"fmt"

	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottllog"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor"
	"go.uber.org/zap"
)

// Attribute names the processor writes on matched log records. The Azure Log
// Analytics (Microsoft Sentinel) exporter consumes these attributes to route
// each record to a specific DCR stream/rule.
const (
	sentinelStreamNameAttribute     = "sentinel_stream_name"
	sentinelRuleIDAttribute         = "sentinel_rule_id"
	sentinelIngestionLabelKeyFormat = `sentinel_ingestion_label["%s"]`
)

// compiledRule is a SentinelFieldRule with its OTTL condition pre-compiled.
type compiledRule struct {
	condition       *expr.OTTLCondition[*ottllog.TransformContext]
	streamName      string
	ruleID          string
	ingestionLabels map[string]string
}

type sentinelStandardizationProcessor struct {
	logger *zap.Logger
	rules  []compiledRule
}

// newSentinelStandardizationProcessor compiles the processor's configuration
// into a processor instance.
func newSentinelStandardizationProcessor(set processor.Settings, cfg *Config) (*sentinelStandardizationProcessor, error) {
	rules := make([]compiledRule, 0, len(cfg.SentinelField))
	for i, rule := range cfg.SentinelField {
		condition := rule.Condition
		if condition == "" {
			condition = "true"
		}

		compiled, err := expr.NewOTTLLogRecordCondition(condition, set.TelemetrySettings)
		if err != nil {
			return nil, fmt.Errorf("compiling sentinel_field[%d] condition: %w", i, err)
		}

		rules = append(rules, compiledRule{
			condition:       compiled,
			streamName:      rule.StreamName,
			ruleID:          rule.RuleID,
			ingestionLabels: rule.IngestionLabels,
		})
	}

	return &sentinelStandardizationProcessor{
		logger: set.Logger,
		rules:  rules,
	}, nil
}

// processLogs applies the configured rules to each log record. On the first
// rule whose condition matches, the record's attributes are updated with the
// rule's stream name, rule ID (if set), and ingestion labels. Records that
// match no rule are left untouched.
func (sp *sentinelStandardizationProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	if len(sp.rules) == 0 {
		return ld, nil
	}

	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		rl := ld.ResourceLogs().At(i)
		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			sl := rl.ScopeLogs().At(j)
			for k := 0; k < sl.LogRecords().Len(); k++ {
				sp.applyRules(ctx, rl, sl, sl.LogRecords().At(k))
			}
		}
	}
	return ld, nil
}

// applyRules evaluates the rules in order and applies the first match.
func (sp *sentinelStandardizationProcessor) applyRules(ctx context.Context, rl plog.ResourceLogs, sl plog.ScopeLogs, lr plog.LogRecord) {
	tCtx := ottllog.NewTransformContextPtr(rl, sl, lr)

	for idx, rule := range sp.rules {
		match, err := rule.condition.Match(ctx, tCtx)
		if err != nil {
			sp.logger.Error(
				"failed to evaluate sentinel_field condition",
				zap.Int("rule_index", idx),
				zap.Error(err),
			)
			continue
		}
		if !match {
			continue
		}

		attrs := lr.Attributes()
		attrs.PutStr(sentinelStreamNameAttribute, rule.streamName)
		if rule.ruleID != "" {
			attrs.PutStr(sentinelRuleIDAttribute, rule.ruleID)
		}
		for key, value := range rule.ingestionLabels {
			attrs.PutStr(fmt.Sprintf(sentinelIngestionLabelKeyFormat, key), value)
		}
		return
	}
}
