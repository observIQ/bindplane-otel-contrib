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
	"fmt"

	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
)

// Supported ASIM target tables.
const (
	TargetTableAuthentication         = "ASimAuthenticationEventLogs"
	TargetTableNetworkSession         = "ASimNetworkSessionLogs"
	TargetTableDNSActivity            = "ASimDnsActivityLogs"
	TargetTableProcessEvent           = "ASimProcessEventLogs"
	TargetTableFileEvent              = "ASimFileEventLogs"
	TargetTableAuditEvent             = "ASimAuditEventLogs"
	TargetTableWebSession             = "ASimWebSessionLogs"
	TargetTableDhcpEvent              = "ASimDhcpEventLogs"
	TargetTableRegistryEvent          = "ASimRegistryEventLogs"
	TargetTableUserManagementActivity = "ASimUserManagementActivityLogs"
)

// eventSchemaByTargetTable maps an ASIM target table to its EventSchema value
// (the PascalCase identifier Microsoft documents for each ASIM schema).
var eventSchemaByTargetTable = map[string]string{
	TargetTableAuthentication:         "Authentication",
	TargetTableNetworkSession:         "NetworkSession",
	TargetTableDNSActivity:            "Dns",
	TargetTableProcessEvent:           "ProcessEvent",
	TargetTableFileEvent:              "FileEvent",
	TargetTableAuditEvent:             "AuditEvent",
	TargetTableWebSession:             "WebSession",
	TargetTableDhcpEvent:              "Dhcp",
	TargetTableRegistryEvent:          "RegistryEvent",
	TargetTableUserManagementActivity: "UserManagement",
}

// FieldMapping maps a source log field (resolved via an expr-lang `from`
// expression) to a target ASIM column name. If `from` is empty or evaluates
// to nil, `default` is used.
type FieldMapping struct {
	From    string `mapstructure:"from"`
	To      string `mapstructure:"to"`
	Default any    `mapstructure:"default,omitempty"`
}

// EventMapping describes how to transform a class of incoming logs into a
// specific ASIM target table.
type EventMapping struct {
	// Filter is an expr-lang boolean expression. The first event mapping
	// whose filter matches a record wins. Empty means match all.
	Filter string `mapstructure:"filter"`

	// TargetTable is the ASIM table name (e.g. "ASimAuthenticationEventLogs").
	// Must be one of the values in eventSchemaByTargetTable.
	TargetTable string `mapstructure:"target_table"`

	// FieldMappings translate source log fields to ASIM column names.
	FieldMappings []FieldMapping `mapstructure:"field_mappings"`
}

// Config is the configuration for the ASIM standardization processor.
type Config struct {
	// EventMappings is the ordered list of event mappings.
	EventMappings []EventMapping `mapstructure:"event_mappings"`
}

// IsKnownTargetTable returns true if the given string is a supported ASIM
// target table.
func IsKnownTargetTable(table string) bool {
	_, ok := eventSchemaByTargetTable[table]
	return ok
}

// Validate validates the processor configuration.
func (cfg Config) Validate() error {
	for i, em := range cfg.EventMappings {
		if em.TargetTable == "" {
			return fmt.Errorf("event_mappings[%d]: target_table is required", i)
		}
		if !IsKnownTargetTable(em.TargetTable) {
			return fmt.Errorf("event_mappings[%d]: unknown target_table %q", i, em.TargetTable)
		}

		if em.Filter != "" {
			if _, err := expr.CreateBoolExpression(em.Filter); err != nil {
				return fmt.Errorf("event_mappings[%d]: invalid filter expression: %w", i, err)
			}
		}

		for j, fm := range em.FieldMappings {
			if fm.To == "" {
				return fmt.Errorf("event_mappings[%d].field_mappings[%d]: to is required", i, j)
			}
			if fm.From == "" && fm.Default == nil {
				return fmt.Errorf("event_mappings[%d].field_mappings[%d]: must have either from or default set", i, j)
			}
			if fm.From != "" {
				if _, err := expr.CreateValueExpression(fm.From); err != nil {
					return fmt.Errorf("event_mappings[%d].field_mappings[%d]: invalid from expression: %w", i, j, err)
				}
			}
		}
	}

	return nil
}
