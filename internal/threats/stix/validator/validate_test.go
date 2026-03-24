package validator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()
	if opts.Version != "2.1" {
		t.Errorf("DefaultOptions().Version = %q, want 2.1", opts.Version)
	}
	if opts.Strict {
		t.Error("DefaultOptions().Strict = true, want false")
	}
}

func TestDefaultConfig_EmbedsDefaultOptions(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.Options.Version; got != "2.1" {
		t.Errorf("DefaultConfig().Options.Version = %q, want 2.1", got)
	}
	if cfg.Options.Strict {
		t.Error("DefaultConfig().Options.Strict = true, want false")
	}
	if !cfg.PreserveObjectOrder {
		t.Error("DefaultConfig().PreserveObjectOrder = false, want true")
	}
	if cfg.MaxConcurrentObjects != 0 {
		t.Errorf("DefaultConfig().MaxConcurrentObjects = %d, want 0 (serial by default)", cfg.MaxConcurrentObjects)
	}
}

func TestGetCode_Success(t *testing.T) {
	results := []FileResult{
		{Result: true, Filepath: "a.json", ObjectResults: []ObjectResult{{Result: true, ObjectID: "x--1"}}},
	}
	if got := GetCode(results); got != ExitSuccess {
		t.Errorf("GetCode(success) = 0x%x, want 0x0", got)
	}
}

func TestGetCode_SchemaInvalid(t *testing.T) {
	results := []FileResult{
		{
			Result:   false,
			Filepath: "a.json",
			ObjectResults: []ObjectResult{{
				Result:   false,
				ObjectID: "indicator--1",
				Errors:   []SchemaError{{Message: "missing required property 'pattern'"}},
			}},
		},
	}
	if got := GetCode(results); got != ExitSchemaInvalid {
		t.Errorf("GetCode(schema invalid) = 0x%x, want 0x2", got)
	}
}

func TestGetCode_Fatal(t *testing.T) {
	results := []FileResult{
		{
			Result:   false,
			Filepath: "a.json",
			Fatal:    &FatalResult{Message: "invalid JSON"},
		},
	}
	if got := GetCode(results); got != ExitValidationError {
		t.Errorf("GetCode(fatal) = 0x%x, want 0x10", got)
	}
}

func TestGetCode_Combined(t *testing.T) {
	results := []FileResult{
		{
			Result:        false,
			ObjectResults: []ObjectResult{{Errors: []SchemaError{{Message: "x"}}}},
		},
		{Fatal: &FatalResult{Message: "y"}},
	}
	got := GetCode(results)
	want := ExitSchemaInvalid | ExitValidationError
	if got != want {
		t.Errorf("GetCode(combined) = 0x%x, want 0x%x", got, want)
	}
}

