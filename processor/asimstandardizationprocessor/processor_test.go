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
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// authInputBody returns a body with all fields needed to populate
// minimalAuthFieldMappings.
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
	// Default to runtime_validation off in tests so existing fixtures with
	// minimal field mappings aren't auto-dropped. Validation-specific tests
	// enable it explicitly via the Config.
	if cfg.RuntimeValidation == nil {
		off := false
		cfg.RuntimeValidation = &off
	}
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
		{TargetTableDNSActivity, "Dns", "Custom-ASimDnsActivityLogs"},
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

func TestRuntimeValidation_DefaultsToOn(t *testing.T) {
	p, err := newASIMStandardizationProcessor(zap.NewNop(), &Config{
		EventMappings: []EventMapping{
			{
				TargetTable:   TargetTableAuthentication,
				FieldMappings: minimalAuthFieldMappings,
			},
		},
	})
	require.NoError(t, err)
	require.True(t, p.runtimeValidation, "RuntimeValidation should default to true")
}

func TestRuntimeValidation_DropsRecordWhenMandatoryMissing(t *testing.T) {
	on := true
	p, err := newASIMStandardizationProcessor(zap.NewNop(), &Config{
		RuntimeValidation: &on,
		EventMappings: []EventMapping{
			{
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{From: "body.user", To: "TargetUsername"},
					// Intentionally omit all other mandatory cols.
				},
			},
		},
	})
	require.NoError(t, err)

	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)
	require.Equal(t, 0, countLogRecords(out), "record missing mandatory cols must drop when validation enabled")
}

func TestRuntimeValidation_KeepsRecordWhenAllMandatoryPresent(t *testing.T) {
	on := true
	p, err := newASIMStandardizationProcessor(zap.NewNop(), &Config{
		RuntimeValidation: &on,
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
}

func TestTypeCoercion_DateTimeStringIsParsed(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{To: "TimeGenerated", Default: "2026-04-29T01:10:00Z"},
				},
			},
		},
	})
	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)
	body := firstRecord(t, out).Body().Map().AsRaw()
	got, ok := body["TimeGenerated"].(string)
	require.True(t, ok, "TimeGenerated should remain a string after coercion")
	// Coerce normalises to RFC3339Nano.
	require.Contains(t, got, "2026-04-29T01:10:00")
}

func TestTypeCoercion_HexProcessIDStringIsParsedToInt(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				TargetTable: TargetTableProcessEvent,
				FieldMappings: []FieldMapping{
					{To: "EventCount", Default: "0x10"},
				},
			},
		},
	})
	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)
	body := firstRecord(t, out).Body().Map().AsRaw()
	require.EqualValues(t, 16, body["EventCount"])
}

func TestTypeCoercion_BadValueDropsField(t *testing.T) {
	p := newProcessor(t, &Config{
		EventMappings: []EventMapping{
			{
				TargetTable: TargetTableAuthentication,
				FieldMappings: []FieldMapping{
					{To: "EventCount", Default: "not-a-number"},
				},
			},
		},
	})
	out, err := p.processLogs(context.Background(), newLogsWithBody(authInputBody()))
	require.NoError(t, err)
	body := firstRecord(t, out).Body().Map().AsRaw()
	_, present := body["EventCount"]
	require.False(t, present, "uncoercible value should drop field")
}

func TestCoerce_IntRejectsValuesOutsideInt32(t *testing.T) {
	// Microsoft KQL int is 32-bit; values above 2^31-1 must be rejected so
	// Azure ingest doesn't truncate or reject the batch.
	_, ok := coerceValue(int64(math.MaxInt32)+1, ColInt)
	require.False(t, ok, "int32 overflow must drop field")
	_, ok = coerceValue(int64(math.MinInt32)-1, ColInt)
	require.False(t, ok, "int32 underflow must drop field")

	v, ok := coerceValue(int64(math.MaxInt32), ColInt)
	require.True(t, ok, "max int32 must round-trip")
	require.EqualValues(t, math.MaxInt32, v)
}

func TestCoerce_LongAcceptsLargeValues(t *testing.T) {
	v, ok := coerceValue(int64(math.MaxInt64), ColLong)
	require.True(t, ok)
	require.EqualValues(t, int64(math.MaxInt64), v)
}

func TestCoerce_DateTimeRejectsNumericEpoch(t *testing.T) {
	// Numeric epoch values are deliberately unsupported because the unit
	// (s/ms/µs/ns) can't be inferred from magnitude alone. Mappings should
	// pre-format with an explicit layout.
	for _, v := range []any{int64(1700000000), int(1700000000), uint64(1700000000)} {
		_, ok := coerceValue(v, ColDateTime)
		require.False(t, ok, "numeric epoch must be rejected by coerceDateTime")
	}
}

func TestCoerce_StringJSONMarshalsComposites(t *testing.T) {
	got, ok := coerceValue([]any{"a", 1, true}, ColString)
	require.True(t, ok)
	require.Equal(t, `["a",1,true]`, got)

	got, ok = coerceValue(map[string]any{"k": "v"}, ColString)
	require.True(t, ok)
	require.Equal(t, `{"k":"v"}`, got)
}

func TestCoerce_StringPrimitivesUseSprint(t *testing.T) {
	got, ok := coerceValue(int64(42), ColString)
	require.True(t, ok)
	require.Equal(t, "42", got)

	got, ok = coerceValue(true, ColString)
	require.True(t, ok)
	require.Equal(t, "true", got)
}
