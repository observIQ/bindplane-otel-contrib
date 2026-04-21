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
	"fmt"

	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
	"go.opentelemetry.io/collector/component/componenttest"
)

// SentinelFieldRule is a single conditional rule that, when its OTTL condition
// matches, sets Sentinel routing attributes on a log record.
type SentinelFieldRule struct {
	// Condition is an OTTL boolean expression evaluated against each log
	// record. The first rule whose condition evaluates to true wins; rules
	// are evaluated in order. An empty condition is treated as "true".
	Condition string `mapstructure:"condition"`

	// StreamName is the DCR stream name (e.g. Custom-MyTable_CL or an ASim*
	// stream) to set on matched records.
	StreamName string `mapstructure:"stream_name"`

	// RuleID is an optional Data Collection Rule immutable ID that
	// overrides the exporter's configured rule ID when set.
	RuleID string `mapstructure:"rule_id"`

	// IngestionLabels is an optional map of labels to set on matched
	// records. Each key/value pair is written to
	// attributes["sentinel_ingestion_label[\"<key>\"]"].
	IngestionLabels map[string]string `mapstructure:"ingestion_labels"`
}

// Config is the configuration for the sentinel standardization processor.
type Config struct {
	// SentinelField is the ordered list of conditional rules. The first
	// rule whose condition matches a log record wins.
	SentinelField []SentinelFieldRule `mapstructure:"sentinel_field"`
}

// Validate validates the processor configuration.
func (cfg *Config) Validate() error {
	// We validate OTTL expressions using a nop telemetry settings instance.
	// The real telemetry settings are only available at factory time, but
	// the parser does not depend on them for detecting syntax errors.
	set := componenttest.NewNopTelemetrySettings()

	for i, rule := range cfg.SentinelField {
		if rule.StreamName == "" {
			return fmt.Errorf("sentinel_field[%d]: stream_name is required", i)
		}

		condition := rule.Condition
		if condition == "" {
			condition = "true"
		}

		if _, err := expr.NewOTTLLogRecordCondition(condition, set); err != nil {
			return fmt.Errorf("sentinel_field[%d]: invalid condition: %w", i, err)
		}
	}

	return nil
}