func TestResultJSON_Structure(t *testing.T) {
	// Marshal FileResult and ObjectResult; assert keys and structure.
	fr := FileResult{
		Result:   false,
		Filepath: "test.json",
		ObjectResults: []ObjectResult{{
			Result:   false,
			ObjectID: "indicator--abc",
			Errors:   []SchemaError{{Path: "pattern", Message: "required"}},
			Warnings: []SchemaError{{Message: "custom property"}},
		}},
	}
	b, err := json.Marshal(fr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded FileResult
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Result != fr.Result || decoded.Filepath != fr.Filepath {
		t.Errorf("decoded = %+v", decoded)
	}
	if len(decoded.ObjectResults) != 1 {
		t.Fatalf("len(ObjectResults) = %d, want 1", len(decoded.ObjectResults))
	}
	obj := decoded.ObjectResults[0]
	if obj.ObjectID != "indicator--abc" || len(obj.Errors) != 1 || len(obj.Warnings) != 1 {
		t.Errorf("ObjectResult = %+v", obj)
	}
	if obj.Errors[0].Message != "required" || obj.Errors[0].Path != "pattern" {
		t.Errorf("Errors[0] = %+v", obj.Errors[0])
	}
}

func TestValidateFiles_MissingFile(t *testing.T) {
	results, err := ValidateFiles([]string{"nonexistent-file-404.json"}, DefaultOptions())
	if err != nil {
		t.Fatalf("ValidateFiles: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Fatal == nil {
		t.Error("expected Fatal when file is missing")
	}
	if results[0].Result {
		t.Error("Result should be false when Fatal is set")
	}
}

func TestValidateFilesWithConfig_MissingFile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Options = DefaultOptions()
	results, err := ValidateFilesWithConfig([]string{"nonexistent-file-404.json"}, cfg)
	if err != nil {
		t.Fatalf("ValidateFilesWithConfig: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Fatal == nil {
		t.Error("expected Fatal when file is missing")
	}
	if results[0].Result {
		t.Error("Result should be false when Fatal is set")
	}
}

func TestValidateReader_InvalidJSON(t *testing.T) {
	results, err := ValidateReader(strings.NewReader("not json"), DefaultOptions())
	if err != nil {
		t.Fatalf("ValidateReader: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Fatal == nil {
		t.Error("expected Fatal for invalid JSON")
	}
}

func TestValidateReaderWithConfig_InvalidJSON(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Options = DefaultOptions()
	results, err := ValidateReaderWithConfig(strings.NewReader("not json"), cfg)
	if err != nil {
		t.Fatalf("ValidateReaderWithConfig: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Fatal == nil {
		t.Error("expected Fatal for invalid JSON")
	}
}

func TestValidateReader_ValidIndicator(t *testing.T) {
	indicator := `{"type":"indicator","spec_version":"2.1","id":"indicator--x","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:hashes.MD5='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"}`
	results, err := ValidateReader(strings.NewReader(indicator), DefaultOptions())
	if err != nil {
		t.Fatalf("ValidateReader: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	fr := results[0]
	if fr.Fatal != nil {
		t.Errorf("unexpected Fatal: %v", fr.Fatal)
	}
	if len(fr.ObjectResults) != 1 {
		t.Fatalf("len(ObjectResults) = %d, want 1", len(fr.ObjectResults))
	}
	if !fr.ObjectResults[0].Result {
		t.Errorf("expected valid indicator; errors: %v", fr.ObjectResults[0].Errors)
	}
	if !fr.Result {
		t.Error("FileResult.Result should be true")
	}
}

// TestValidateReader_BundleWithErrors runs full validation on a bundle with one valid
// and one invalid object; asserts object results and that the invalid one has errors.
func TestValidateReader_BundleWithErrors(t *testing.T) {
	bundle := `{"type":"bundle","id":"bundle--x","spec_version":"2.1","objects":[{"type":"indicator","spec_version":"2.1","id":"indicator--a0000000-0000-4000-8000-000000000001","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"},{"type":"indicator","spec_version":"2.1","id":"indicator--b0000000-0000-4000-8000-000000000002","created":"2020-01-32T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"}]}`
	results, err := ValidateReader(strings.NewReader(bundle), DefaultOptions())
	if err != nil {
		t.Fatalf("ValidateReader: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 file result, got %d", len(results))
	}
	fr := results[0]
	if fr.Fatal != nil {
		t.Fatalf("unexpected Fatal: %v", fr.Fatal)
	}
	// We expect at least one object result that has errors (invalid timestamp).
	var foundInvalid bool
	for _, o := range fr.ObjectResults {
		if !o.Result && len(o.Errors) > 0 {
			foundInvalid = true
			break
		}
	}
	if !foundInvalid {
		t.Errorf("expected at least one object result with errors (invalid timestamp); got %d results: %+v", len(fr.ObjectResults), fr.ObjectResults)
	}
}

// TestValidateReaderStreaming_ValidBundle runs streaming validation on a bundle and compares
// results to ValidateReader (same object count and pass/fail pattern).
func TestValidateReaderStreaming_ValidBundle(t *testing.T) {
	bundle := `{"type":"bundle","id":"bundle--x","spec_version":"2.1","objects":[{"type":"indicator","spec_version":"2.1","id":"indicator--a0000000-0000-4000-8000-000000000001","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"},{"type":"indicator","spec_version":"2.1","id":"indicator--b0000000-0000-4000-8000-000000000002","created":"2020-01-32T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"}]}`
	opts := DefaultOptions()
	// Non-streaming baseline
	resultsNonStream, err := ValidateReader(strings.NewReader(bundle), opts)
	if err != nil {
		t.Fatalf("ValidateReader: %v", err)
	}
	// Streaming
	resultsStream, err := ValidateReaderStreaming(strings.NewReader(bundle), opts)
	if err != nil {
		t.Fatalf("ValidateReaderStreaming: %v", err)
	}
	if len(resultsStream) != 1 {
		t.Fatalf("streaming: expected 1 file result, got %d", len(resultsStream))
	}
	if len(resultsStream[0].ObjectResults) != len(resultsNonStream[0].ObjectResults) {
		t.Errorf("streaming object count = %d, want %d", len(resultsStream[0].ObjectResults), len(resultsNonStream[0].ObjectResults))
	}
	for i := range resultsStream[0].ObjectResults {
		got := resultsStream[0].ObjectResults[i].Result
		want := resultsNonStream[0].ObjectResults[i].Result
		if got != want {
			t.Errorf("object %d: streaming Result = %v, want %v", i, got, want)
		}
	}
	if resultsStream[0].Result != resultsNonStream[0].Result {
		t.Errorf("streaming FileResult.Result = %v, want %v", resultsStream[0].Result, resultsNonStream[0].Result)
	}
}

// TestValidateReaderStreaming_RequiresBundle asserts that streaming returns an error for non-bundle input.
func TestValidateReaderStreaming_RequiresBundle(t *testing.T) {
	indicator := `{"type":"indicator","spec_version":"2.1","id":"indicator--x","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"}`
	_, err := ValidateReaderStreaming(strings.NewReader(indicator), DefaultOptions())
	if err == nil {
		t.Fatal("expected error for single-object input")
	}
	if !strings.Contains(err.Error(), "objects") {
		t.Errorf("error should mention 'objects'; got %q", err.Error())
	}
}

func TestValidateReaderStreamingWithConfig_RequiresBundle(t *testing.T) {
	indicator := `{"type":"indicator","spec_version":"2.1","id":"indicator--x","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"}`
	cfg := DefaultConfig()
	cfg.Options = DefaultOptions()
	_, err := ValidateReaderStreamingWithConfig(strings.NewReader(indicator), cfg)
	if err == nil {
		t.Fatal("expected error for single-object input")
	}
	if !strings.Contains(err.Error(), "objects") {
		t.Errorf("error should mention 'objects'; got %q", err.Error())
	}
}

// TestValidateReaderWithConfig_ParallelMatchesSerial asserts that bundle validation
// with ParallelizeBundles and MaxConcurrentObjects > 1 produces the same results
// (same count, same pass/fail per index) as serial validation.
func TestValidateReaderWithConfig_ParallelMatchesSerial(t *testing.T) {
	bundle := `{"type":"bundle","id":"bundle--x","spec_version":"2.1","objects":[{"type":"indicator","spec_version":"2.1","id":"indicator--a0000000-0000-4000-8000-000000000001","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"},{"type":"indicator","spec_version":"2.1","id":"indicator--b0000000-0000-4000-8000-000000000002","created":"2020-01-32T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"}]}`
	serial := DefaultConfig()
	serial.Options = DefaultOptions()
	parallel := DefaultConfig()
	parallel.Options = DefaultOptions()
	parallel.ParallelizeBundles = true
	parallel.MaxConcurrentObjects = 2
	parallel.PreserveObjectOrder = true

	resultsSerial, err := ValidateReaderWithConfig(strings.NewReader(bundle), serial)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	resultsParallel, err := ValidateReaderWithConfig(strings.NewReader(bundle), parallel)
	if err != nil {
		t.Fatalf("parallel: %v", err)
	}
	if len(resultsSerial) != 1 || len(resultsParallel) != 1 {
		t.Fatalf("expected 1 file result each; serial=%d parallel=%d", len(resultsSerial), len(resultsParallel))
	}
	sObj := resultsSerial[0].ObjectResults
	pObj := resultsParallel[0].ObjectResults
	if len(sObj) != len(pObj) {
		t.Fatalf("object count: serial=%d parallel=%d", len(sObj), len(pObj))
	}
	for i := range sObj {
		if sObj[i].Result != pObj[i].Result {
			t.Errorf("object %d: serial Result=%v parallel Result=%v", i, sObj[i].Result, pObj[i].Result)
		}
	}
	if resultsSerial[0].Result != resultsParallel[0].Result {
		t.Errorf("FileResult.Result: serial=%v parallel=%v", resultsSerial[0].Result, resultsParallel[0].Result)
	}
}

// TestValidateReaderStreamingWithConfig_ParallelMatchesSerial asserts that streaming
// validation with parallelism produces the same results as serial streaming.
func TestValidateReaderStreamingWithConfig_ParallelMatchesSerial(t *testing.T) {
	bundle := `{"type":"bundle","id":"bundle--x","spec_version":"2.1","objects":[{"type":"indicator","spec_version":"2.1","id":"indicator--a0000000-0000-4000-8000-000000000001","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"},{"type":"indicator","spec_version":"2.1","id":"indicator--b0000000-0000-4000-8000-000000000002","created":"2020-01-32T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"}]}`
	serial := DefaultConfig()
	serial.Options = DefaultOptions()
	parallel := DefaultConfig()
	parallel.Options = DefaultOptions()
	parallel.ParallelizeBundles = true
	parallel.MaxConcurrentObjects = 2
	parallel.PreserveObjectOrder = true

	resultsSerial, err := ValidateReaderStreamingWithConfig(strings.NewReader(bundle), serial)
	if err != nil {
		t.Fatalf("serial streaming: %v", err)
	}
	resultsParallel, err := ValidateReaderStreamingWithConfig(strings.NewReader(bundle), parallel)
	if err != nil {
		t.Fatalf("parallel streaming: %v", err)
	}
	if len(resultsSerial) != 1 || len(resultsParallel) != 1 {
		t.Fatalf("expected 1 file result each; serial=%d parallel=%d", len(resultsSerial), len(resultsParallel))
	}
	sObj := resultsSerial[0].ObjectResults
	pObj := resultsParallel[0].ObjectResults
	if len(sObj) != len(pObj) {
		t.Fatalf("object count: serial=%d parallel=%d", len(sObj), len(pObj))
	}
	for i := range sObj {
		if sObj[i].Result != pObj[i].Result {
			t.Errorf("object %d: serial Result=%v parallel Result=%v", i, sObj[i].Result, pObj[i].Result)
		}
	}
	if resultsSerial[0].Result != resultsParallel[0].Result {
		t.Errorf("FileResult.Result: serial=%v parallel=%v", resultsSerial[0].Result, resultsParallel[0].Result)
	}
}

// TestValidateReader_ShouldWarning asserts that a SHOULD violation can appear in Warnings when not Strict.
func TestValidateReader_ShouldWarning(t *testing.T) {
	// Use an indicator with a non-vocab value to trigger a SHOULD warning.
	indicator := `{"type":"indicator","spec_version":"2.1","id":"indicator--a0000000-0000-4000-8000-000000000001","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","indicator_types":["unknown-vocab-value"],"pattern":"[file:name='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z","name":"x","description":"y"}`
	opts := DefaultOptions()
	opts.Strict = false
	results, err := ValidateReader(strings.NewReader(indicator), opts)
	if err != nil {
		t.Fatalf("ValidateReader: %v", err)
	}
	if len(results) != 1 || len(results[0].ObjectResults) != 1 {
		t.Fatalf("expected 1 file, 1 object result")
	}
	obj := results[0].ObjectResults[0]
	// indicator_types not in vocab should produce a warning
	hasWarning := false
	for _, w := range obj.Warnings {
		if w.Path == "indicator_types" || strings.Contains(w.Message, "vocabulary") {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Errorf("expected a SHOULD warning (e.g. indicator-types vocab); got Warnings: %v", obj.Warnings)
	}
}

// benchmarkTestdataDir returns the path to testdata/bench (repo root testdata/bench).
// Payloads there are shared with scripts/bench_python_validator.py for exact same workload.
func benchmarkTestdataDir(b *testing.B) string {
	_, fn, _, _ := runtime.Caller(0)
	dir := filepath.Dir(fn)
	repoRoot := filepath.Join(dir, "..", "..")
	path := filepath.Join(repoRoot, "testdata", "bench")
	if _, err := os.Stat(path); err != nil {
		b.Fatalf("benchmark testdata not found at %s: %v", path, err)
	}
	return path
}

// benchmarkMITREAttackPath returns the path to internal/threats/stix/validator/e2e/testdata/attack.json.
// Skips the benchmark if the file is not present (e2e testdata optional).
func benchmarkMITREAttackPath(b *testing.B) string {
	_, fn, _, _ := runtime.Caller(0)
	dir := filepath.Dir(fn)
	path := filepath.Join(dir, "e2e", "testdata", "attack.json")
	if _, err := os.Stat(path); err != nil {
		b.Skipf("MITRE attack.json not found at %s: %v", path, err)
	}
	return path
}

// TestBenchmarkPayloadsValid asserts that all benchmark testdata files pass full validation.
// This confirms benchmark timings reflect the success path, not early exit on failure.
func TestBenchmarkPayloadsValid(t *testing.T) {
	_, fn, _, _ := runtime.Caller(0)
	dir := filepath.Dir(fn)
	benchDir := filepath.Join(dir, "..", "..", "testdata", "bench")
	if _, err := os.Stat(benchDir); err != nil {
		t.Skipf("benchmark testdata not found at %s: %v", benchDir, err)
	}
	files := []string{"single_object.json", "bundle_3.json", "bundle_1.json", "bundle_10.json", "bundle_100.json"}
	opts := DefaultOptions()
	for _, name := range files {
		path := filepath.Join(benchDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read: %v", name, err)
			continue
		}
		results, err := ValidateReader(bytes.NewReader(data), opts)
		if err != nil {
			t.Errorf("%s: ValidateReader: %v", name, err)
			continue
		}
		if len(results) != 1 {
			t.Errorf("%s: expected 1 file result, got %d", name, len(results))
			continue
		}
		if code := GetCode(results); code != ExitSuccess {
			t.Errorf("%s: validation failed (exit code 0x%x); Fatal=%v, ObjectResults=%d",
				name, code, results[0].Fatal, len(results[0].ObjectResults))
			continue
		}
		if !results[0].Result {
			t.Errorf("%s: FileResult.Result is false; errors: %v", name, results[0].ObjectResults)
		}
	}
}

// TestBenchmarkPayloadsValidStreaming asserts that bundle benchmark payloads pass streaming validation.
// Streaming requires a bundle (objects array); single_object.json is not used.
func TestBenchmarkPayloadsValidStreaming(t *testing.T) {
	_, fn, _, _ := runtime.Caller(0)
	dir := filepath.Dir(fn)
	benchDir := filepath.Join(dir, "..", "..", "testdata", "bench")
	if _, err := os.Stat(benchDir); err != nil {
		t.Skipf("benchmark testdata not found at %s: %v", benchDir, err)
	}
	bundleFiles := []string{"bundle_1.json", "bundle_3.json", "bundle_10.json", "bundle_100.json"}
	opts := DefaultOptions()
	for _, name := range bundleFiles {
		path := filepath.Join(benchDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read: %v", name, err)
			continue
		}
		results, err := ValidateReaderStreaming(bytes.NewReader(data), opts)
		if err != nil {
			t.Errorf("%s: ValidateReaderStreaming: %v", name, err)
			continue
		}
		if len(results) != 1 {
			t.Errorf("%s: expected 1 file result, got %d", name, len(results))
			continue
		}
		if code := GetCode(results); code != ExitSuccess {
			t.Errorf("%s: validation failed (exit code 0x%x); Fatal=%v, ObjectResults=%d",
				name, code, results[0].Fatal, len(results[0].ObjectResults))
			continue
		}
		if !results[0].Result {
			t.Errorf("%s: FileResult.Result is false; errors: %v", name, results[0].ObjectResults)
		}
	}
}

// BenchmarkValidateReader_SingleObject measures end-to-end validation of a minimal valid indicator
// (JSON unmarshal + schema + MUST + SHOULD). Payload from testdata/bench/single_object.json.
func BenchmarkValidateReader_SingleObject(b *testing.B) {
	td := benchmarkTestdataDir(b)
	data, err := os.ReadFile(filepath.Join(td, "single_object.json"))
	if err != nil {
		b.Fatal(err)
	}
	opts := DefaultOptions()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ValidateReader(bytes.NewReader(data), opts)
	}
}

// BenchmarkValidateReader_Bundle measures end-to-end validation of a small bundle (bundle + 3 objects).
// Payload from testdata/bench/bundle_3.json.
func BenchmarkValidateReader_Bundle(b *testing.B) {
	td := benchmarkTestdataDir(b)
	data, err := os.ReadFile(filepath.Join(td, "bundle_3.json"))
	if err != nil {
		b.Fatal(err)
	}
	opts := DefaultOptions()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ValidateReader(bytes.NewReader(data), opts)
	}
}

// BenchmarkValidateReader_Bundle_Scaling runs validation for bundles of different sizes.
// Payloads from testdata/bench/bundle_1.json, bundle_10.json, bundle_100.json.
func BenchmarkValidateReader_Bundle_Scaling(b *testing.B) {
	td := benchmarkTestdataDir(b)
	sizes := []int{1, 10, 100}
	for _, n := range sizes {
		n := n
		fname := fmt.Sprintf("bundle_%d.json", n)
		data, err := os.ReadFile(filepath.Join(td, fname))
		if err != nil {
			b.Fatal(err)
		}
		opts := DefaultOptions()
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = ValidateReader(bytes.NewReader(data), opts)
			}
		})
	}
}

// BenchmarkValidateReader_MITREAttack measures end-to-end validation of the MITRE ATT&CK
// STIX bundle (internal/threats/stix/validator/e2e/testdata/attack.json). Skips if the file is
// not present. Use for real-dataset performance comparison with the Python validator.
func BenchmarkValidateReader_MITREAttack(b *testing.B) {
	path := benchmarkMITREAttackPath(b)
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	opts := DefaultOptions()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ValidateReader(bytes.NewReader(data), opts)
	}
}

// BenchmarkValidateReaderStreaming_MITREAttack measures streaming validation on the MITRE
// ATT&CK bundle (same file as BenchmarkValidateReader_MITREAttack). Compare B/op to the
// non-streaming benchmark to see peak-memory reduction.
func BenchmarkValidateReaderStreaming_MITREAttack(b *testing.B) {
	path := benchmarkMITREAttackPath(b)
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	opts := DefaultOptions()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ValidateReaderStreaming(bytes.NewReader(data), opts)
	}
}

