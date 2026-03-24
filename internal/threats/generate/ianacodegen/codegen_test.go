package ianacodegen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// findModuleRoot returns the repo root (directory containing go.mod) by walking up from this test file.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found (reached filesystem root)")
		}
		dir = parent
	}
}

func TestLoad_ContainsGoldenValues(t *testing.T) {
	root := findModuleRoot(t)
	// Use same path as Makefile: pkg/cti-stix-validator (subtree location).
	assetsDir := filepath.Join(root, "pkg", "cti-stix-validator", "stix2validator", "v21", "assets")
	if _, err := os.Stat(assetsDir); err != nil {
		if os.IsNotExist(err) {
			t.Skipf("assets dir not found (pull subtree: make subtree-pull): %s", assetsDir)
		}
		t.Fatalf("Load: %v", err)
	}
	d, err := Load(assetsDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mime, charset, protocol, ipfix := KnownValues(d)
	if mime == "" || charset == "" || protocol == "" || ipfix == "" {
		t.Errorf("KnownValues returned empty: mime=%q charset=%q protocol=%q ipfix=%q", mime, charset, protocol, ipfix)
	}
	// Ensure generated output contains these
	code, err := Generate(d)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	codeStr := string(code)
	for _, s := range []string{mime, charset, protocol, ipfix} {
		if !strings.Contains(codeStr, s) {
			t.Errorf("generated code does not contain golden value %q", s)
		}
	}
	if !strings.Contains(codeStr, "IsValidMIMEType") || !strings.Contains(codeStr, "IsValidCharset") ||
		!strings.Contains(codeStr, "IsValidProtocol") || !strings.Contains(codeStr, "IsValidIPFIXName") {
		t.Error("generated code missing one or more IsValid* functions")
	}
}
