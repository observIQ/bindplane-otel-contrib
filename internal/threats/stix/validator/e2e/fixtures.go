// Package e2e provides fixture-based E2E tests comparing the Go validator
// pass/fail and exit codes to golden results (generated from the Python validator).
package e2e

import (
	"os"
	"path/filepath"
	"runtime"
)

// FixtureDirs are the subdirs under v21 that contain JSON fixtures (relative to v21).
var FixtureDirs = []string{"test_examples", "test_schemas"}

// FindRepoRoot returns the repository root by walking up from the caller's
// source file until a directory containing go.mod is found.
func FindRepoRoot() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	dir := filepath.Dir(file)
	for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, true
		}
	}
	return "", false
}

// FixtureBase returns the absolute path to pkg/cti-stix-validator/stix2validator/test/v21,
// using repoRoot. Returns empty string if repoRoot is empty.
func FixtureBase(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	return filepath.Join(repoRoot, "pkg", "cti-stix-validator", "stix2validator", "test", "v21")
}

// CollectFixtures returns relative paths (e.g. "test_examples/identity.json") for all
// .json files under fixtureBase in FixtureDirs. Paths use forward slashes for stable golden keys.
func CollectFixtures(fixtureBase string) ([]string, error) {
	var out []string
	for _, subdir := range FixtureDirs {
		dir := filepath.Join(fixtureBase, subdir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			rel := filepath.Join(subdir, e.Name())
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out, nil
}
