package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/validator"
)

// goldenEntry matches the JSON written by scripts/generate_e2e_golden.py.
type goldenEntry struct {
	Valid    bool `json:"valid"`
	ExitCode int  `json:"exit_code"`
}

// Set UPDATE_E2E_GOLDEN=1 and run the e2e test to regenerate testdata/e2e_golden.json
// from the Go validator (e.g. go test -run TestE2E_GoValidatorMatchesGolden ./internal/threats/stix/validator/e2e/... -count=1).
const updateGoldenEnv = "UPDATE_E2E_GOLDEN"

func TestE2E_GoValidatorMatchesGolden(t *testing.T) {
	repoRoot, ok := FindRepoRoot()
	if !ok {
		t.Fatal("could not find repo root (go.mod)")
	}
	fixtureBase := FixtureBase(repoRoot)
	if fixtureBase == "" {
		t.Fatal("fixture base is empty")
	}
	if _, err := os.Stat(fixtureBase); err != nil {
		if os.IsNotExist(err) {
			t.Skipf("fixture dir not found (pkg/cti-stix-validator): %s", fixtureBase)
		}
		t.Fatal(err)
	}

	fixtures, err := CollectFixtures(fixtureBase)
	if err != nil {
		t.Fatalf("collect fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Skip("no fixtures found under fixture dir")
	}

	goldenPath := filepath.Join(e2ePackageDir(t), "testdata", "e2e_golden.json")

	opts := validator.DefaultOptions()
	gotGolden := make(map[string]goldenEntry, len(fixtures))
	for _, rel := range fixtures {
		absPath := filepath.Join(fixtureBase, filepath.FromSlash(rel))
		results, err := validator.ValidateFiles([]string{absPath}, opts)
		if err != nil {
			t.Fatalf("ValidateFiles(%s): %v", rel, err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result for %s, got %d", rel, len(results))
		}
		gotGolden[rel] = goldenEntry{
			Valid:    results[0].Result,
			ExitCode: validator.GetCode(results),
		}
	}

	if os.Getenv(updateGoldenEnv) == "1" {
		out, err := json.MarshalIndent(gotGolden, "", "  ")
		if err != nil {
			t.Fatalf("marshal golden: %v", err)
		}
		if err := os.WriteFile(goldenPath, out, 0644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s from Go validator (%d entries)", goldenPath, len(gotGolden))
		return
	}

	goldenBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("golden file not found: %s (run: python3 scripts/generate_e2e_golden.py or UPDATE_E2E_GOLDEN=1 go test -run TestE2E_GoValidatorMatchesGolden ./internal/threats/stix/validator/e2e/...)", goldenPath)
		}
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]goldenEntry
	if err := json.Unmarshal(goldenBytes, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	for _, rel := range fixtures {
		rel := rel
		t.Run(rel, func(t *testing.T) {
			expect, ok := golden[rel]
			if !ok {
				t.Skipf("no golden entry for %s (regenerate with python3 scripts/generate_e2e_golden.py or UPDATE_E2E_GOLDEN=1)", rel)
			}
			got := gotGolden[rel]
			if got.Valid != expect.Valid {
				t.Errorf("valid: got %v, golden %v", got.Valid, expect.Valid)
			}
			if got.ExitCode != expect.ExitCode {
				t.Errorf("exit_code: got 0x%x (%d), golden 0x%x (%d)", got.ExitCode, got.ExitCode, expect.ExitCode, expect.ExitCode)
			}
		})
	}
}

// e2ePackageDir returns the directory of the e2e package (for testdata).
func e2ePackageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}
