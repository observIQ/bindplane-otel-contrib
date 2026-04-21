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

package azureloganalyticsexporter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
)

// makeRecord appends a log record to the given scope with the provided body
// and optional per-record routing attributes.
func makeRecord(sl plog.ScopeLogs, body, streamAttr, ruleAttr string) {
	lr := sl.LogRecords().AppendEmpty()
	lr.Body().SetStr(body)
	if streamAttr != "" {
		lr.Attributes().PutStr(sentinelStreamNameAttribute, streamAttr)
	}
	if ruleAttr != "" {
		lr.Attributes().PutStr(sentinelRuleIDAttribute, ruleAttr)
	}
}

func TestGroupLogs_NoAttributesSingleGroup(t *testing.T) {
	cfg := &Config{
		Endpoint:   "https://example.ingest.monitor.azure.com",
		RuleID:     "dcr-default",
		StreamName: "Custom-Default",
	}

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "svc")
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("scope-a")
	makeRecord(sl, "r1", "", "")
	makeRecord(sl, "r2", "", "")
	makeRecord(sl, "r3", "", "")

	groups := groupLogs(ld, cfg)
	require.Len(t, groups, 1, "no routing attrs should produce a single group")

	wantKey := groupKey{Endpoint: cfg.Endpoint, RuleID: cfg.RuleID, StreamName: cfg.StreamName}
	got, ok := groups[wantKey]
	require.True(t, ok, "single group should be keyed by config values; got keys %v", keysOf(groups))
	assert.Equal(t, 3, got.LogRecordCount())
	assert.Equal(t, 1, got.ResourceLogs().Len())
	assert.Equal(t, 1, got.ResourceLogs().At(0).ScopeLogs().Len())

	// Resource + scope metadata should be preserved.
	v, ok := got.ResourceLogs().At(0).Resource().Attributes().Get("service.name")
	require.True(t, ok)
	assert.Equal(t, "svc", v.Str())
	assert.Equal(t, "scope-a", got.ResourceLogs().At(0).ScopeLogs().At(0).Scope().Name())
}

func TestGroupLogs_ByStreamName(t *testing.T) {
	cfg := &Config{Endpoint: "ep", RuleID: "r", StreamName: "default-stream"}

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()
	makeRecord(sl, "alpha-1", "Custom-Alpha", "")
	makeRecord(sl, "alpha-2", "Custom-Alpha", "")
	makeRecord(sl, "beta-1", "Custom-Beta", "")
	makeRecord(sl, "default-1", "", "")

	groups := groupLogs(ld, cfg)
	require.Len(t, groups, 3)

	assert.Equal(t, 2, groups[groupKey{Endpoint: "ep", RuleID: "r", StreamName: "Custom-Alpha"}].LogRecordCount())
	assert.Equal(t, 1, groups[groupKey{Endpoint: "ep", RuleID: "r", StreamName: "Custom-Beta"}].LogRecordCount())
	assert.Equal(t, 1, groups[groupKey{Endpoint: "ep", RuleID: "r", StreamName: "default-stream"}].LogRecordCount())
}

func TestGroupLogs_ByRuleID(t *testing.T) {
	cfg := &Config{Endpoint: "ep", RuleID: "default-rule", StreamName: "s"}

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()
	makeRecord(sl, "x", "", "rule-1")
	makeRecord(sl, "y", "", "rule-1")
	makeRecord(sl, "z", "", "rule-2")

	groups := groupLogs(ld, cfg)
	require.Len(t, groups, 2)
	assert.Equal(t, 2, groups[groupKey{Endpoint: "ep", RuleID: "rule-1", StreamName: "s"}].LogRecordCount())
	assert.Equal(t, 1, groups[groupKey{Endpoint: "ep", RuleID: "rule-2", StreamName: "s"}].LogRecordCount())
}

func TestGroupLogs_CombinedRuleAndStream(t *testing.T) {
	cfg := &Config{Endpoint: "ep", RuleID: "default-rule", StreamName: "default-stream"}

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()

	// (rule-1, stream-1) x2
	makeRecord(sl, "a", "stream-1", "rule-1")
	makeRecord(sl, "b", "stream-1", "rule-1")
	// (rule-1, stream-2) x1
	makeRecord(sl, "c", "stream-2", "rule-1")
	// (rule-2, stream-1) x1
	makeRecord(sl, "d", "stream-1", "rule-2")
	// (default, default) x1
	makeRecord(sl, "e", "", "")

	groups := groupLogs(ld, cfg)
	require.Len(t, groups, 4)
	assert.Equal(t, 2, groups[groupKey{Endpoint: "ep", RuleID: "rule-1", StreamName: "stream-1"}].LogRecordCount())
	assert.Equal(t, 1, groups[groupKey{Endpoint: "ep", RuleID: "rule-1", StreamName: "stream-2"}].LogRecordCount())
	assert.Equal(t, 1, groups[groupKey{Endpoint: "ep", RuleID: "rule-2", StreamName: "stream-1"}].LogRecordCount())
	assert.Equal(t, 1, groups[groupKey{Endpoint: "ep", RuleID: "default-rule", StreamName: "default-stream"}].LogRecordCount())
}