// BenchmarkValidateReaderWithConfig_MITREAttack runs the MITRE benchmark using ValidateReaderWithConfig
// with different engine configurations. Today Config only affects semantic options, but this benchmark
// scaffolds comparison points for future parallel and engine-level behavior.
func BenchmarkValidateReaderWithConfig_MITREAttack(b *testing.B) {
	path := benchmarkMITREAttackPath(b)
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}

	baseCfg := DefaultConfig()
	baseCfg.Options = DefaultOptions()

	serialCfg := baseCfg

	parallelCfg := baseCfg
	parallelCfg.ParallelizeBundles = true
	parallelCfg.MaxConcurrentObjects = 4
	parallelCfg.PreserveObjectOrder = true

	parallelUnorderedCfg := baseCfg
	parallelUnorderedCfg.ParallelizeBundles = true
	parallelUnorderedCfg.MaxConcurrentObjects = 4
	parallelUnorderedCfg.PreserveObjectOrder = false

	b.Run("Serial_DefaultConfig", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = ValidateReaderWithConfig(bytes.NewReader(data), serialCfg)
		}
	})

	b.Run("Parallel4_PreserveOrder", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = ValidateReaderWithConfig(bytes.NewReader(data), parallelCfg)
		}
	})

	b.Run("Parallel4_Unordered", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = ValidateReaderWithConfig(bytes.NewReader(data), parallelUnorderedCfg)
		}
	})
}

