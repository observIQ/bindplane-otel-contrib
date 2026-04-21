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
	"testing"

	"github.com/observiq/bindplane-otel-contrib/processor/sentinelstandardizationprocessor/internal/metadata"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor/processortest"
)

// newLogs builds a plog.Logs with a single log record whose attributes are
// seeded from the given map. Attribute values are stored as strings to keep
// OTTL conditions simple and predictable.
func newLogs(attrs map[string]string) plog.Logs {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.Body().SetStr("test log")
	for k, v := range attrs {
		lr.Attributes().PutStr(k, v)
	}
	return logs
}

// firstLogRecord returns the first (and in these tests, only) log record.
func firstLogRecord(t *testing.T, ld plog.Logs) plog.LogRecord {
	t.Helper()
	require.Equal(t, 1, ld.ResourceLogs().Len())
	rl := ld.ResourceLogs().At(0)
	require.Equal(t, 1, rl.ScopeLogs().Len())
	sl := rl.ScopeLogs().At(0)
	require.Equal(t, 1, sl.LogRecords().Len())
	return sl.LogRecords().At(0)
}

// newProcessor compiles a processor instance from the given rules.
func newProcessor(t *testing.T, rules []SentinelFieldRule) *sentinelStandardizationProcessor {
	t.Helper()
	set := processortest.NewNopSettings(metadata.Type)
	sp, err := newSentinelStandardizationProcessor(set, &Config{SentinelField: rules})
	require.NoError(t, err)
	return sp
}

func TestProcessLogs_FirstMatchWins(t *testing.T) {
	sp := newProcessor(t, []SentinelFieldRule{
		{
			Condition:       `attributes["event.type"] == "auth"`,
			StreamName:      "Custom-Auth_CL",
			RuleID:          "dcr-auth",
			IngestionLabels: map[string]string{"env": "prod"},
		},
		{
			// This rule would also match, but the first rule wins.
			Condition:  `IsMatch(attributes["event.type"], "a.*")`,
			StreamName: "Custom-Other_CL",
			RuleID:     "dcr-other",
		},
	})

	out, err := sp.processLogs(context.Background(), newLogs(map[string]string{"event.type": "auth"}))
	require.NoError(t, err)

	attrs := firstLogRecord(t, out).Attributes()

	stream, ok := attrs.Get(sentinelStreamNameAttribute)
	require.True(t, ok)
	require.Equal(t, "Custom-Auth_CL", stream.Str())

	ruleID, ok := attrs.Get(sentinelRuleIDAttribute)
	require.True(t, ok)
	require.Equal(t, "dcr-auth", ruleID.Str())

	label, ok := attrs.Get(`sentinel_ingestion_label["env"]`)
	require.True(t, ok)
	require.Equal(t, "prod", label.Str())
}

func TestProcessLogs_SecondRuleMatches(t *testing.T) {
	sp := newProcessor(t, []SentinelFieldRule{
		{
			Condition:  `attributes["event.type"] == "auth"`,
			StreamName: "Custom-Auth_CL",
		},
		{
			Condition:  `attributes["event.type"] == "network"`,
			StreamName: "Custom-Network_CL",
			RuleID:     "dcr-network",
		},
	})

	out, err := sp.processLogs(context.Background(), newLogs(map[string]string{"event.type": "network"}))
	require.NoError(t, err)

	attrs := firstLogRecord(t, out).Attributes()

	stream, ok := attrs.Get(sentinelStreamNameAttribute)
	require.True(t, ok)
	require.Equal(t, "Custom-Network_CL", stream.Str())

	ruleID, ok := attrs.Get(sentinelRuleIDAttribute)
	require.True(t, ok)
	require.Equal(t, "dcr-network", ruleID.Str())
}

func TestProcessLogs_NoMatchLeavesAttributesUnchanged(t *testing.T) {
	sp := newProcessor(t, []SentinelFieldRule{
		{
			Condition:  `attributes["event.type"] == "auth"`,
			StreamName: "Custom-Auth_CL",
		},
	})

	input := newLogs(map[string]string{"event.type": "metrics", "other": "keep"})
	out, err := sp.processLogs(context.Background(), input)
	require.NoError(t, err)

	attrs := firstLogRecord(t, out).Attributes()

	_, ok := attrs.Get(sentinelStreamNameAttribute)
	require.False(t, ok, "stream name should not be set when no rule matches")

	_, ok = attrs.Get(sentinelRuleIDAttribute)
	require.False(t, ok, "rule id should not be set when no rule matches")

	// Original attributes preserved.
	v, ok := attrs.Get("event.type")
	require.True(t, ok)
	require.Equal(t, "metrics", v.Str())
	v, ok = attrs.Get("other")
	require.True(t, ok)
	require.Equal(t, "keep", v.Str())
}