func TestGroupLogs_ResourceAttributeRouting(t *testing.T) {
	cfg := &Config{Endpoint: "ep", RuleID: "default-rule", StreamName: "default-stream"}

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr(sentinelRuleIDAttribute, "res-rule")
	rl.Resource().Attributes().PutStr(sentinelStreamNameAttribute, "res-stream")
	sl := rl.ScopeLogs().AppendEmpty()
	makeRecord(sl, "a", "", "")
	makeRecord(sl, "b", "", "")

	groups := groupLogs(ld, cfg)
	require.Len(t, groups, 1)
	wantKey := groupKey{Endpoint: "ep", RuleID: "res-rule", StreamName: "res-stream"}
	assert.Equal(t, 2, groups[wantKey].LogRecordCount(),
		"resource attributes should route all records under that resource")
}

func TestGroupLogs_RecordAttributeOverridesResource(t *testing.T) {
	cfg := &Config{Endpoint: "ep", RuleID: "default-rule", StreamName: "default-stream"}

	ld := plog.NewLogs()
	rl := ld.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr(sentinelRuleIDAttribute, "res-rule")
	sl := rl.ScopeLogs().AppendEmpty()
	makeRecord(sl, "record-wins", "", "rec-rule")
	makeRecord(sl, "resource-wins", "", "")

	groups := groupLogs(ld, cfg)
	require.Len(t, groups, 2)
	assert.Equal(t, 1, groups[groupKey{Endpoint: "ep", RuleID: "rec-rule", StreamName: "default-stream"}].LogRecordCount())
	assert.Equal(t, 1, groups[groupKey{Endpoint: "ep", RuleID: "res-rule", StreamName: "default-stream"}].LogRecordCount())
}

func TestGroupLogs_PreservesResourceAndScopeHierarchy(t *testing.T) {
	cfg := &Config{Endpoint: "ep", RuleID: "default-rule", StreamName: "default-stream"}

	ld := plog.NewLogs()

	// Resource A with two scopes, mixed routing within each.
	rlA := ld.ResourceLogs().AppendEmpty()
	rlA.Resource().Attributes().PutStr("resource.id", "A")
	slA1 := rlA.ScopeLogs().AppendEmpty()
	slA1.Scope().SetName("scope-A1")
	makeRecord(slA1, "a1-s1", "stream-1", "")
	makeRecord(slA1, "a1-s2", "stream-2", "")

	slA2 := rlA.ScopeLogs().AppendEmpty()
	slA2.Scope().SetName("scope-A2")
	makeRecord(slA2, "a2-s1", "stream-1", "")

	// Resource B, single scope, single stream.
	rlB := ld.ResourceLogs().AppendEmpty()
	rlB.Resource().Attributes().PutStr("resource.id", "B")
	slB := rlB.ScopeLogs().AppendEmpty()
	slB.Scope().SetName("scope-B")
	makeRecord(slB, "b-s1", "stream-1", "")

	groups := groupLogs(ld, cfg)
	require.Len(t, groups, 2)

	s1 := groups[groupKey{Endpoint: "ep", RuleID: "default-rule", StreamName: "stream-1"}]
	s2 := groups[groupKey{Endpoint: "ep", RuleID: "default-rule", StreamName: "stream-2"}]

	// stream-1 contains records from both resources, and two scopes under A.
	assert.Equal(t, 3, s1.LogRecordCount())
	require.Equal(t, 2, s1.ResourceLogs().Len())
	assertResourceID(t, s1.ResourceLogs().At(0), "A")
	assertResourceID(t, s1.ResourceLogs().At(1), "B")
	assert.Equal(t, 2, s1.ResourceLogs().At(0).ScopeLogs().Len(),
		"both scopes under resource A should be preserved in stream-1")
	assert.Equal(t, 1, s1.ResourceLogs().At(1).ScopeLogs().Len())

	// stream-2 contains a single record under resource A, scope-A1 only.
	assert.Equal(t, 1, s2.LogRecordCount())
	require.Equal(t, 1, s2.ResourceLogs().Len())
	assertResourceID(t, s2.ResourceLogs().At(0), "A")
	require.Equal(t, 1, s2.ResourceLogs().At(0).ScopeLogs().Len())
	assert.Equal(t, "scope-A1", s2.ResourceLogs().At(0).ScopeLogs().At(0).Scope().Name())
}

func TestGroupLogs_EmptyInputReturnsConfigKeyedGroup(t *testing.T) {
	cfg := &Config{Endpoint: "ep", RuleID: "r", StreamName: "s"}
	ld := plog.NewLogs()

	groups := groupLogs(ld, cfg)
	require.Len(t, groups, 1)
	_, ok := groups[groupKey{Endpoint: "ep", RuleID: "r", StreamName: "s"}]
	assert.True(t, ok)
}

func TestGroupKey_String(t *testing.T) {
	k := groupKey{Endpoint: "ep", RuleID: "r", StreamName: "s"}
	assert.Equal(t, "ep|r|s", k.String())
}

func assertResourceID(t *testing.T, rl plog.ResourceLogs, want string) {
	t.Helper()
	v, ok := rl.Resource().Attributes().Get("resource.id")
	require.True(t, ok, "resource.id attribute missing")
	assert.Equal(t, want, v.Str())
}

func keysOf(m map[groupKey]plog.Logs) []groupKey {
	out := make([]groupKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