// BenchmarkValidateReaderStreamingWithConfig_MITREAttack runs the MITRE benchmark using
// ValidateReaderStreamingWithConfig with different engine configurations. As with the
// non-streaming variant, these sub-benchmarks provide baselines for future parallel
// and engine-level behavior.
func BenchmarkValidateReaderStreamingWithConfig_MITREAttack(b *testing.B) {
	path := benchmarkMITREAttackPath(b)
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}

	baseCfg := DefaultConfig()
	baseCfg.Options = DefaultOptions()

	serialCfg := baseCfg

	parallelCfg := baseCfg
	parallelCfg.ParallelizeBundles = true
	parallelCfg.MaxConcurrentObjects = 4
	parallelCfg.PreserveObjectOrder = true

	parallelUnorderedCfg := baseCfg
	parallelUnorderedCfg.ParallelizeBundles = true
	parallelUnorderedCfg.MaxConcurrentObjects = 4
	parallelUnorderedCfg.PreserveObjectOrder = false

	b.Run("Streaming_Serial_DefaultConfig", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = ValidateReaderStreamingWithConfig(bytes.NewReader(data), serialCfg)
		}
	})

	b.Run("Streaming_Parallel4_PreserveOrder", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = ValidateReaderStreamingWithConfig(bytes.NewReader(data), parallelCfg)
		}
	})

	b.Run("Streaming_Parallel4_Unordered", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _ = ValidateReaderStreamingWithConfig(bytes.NewReader(data), parallelUnorderedCfg)
		}
	})
}