func TestProcessLogs_EmptyConditionMatchesAll(t *testing.T) {
	sp := newProcessor(t, []SentinelFieldRule{
		{StreamName: "Custom-CatchAll_CL"},
	})

	out, err := sp.processLogs(context.Background(), newLogs(nil))
	require.NoError(t, err)

	stream, ok := firstLogRecord(t, out).Attributes().Get(sentinelStreamNameAttribute)
	require.True(t, ok)
	require.Equal(t, "Custom-CatchAll_CL", stream.Str())
}

func TestProcessLogs_OmitsRuleIDWhenEmpty(t *testing.T) {
	sp := newProcessor(t, []SentinelFieldRule{
		{Condition: "true", StreamName: "Custom-Stream_CL"},
	})

	out, err := sp.processLogs(context.Background(), newLogs(nil))
	require.NoError(t, err)

	attrs := firstLogRecord(t, out).Attributes()

	_, ok := attrs.Get(sentinelStreamNameAttribute)
	require.True(t, ok)

	_, ok = attrs.Get(sentinelRuleIDAttribute)
	require.False(t, ok, "rule id attribute should not be set when rule_id is empty")
}

func TestProcessLogs_WritesAllIngestionLabels(t *testing.T) {
	sp := newProcessor(t, []SentinelFieldRule{
		{
			Condition:  "true",
			StreamName: "Custom-Stream_CL",
			IngestionLabels: map[string]string{
				"env":  "prod",
				"team": "security",
				"tier": "critical",
			},
		},
	})

	out, err := sp.processLogs(context.Background(), newLogs(nil))
	require.NoError(t, err)

	attrs := firstLogRecord(t, out).Attributes()

	expected := map[string]string{
		`sentinel_ingestion_label["env"]`:  "prod",
		`sentinel_ingestion_label["team"]`: "security",
		`sentinel_ingestion_label["tier"]`: "critical",
	}
	for k, want := range expected {
		got, ok := attrs.Get(k)
		require.Truef(t, ok, "missing attribute %q", k)
		require.Equalf(t, want, got.Str(), "value for attribute %q", k)
	}
}

func TestProcessLogs_ProcessesMultipleRecords(t *testing.T) {
	sp := newProcessor(t, []SentinelFieldRule{
		{
			Condition:  `attributes["event.type"] == "auth"`,
			StreamName: "Custom-Auth_CL",
		},
	})

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	scope := rl.ScopeLogs().AppendEmpty()
	auth := scope.LogRecords().AppendEmpty()
	auth.Attributes().PutStr("event.type", "auth")
	metrics := scope.LogRecords().AppendEmpty()
	metrics.Attributes().PutStr("event.type", "metrics")

	out, err := sp.processLogs(context.Background(), logs)
	require.NoError(t, err)

	records := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Equal(t, 2, records.Len())

	authStream, ok := records.At(0).Attributes().Get(sentinelStreamNameAttribute)
	require.True(t, ok)
	require.Equal(t, "Custom-Auth_CL", authStream.Str())

	_, ok = records.At(1).Attributes().Get(sentinelStreamNameAttribute)
	require.False(t, ok, "non-matching record should be untouched")
}

func TestProcessLogs_EmptyRulesPassThrough(t *testing.T) {
	sp := newProcessor(t, nil)

	input := newLogs(map[string]string{"event.type": "auth"})
	out, err := sp.processLogs(context.Background(), input)
	require.NoError(t, err)

	_, ok := firstLogRecord(t, out).Attributes().Get(sentinelStreamNameAttribute)
	require.False(t, ok)
}

func TestNewSentinelStandardizationProcessor_InvalidCondition(t *testing.T) {
	set := processortest.NewNopSettings(metadata.Type)
	_, err := newSentinelStandardizationProcessor(set, &Config{
		SentinelField: []SentinelFieldRule{
			{Condition: "not valid ottl", StreamName: "Custom-Stream_CL"},
		},
	})
	require.Error(t, err)
}
