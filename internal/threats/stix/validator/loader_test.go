package validator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func findSchemaDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Skipf("Abs: %v", err)
	}
	for i := 0; i < 5; i++ {
		try := filepath.Join(dir, "cti-stix2-json-schemas", "schemas")
		if _, err := os.Stat(try); err == nil {
			return try
		}
		dir = filepath.Dir(dir)
		if dir == "." || strings.HasSuffix(dir, "stix-go") {
			break
		}
	}
	return ""
}

func TestLoad_BuildsTypeToPath(t *testing.T) {
	schemaDir := findSchemaDir(t)
	if schemaDir == "" {
		t.Skip("cannot find cti-stix2-json-schemas/schemas (run from repo root)")
	}
	loader, err := Load(schemaDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loader == nil || loader.Schemas == nil || loader.TypeToPath == nil {
		t.Fatal("Load: nil loader or maps")
	}
	// Must have bundle and indicator
	if loader.TypeToPath["bundle"] != "common/bundle.json" {
		t.Errorf("TypeToPath[bundle] = %q, want common/bundle.json", loader.TypeToPath["bundle"])
	}
	if loader.TypeToPath["indicator"] != "sdos/indicator.json" {
		t.Errorf("TypeToPath[indicator] = %q, want sdos/indicator.json", loader.TypeToPath["indicator"])
	}
	if loader.TypeToPath["file"] != "observables/file.json" {
		t.Errorf("TypeToPath[file] = %q, want observables/file.json", loader.TypeToPath["file"])
	}
	// SchemaAt
	s := loader.SchemaAt("sdos/indicator.json")
	if s == nil {
		t.Error("SchemaAt(sdos/indicator.json) = nil")
	}
}

func TestResolveRef(t *testing.T) {
	tests := []struct {
		base, ref, want string
	}{
		{"sdos/indicator.json", "../common/core.json", "common/core.json"},
		{"common/core.json", "../common/timestamp.json", "common/timestamp.json"},
		{"observables/file.json", "../common/hashes-type.json", "common/hashes-type.json"},
	}
	for _, tt := range tests {
		got := ResolveRef(tt.base, tt.ref)
		if got != tt.want {
			t.Errorf("ResolveRef(%q, %q) = %q, want %q", tt.base, tt.ref, got, tt.want)
		}
	}
}

func TestBuiltinLoader(t *testing.T) {
	loader := BuiltinLoader()
	if loader == nil {
		t.Fatal("BuiltinLoader() = nil")
	}
	if loader.Schemas == nil || loader.TypeToPath == nil {
		t.Fatal("BuiltinLoader: nil Schemas or TypeToPath")
	}
	if loader.SchemaForType("indicator") != "sdos/indicator.json" {
		t.Errorf("SchemaForType(indicator) = %q, want sdos/indicator.json", loader.SchemaForType("indicator"))
	}
	if loader.SchemaForType("bundle") != "common/bundle.json" {
		t.Errorf("SchemaForType(bundle) = %q, want common/bundle.json", loader.SchemaForType("bundle"))
	}
	if loader.SchemaAt("sdos/indicator.json") == nil {
		t.Error("SchemaAt(sdos/indicator.json) = nil")
	}
}
