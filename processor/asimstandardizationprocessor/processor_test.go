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
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// authInputBody returns a body with all fields needed to populate the
// commonRequiredColumns when used with minimalAuthFieldMappings.
func authInputBody() map[string]any {
	return map[string]any{
		"time":           "2024-01-01T00:00:00Z",
		"count":          int64(1),
		"start":          "2024-01-01T00:00:00Z",
		"end":            "2024-01-01T00:00:00Z",
		"type":           "Logon",
		"result":         "Success",
		"product":        "WindowsSecurity",
		"vendor":         "Microsoft",
		"schema_version": "0.1.3",
		"dvc":            "host-01",
		"user":           "alice",
	}
}

func newProcessor(t *testing.T, cfg *Config) *asimStandardizationProcessor {
	t.Helper()
	p, err := newASIMStandardizationProcessor(zap.NewNop(), cfg)
	require.NoError(t, err)
	return p
}

func newLogsWithBody(body map[string]any) plog.Logs {
	ld := plog.NewLogs()
	record := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	_ = record.Body().SetEmptyMap().FromRaw(body)
	return ld
}

func firstRecord(t *testing.T, ld plog.Logs) plog.LogRecord {
	t.Helper()
	require.Equal(t, 1, ld.ResourceLogs().Len())
	rl := ld.ResourceLogs().At(0)
	require.Equal(t, 1, rl.ScopeLogs().Len())
	sl := rl.ScopeLogs().At(0)
	require.Equal(t, 1, sl.LogRecords().Len())
	return sl.LogRecords().At(0)
}

func countLogRecords(ld plog.Logs) int {
	count := 0
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		for j := 0; j < ld.ResourceLogs().At(i).ScopeLogs().Len(); j++ {
			count += ld.ResourceLogs().At(i).ScopeLogs().At(j).LogRecords().Len()
		}
	}
	return count
}

func TestProcessLogs_FilterMatch(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				Filter:        `body.type == "Logon"`,
				TargetTable:   TargetTableAuthentication,
				FieldMappings: minimalAuthFieldMappings,
			},
		},
	})

	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)
	require.Equal(t, 1, countLogRecords(out))

	rec := firstRecord(t, out)
	body := rec.Body().Map().AsRaw()
	require.Equal(t, "Logon", body["EventType"])
	require.Equal(t, "Authentication", body["EventSchema"])

	stream, ok := rec.Attributes().Get(sentinelStreamNameAttribute)
	require.True(t, ok)
	require.Equal(t, "Custom-ASimAuthenticationEventLogs", stream.Str())

	schema, ok := rec.Attributes().Get(eventSchemaAttribute)
	require.True(t, ok)
	require.Equal(t, "Authentication", schema.Str())
}

func TestProcessLogs_FilterNoMatch_DropsRecord(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				Filter:        `body.type == "DoesNotMatch"`,
				TargetTable:   TargetTableAuthentication,
				FieldMappings: minimalAuthFieldMappings,
			},
		},
	})

	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)
	require.Equal(t, 0, out.ResourceLogs().Len(), "expected empty resource logs")
}

func TestProcessLogs_FieldMappingFromExpression(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{From: "body.user", To: "TargetUsername"},
				},
			},
		},
	})

	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)

	body := firstRecord(t, out).Body().Map().AsRaw()
	require.Equal(t, "alice", body["TargetUsername"])
	require.Equal(t, "Authentication", body["EventSchema"])
}

func TestProcessLogs_DefaultFallback(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{From: "body.user", To: "TargetUsername"},
					{From: "body.missing", To: "EventResult", Default: "Success"},
					{To: "EventVendor", Default: "Microsoft"},
				},
			},
		},
	})

	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)

	body := firstRecord(t, out).Body().Map().AsRaw()
	require.Equal(t, "alice", body["TargetUsername"])
	require.Equal(t, "Success", body["EventResult"])
	require.Equal(t, "Microsoft", body["EventVendor"])
}

func TestProcessLogs_EventSchemaPerTargetTable(t *testing.T) {
	cases := []struct {
		table  string
		schema string
		stream string
	}{
		{TargetTableAuthentication, "Authentication", "Custom-ASimAuthenticationEventLogs"},
		{TargetTableNetworkSession, "NetworkSession", "Custom-ASimNetworkSessionLogs"},
		{TargetTableDnsActivity, "Dns", "Custom-ASimDnsActivityLogs"},
		{TargetTableProcessEvent, "ProcessEvent", "Custom-ASimProcessEventLogs"},
		{TargetTableFileEvent, "FileEvent", "Custom-ASimFileEventLogs"},
		{TargetTableAuditEvent, "AuditEvent", "Custom-ASimAuditEventLogs"},
		{TargetTableWebSession, "WebSession", "Custom-ASimWebSessionLogs"},
		{TargetTableDhcpEvent, "Dhcp", "Custom-ASimDhcpEventLogs"},
		{TargetTableRegistryEvent, "RegistryEvent", "Custom-ASimRegistryEventLogs"},
		{TargetTableUserManagementActivity, "UserManagement", "Custom-ASimUserManagementActivityLogs"},
	}

	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			p := newProcessor(t, &Config{
				EventMappings: []EventMapping{
					{
						TargetTable: tc.table,
						FieldMappings: []FieldMapping{
							{To: "EventType", Default: "x"},
						},
					},
				},
			})

			out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
			require.NoError(t, err)

			rec := firstRecord(t, out)

			stream, ok := rec.Attributes().Get(sentinelStreamNameAttribute)
			require.True(t, ok)
			require.Equal(t, tc.stream, stream.Str())

			schema, ok := rec.Attributes().Get(eventSchemaAttribute)
			require.True(t, ok)
			require.Equal(t, tc.schema, schema.Str())

			require.Equal(t, tc.schema, rec.Body().Map().AsRaw()["EventSchema"])
		})
	}
}

