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
	"testing"

	"github.com/stretchr/testify/require"
)

// minimalAuthFieldMappings provides enough field mappings to populate the
// commonRequiredColumns for the Authentication target table.
var minimalAuthFieldMappings = []FieldMapping{
	{From: "body.time", To: "TimeGenerated"},
	{From: "body.count", To: "EventCount"},
	{From: "body.start", To: "EventStartTime"},
	{From: "body.end", To: "EventEndTime"},
	{From: "body.type", To: "EventType"},
	{From: "body.result", To: "EventResult"},
	{From: "body.product", To: "EventProduct"},
	{From: "body.vendor", To: "EventVendor"},
	{From: "body.schema_version", To: "EventSchemaVersion"},
	{From: "body.dvc", To: "Dvc"},
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "valid config",
			cfg: Config{
				EventMappings: []EventMapping{
					{
						Filter:        "true",
						TargetTable:   TargetTableAuthentication,
						FieldMappings: minimalAuthFieldMappings,
					},
				},
			},
		},
		{
			name: "valid empty config",
			cfg:  Config{},
		},
		{
			name: "valid for every supported target table",
			cfg: Config{
				EventMappings: []EventMapping{
					{TargetTable: TargetTableAuthentication, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: TargetTableNetworkSession, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: TargetTableDNSActivity, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: TargetTableProcessEvent, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: TargetTableFileEvent, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: TargetTableAuditEvent, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: TargetTableWebSession, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: TargetTableDhcpEvent, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: TargetTableRegistryEvent, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: TargetTableUserManagementActivity, FieldMappings: minimalAuthFieldMappings},
				},
			},
		},
		{
			name: "missing target_table",
			cfg: Config{
				EventMappings: []EventMapping{
					{FieldMappings: minimalAuthFieldMappings},
				},
			},
			wantErr: "event_mappings[0]: target_table is required",
		},
		{
			name: "unknown target_table",
			cfg: Config{
				EventMappings: []EventMapping{
					{TargetTable: "ASimNotARealTable"},
				},
			},
			wantErr: `event_mappings[0]: unknown target_table "ASimNotARealTable"`,
		},
		{
			name: "invalid filter expression",
			cfg: Config{
				EventMappings: []EventMapping{
					{
						Filter:      "|||invalid|||",
						TargetTable: TargetTableAuthentication,
					},
				},
			},
			wantErr: "event_mappings[0]: invalid filter expression",
		},
		{
			name: "field mapping missing to",
			cfg: Config{
				EventMappings: []EventMapping{
					{
						TargetTable: TargetTableAuthentication,
						FieldMappings: []FieldMapping{
							{From: "body.x"},
						},
					},
				},
			},
			wantErr: "event_mappings[0].field_mappings[0]: to is required",
		},
		{
			name: "field mapping missing both from and default",
			cfg: Config{
				EventMappings: []EventMapping{
					{
						TargetTable: TargetTableAuthentication,
						FieldMappings: []FieldMapping{
							{To: "EventType"},
						},
					},
				},
			},
			wantErr: "event_mappings[0].field_mappings[0]: must have either from or default set",
		},
		{
			name: "invalid from expression",
			cfg: Config{
				EventMappings: []EventMapping{
					{
						TargetTable: TargetTableAuthentication,
						FieldMappings: []FieldMapping{
							{From: "|||invalid|||", To: "EventType"},
						},
					},
				},
			},
			wantErr: "event_mappings[0].field_mappings[0]: invalid from expression",
		},
		{
			name: "default-only field mapping is valid",
			cfg: Config{
				EventMappings: []EventMapping{
					{
						TargetTable: TargetTableAuthentication,
						FieldMappings: []FieldMapping{
							{To: "EventType", Default: "Logon"},
						},
					},
				},
			},
		},
		{
			name: "error in second event mapping",
			cfg: Config{
				EventMappings: []EventMapping{
					{TargetTable: TargetTableAuthentication, FieldMappings: minimalAuthFieldMappings},
					{TargetTable: ""},
				},
			},
			wantErr: "event_mappings[1]: target_table is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestIsKnownTargetTable(t *testing.T) {
	require.True(t, IsKnownTargetTable(TargetTableAuthentication))
	require.True(t, IsKnownTargetTable(TargetTableUserManagementActivity))
	require.False(t, IsKnownTargetTable(""))
	require.False(t, IsKnownTargetTable("ASimNotReal"))
}
