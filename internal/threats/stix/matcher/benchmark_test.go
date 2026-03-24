package matcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// benchmarkMatcherDir returns the path to testdata/bench/matcher (repo root testdata/bench/matcher).
// Payloads there are shared with scripts/bench_python_matcher.py for exact same workload.
func benchmarkMatcherDir(b *testing.B) string {
	_, fn, _, _ := runtime.Caller(0)
	dir := filepath.Dir(fn)
	repoRoot := filepath.Join(dir, "..", "..")
	path := filepath.Join(repoRoot, "testdata", "bench", "matcher")
	if _, err := os.Stat(path); err != nil {
		b.Fatalf("benchmark testdata not found at %s: %v", path, err)
	}
	return path
}

func loadBenchObs(b *testing.B, dir, name string) []map[string]interface{} {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		b.Fatal(err)
	}
	var obs []map[string]interface{}
	if err := json.Unmarshal(data, &obs); err != nil {
		b.Fatal(err)
	}
	return obs
}

func loadBenchPatterns(b *testing.B, dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "patterns.json"))
	if err != nil {
		b.Fatal(err)
	}
	var patterns []string
	if err := json.Unmarshal(data, &patterns); err != nil {
		b.Fatal(err)
	}
	return patterns
}

// BenchmarkMatch_SinglePattern_SmallObs measures Match(pattern, obs) with one pattern and small observed data.
// Uses first pattern from testdata/bench/matcher/patterns.json and observed_small.json (same as Python script).
func BenchmarkMatch_SinglePattern_SmallObs(b *testing.B) {
	dir := benchmarkMatcherDir(b)
	obs := loadBenchObs(b, dir, "observed_small.json")
	patterns := loadBenchPatterns(b, dir)
	if len(patterns) == 0 {
		b.Fatal("patterns.json is empty")
	}
	pattern := patterns[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Match(pattern, obs, nil)
	}
}

// BenchmarkMatch_Compiled_SmallObs measures compiled pattern Match only (no parse per iteration).
// Compile(pattern) once outside the loop; same observed data as SinglePattern_SmallObs.
func BenchmarkMatch_Compiled_SmallObs(b *testing.B) {
	dir := benchmarkMatcherDir(b)
	obs := loadBenchObs(b, dir, "observed_small.json")
	patterns := loadBenchPatterns(b, dir)
	if len(patterns) == 0 {
		b.Fatal("patterns.json is empty")
	}
	pattern := patterns[0]
	cp, err := Compile(pattern)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cp.Match(obs, nil)
	}
}

// BenchmarkMatch_MultiplePatterns runs each pattern in patterns.json against the same small observed data.
// Sub-benchmark per pattern so Go and Python can compare per-pattern ns/op.
func BenchmarkMatch_MultiplePatterns(b *testing.B) {
	dir := benchmarkMatcherDir(b)
	obs := loadBenchObs(b, dir, "observed_small.json")
	patterns := loadBenchPatterns(b, dir)
	if len(patterns) == 0 {
		b.Fatal("patterns.json is empty")
	}
	for i, pattern := range patterns {
		pattern := pattern
		name := fmt.Sprintf("pattern_%d", i)
		b.Run(name, func(b *testing.B) {
			b.ResetTimer()
			for j := 0; j < b.N; j++ {
				_, _ = Match(pattern, obs, nil)
			}
		})
	}
}