func TestProcessLogs_FirstMatchingMappingWins(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				Filter:      "true",
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{To: "EventType", Default: "first"},
				},
			},
			{
				Filter:      "true",
				TargetTable: TargetTableNetworkSession,
				FieldMappings: []FieldMapping{
					{To: "EventType", Default: "second"},
				},
			},
		},
	})

	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)

	rec := firstRecord(t, out)
	require.Equal(t, "first", rec.Body().Map().AsRaw()["EventType"])

	stream, ok := rec.Attributes().Get(sentinelStreamNameAttribute)
	require.True(t, ok)
	require.Equal(t, "Custom-ASimAuthenticationEventLogs", stream.Str())
}

func TestProcessLogs_ResourceAttributesAccessible(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				Filter:      `resource.host == "web-01"`,
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{From: "resource.host", To: "Dvc"},
				},
			},
		},
	})

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("host", "web-01")
	record := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	_ = record.Body().SetEmptyMap().FromRaw(authInputBody())

	out, err := p.processLogs(context.Background(), ld)
	require.NoError(t, err)

	body := firstRecord(t, out).Body().Map().AsRaw()
	require.Equal(t, "web-01", body["Dvc"])
}

func TestProcessLogs_RuntimeValidation_LogsMissingButKeepsRecord(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	enabled := true
	p, err := newASIMStandardizationProcessor(logger, &Config{
		RuntimeValidation: &enabled,
		EventMappings: []EventMapping{
			{
				TargetTable: TargetTableAuthentication,
				// Intentionally omit most required columns.
				FieldMappings: []FieldMapping{
					{From: "body.user", To: "TargetUsername"},
				},
			},
		},
	})
	require.NoError(t, err)

	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)
	require.Equal(t, 1, countLogRecords(out), "record must NOT be dropped on validation failure")

	// A debug-level log must mention missing required columns.
	entries := observed.FilterMessage("ASIM record missing required columns").All()
	require.NotEmpty(t, entries, "expected debug log for missing required columns")
}

func TestProcessLogs_RuntimeValidation_AllRequiredPresent_NoLog(t *testing.T) {
	core, observed := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	enabled := true
	p, err := newASIMStandardizationProcessor(logger, &Config{
		RuntimeValidation: &enabled,
		EventMappings: []EventMapping{
			{
				TargetTable:   TargetTableAuthentication,
				FieldMappings: minimalAuthFieldMappings,
			},
		},
	})
	require.NoError(t, err)

	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)
	require.Equal(t, 1, countLogRecords(out))

	entries := observed.FilterMessage("ASIM record missing required columns").All()
	require.Empty(t, entries, "no missing-column warning expected when all required cols present")
}

func TestProcessLogs_RuntimeValidationDefaultsToFalse(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{From: "body.user", To: "TargetUsername"},
				},
			},
		},
	})
	require.False(t, p.runtimeValidation)
}

func TestProcessLogs_NoMatchingEventMapping_DropsRecord(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				Filter:      `body.never == true`,
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{To: "EventType", Default: "x"},
				},
			},
		},
	})

	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)
	require.Equal(t, 0, countLogRecords(out))
}

func TestProcessLogs_DropsEmptyResourceAndScope(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				Filter:      `body.keep == true`,
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{To: "EventType", Default: "x"},
				},
			},
		},
	})

	ld := plog.NewLogs()
	// Resource 1: dropped (no matching record).
	rl1 := ld.ResourceLogs().AppendEmpty()
	rec1 := rl1.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	body1 := authInputBody()
	body1["keep"] = false
	_ = rec1.Body().SetEmptyMap().FromRaw(body1)
	// Resource 2: kept.
	rl2 := ld.ResourceLogs().AppendEmpty()
	rec2 := rl2.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	body2 := authInputBody()
	body2["keep"] = true
	_ = rec2.Body().SetEmptyMap().FromRaw(body2)

	out, err := p.processLogs(context.Background(), ld)
	require.NoError(t, err)
	require.Equal(t, 1, out.ResourceLogs().Len())
	require.Equal(t, 1, countLogRecords(out))
}

func TestNewASIMStandardizationProcessor_UnknownTargetTable(t *testing.T) {
	_, err := newASIMStandardizationProcessor(zap.NewNop(), &Config{
		EventMappings: []EventMapping{
			{TargetTable: "ASimNotARealTable"},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown target_table")
}

func TestNewASIMStandardizationProcessor_InvalidFromExpression(t *testing.T) {
	_, err := newASIMStandardizationProcessor(zap.NewNop(), &Config{
		EventMappings: []EventMapping{
			{
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{From: "|||invalid|||", To: "TargetUsername"},
				},
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "compiling from expression")
}

func TestNewASIMStandardizationProcessor_InvalidFilterExpression(t *testing.T) {
	_, err := newASIMStandardizationProcessor(zap.NewNop(), &Config{
		EventMappings: []EventMapping{
			{
				Filter:      "|||invalid|||",
				TargetTable: TargetTableAuthentication,
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "compiling filter expression")
}

func TestMissingRequiredColumns(t *testing.T) {
	body := map[string]any{
		"TimeGenerated": "x",
		"EventCount":    1,
	}
	missing := missingRequiredColumns(body, []string{
		"TimeGenerated", "EventCount", "EventType", "EventResult",
	})
	require.ElementsMatch(t, []string{"EventType", "EventResult"}, missing)

	require.Empty(t, missingRequiredColumns(body, []string{"TimeGenerated"}))
}
