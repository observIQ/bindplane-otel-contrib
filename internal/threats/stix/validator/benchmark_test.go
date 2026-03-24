// benchmarks: component-level validation (schema, MUSTs, SHOULDs).

package validator

import (
	"encoding/json"
	"testing"
)

// minimalIndicatorJSON is a valid minimal STIX 2.1 indicator (used for consistent benchmarks).
const minimalIndicatorJSON = `{"type":"indicator","spec_version":"2.1","id":"indicator--x","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:hashes.MD5='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"}`

// benchmarkIndicatorObj returns a parsed indicator object and the shared loader/options for component benchmarks.
// Call from benchmark setup (outside b.N loop).
func benchmarkIndicatorObj(tb testing.TB) (obj map[string]interface{}, loader *Loader, mustOpts *MustOptions, shouldOpts ShouldOptions) {
	tb.Helper()
	var raw interface{}
	if err := json.Unmarshal([]byte(minimalIndicatorJSON), &raw); err != nil {
		tb.Fatalf("parse indicator: %v", err)
	}
	var ok bool
	obj, ok = raw.(map[string]interface{})
	if !ok {
		tb.Fatal("parsed value is not map[string]interface{}")
	}
	loader = BuiltinLoader()
	mustOpts = &MustOptions{}
	shouldOpts = ShouldOptions{}
	return obj, loader, mustOpts, shouldOpts
}

// BenchmarkValidateObject measures only the schema interpreter (path→constraints walk).
func BenchmarkValidateObject(b *testing.B) {
	obj, loader, _, _ := benchmarkIndicatorObj(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ValidateObject(obj, "indicator", loader)
	}
}

// BenchmarkRunMUSTs measures only MUST checks (timestamps, pattern, etc.) for one object.
func BenchmarkRunMUSTs(b *testing.B) {
	obj, _, mustOpts, _ := benchmarkIndicatorObj(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RunMUSTs(obj, "indicator", mustOpts)
	}
}

// BenchmarkRunShoulds measures only SHOULD checks (open-vocab, etc.) for one object.
func BenchmarkRunShoulds(b *testing.B) {
	obj, _, _, shouldOpts := benchmarkIndicatorObj(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RunShoulds(obj, "indicator", shouldOpts)
	}
}
