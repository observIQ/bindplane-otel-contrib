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
	TargetTableDnsActivity            = "ASimDnsActivityLogs"
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
	TargetTableDnsActivity:            "Dns",
	TargetTableProcessEvent:           "ProcessEvent",
	TargetTableFileEvent:              "FileEvent",
	TargetTableAuditEvent:             "AuditEvent",
	TargetTableWebSession:             "WebSession",
	TargetTableDhcpEvent:              "Dhcp",
	TargetTableRegistryEvent:          "RegistryEvent",
	TargetTableUserManagementActivity: "UserManagement",
}

// commonRequiredColumns are required for every ASIM stream regardless of
// target table. These match the fields the Sentinel ASIM parsers always
// expect to be present.
var commonRequiredColumns = []string{
	"TimeGenerated",
	"EventCount",
	"EventStartTime",
	"EventEndTime",
	"EventType",
	"EventResult",
	"EventProduct",
	"EventVendor",
	"EventSchema",
	"EventSchemaVersion",
	"Dvc",
}

// permissibleColumnsByTargetTable lists the full set of columns declared for
// each Custom-ASim* DCR stream. Sourced from the bindplane-op-enterprise
// dcr-asim-*.json templates' streamDeclarations.<stream>.columns.
//
// The processor treats all entries here as "permissible" (allowed) but only
// the entries also present in commonRequiredColumns are validated as required
// when runtime_validation is enabled. Per-table required-column whitelists
// are not currently distinguished — every declared column is treated as
// permissible, every commonRequiredColumns entry is required.
var permissibleColumnsByTargetTable = map[string][]string{
	TargetTableAuthentication: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventResult", "EventResultDetails", "EventProduct",
		"EventVendor", "EventSchema", "EventSchemaVersion", "EventSeverity",
		"Dvc", "DvcHostname", "TargetUsername", "ActorUsername", "SrcIpAddr",
		"LogonMethod",
	},
	TargetTableNetworkSession: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventResult", "EventProduct", "EventVendor",
		"EventSchema", "EventSchemaVersion", "EventSeverity", "Dvc",
		"DvcHostname", "DvcIpAddr", "SrcIpAddr", "SrcHostname", "SrcPortNumber",
		"DstIpAddr", "DstHostname", "DstPortNumber", "NetworkProtocol",
		"NetworkApplicationProtocol", "NetworkBytes", "NetworkPackets",
		"SrcBytes", "DstBytes", "NetworkDirection", "NetworkRuleName",
		"DvcAction",
	},
	TargetTableDnsActivity: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventSubType", "EventResult", "EventResultDetails",
		"EventProduct", "EventVendor", "EventSchema", "EventSchemaVersion",
		"EventSeverity", "Dvc", "DvcHostname", "DvcIpAddr", "SrcIpAddr",
		"SrcHostname", "SrcPortNumber", "DstIpAddr", "DstPortNumber",
		"NetworkProtocol", "DnsQuery", "DnsQueryType", "DnsQueryTypeName",
		"DnsResponseCode", "DnsResponseCodeName", "DnsResponseName",
		"TransactionIdHex",
	},
	TargetTableProcessEvent: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventResult", "EventProduct", "EventVendor",
		"EventSchema", "EventSchemaVersion", "EventSeverity", "Dvc",
		"DvcHostname", "DvcIpAddr", "TargetProcessName",
		"TargetProcessCommandLine", "TargetProcessId", "TargetProcessGuid",
		"TargetProcessCreationTime", "TargetProcessFileMD5",
		"TargetProcessFileSHA1", "TargetProcessFileSHA256",
		"TargetProcessCurrentDirectory", "TargetUsername", "ActingProcessName",
		"ActingProcessCommandLine", "ActingProcessId", "ActingProcessGuid",
		"ActorUsername", "User",
	},
	TargetTableFileEvent: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventResult", "EventProduct", "EventVendor",
		"EventSchema", "EventSchemaVersion", "EventSeverity", "Dvc",
		"DvcHostname", "DvcIpAddr", "ActorUsername", "ActingProcessName",
		"ActingProcessId", "TargetFilePath", "TargetFileName",
		"TargetFileExtension", "TargetFileSize", "TargetFileMD5",
		"TargetFileSHA1", "TargetFileSHA256", "SrcFilePath", "SrcFileName",
	},
	TargetTableAuditEvent: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventSubType", "EventResult", "EventResultDetails",
		"EventProduct", "EventVendor", "EventSchema", "EventSchemaVersion",
		"EventSeverity", "Dvc", "DvcHostname", "DvcIpAddr", "ActorUsername",
		"ActorUserType", "Operation", "Object", "ObjectType", "OldValue",
		"NewValue", "HttpUserAgent", "SrcIpAddr",
	},
	TargetTableWebSession: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventSubType", "EventResult", "EventResultDetails",
		"EventProduct", "EventVendor", "EventSchema", "EventSchemaVersion",
		"EventSeverity", "Dvc", "DvcHostname", "DvcIpAddr", "SrcIpAddr",
		"SrcHostname", "SrcPortNumber", "DstIpAddr", "DstHostname",
		"DstPortNumber", "NetworkProtocol", "NetworkBytes", "SrcBytes",
		"DstBytes", "Url", "UrlCategory", "HttpVersion", "HttpRequestMethod",
		"HttpStatusCode", "HttpUserAgent", "HttpReferrer", "HttpContentType",
		"HttpResponseTime", "FileName", "DvcAction",
	},
	TargetTableDhcpEvent: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventResult", "EventResultDetails", "EventProduct",
		"EventVendor", "EventSchema", "EventSchemaVersion", "EventSeverity",
		"Dvc", "DvcHostname", "DvcIpAddr", "SrcIpAddr", "SrcMacAddr",
		"SrcHostname", "DstIpAddr", "DstMacAddr", "DstPortNumber",
		"DhcpLeaseDuration", "DhcpOfferedIpAddr", "DhcpRequestedIpAddr",
	},
	TargetTableRegistryEvent: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventResult", "EventProduct", "EventVendor",
		"EventSchema", "EventSchemaVersion", "EventSeverity", "Dvc",
		"DvcHostname", "DvcIpAddr", "ActorUsername", "ActingProcessName",
		"ActingProcessId", "ActingProcessGuid", "RegistryKey",
		"RegistryPreviousKey", "RegistryValue", "RegistryValueType",
		"RegistryValueData", "RegistryPreviousValueData",
	},
	TargetTableUserManagementActivity: {
		"TimeGenerated", "EventCount", "EventStartTime", "EventEndTime",
		"EventType", "EventResult", "EventProduct", "EventVendor",
		"EventSchema", "EventSchemaVersion", "EventSeverity", "Dvc",
		"DvcHostname", "DvcIpAddr", "ActorUsername", "TargetUsername",
		"TargetUserType", "TargetUsernameType", "GroupName", "GroupType",
		"SrcIpAddr",
	},
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

	// RuntimeValidation, when true, verifies that all required ASIM columns
	// are present in the transformed body. Missing columns are logged at
	// debug level but the record is NOT dropped.
	RuntimeValidation *bool `mapstructure:"runtime_validation"`
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
