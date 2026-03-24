package validatorcodegen

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
	schemas, typeToPath, err := Load(schemaDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(schemas) == 0 || len(typeToPath) == 0 {
		t.Fatalf("Load: empty schemas or typeToPath")
	}
	if typeToPath["bundle"] != "common/bundle.json" {
		t.Errorf("typeToPath[bundle] = %q, want common/bundle.json", typeToPath["bundle"])
	}
	if typeToPath["indicator"] != "sdos/indicator.json" {
		t.Errorf("typeToPath[indicator] = %q, want sdos/indicator.json", typeToPath["indicator"])
	}
	if typeToPath["file"] != "observables/file.json" {
		t.Errorf("typeToPath[file] = %q, want observables/file.json", typeToPath["file"])
	}
}

func TestGenerate_ProducesValidGo(t *testing.T) {
	schemaDir := findSchemaDir(t)
	if schemaDir == "" {
		t.Skip("cannot find cti-stix2-json-schemas/schemas (run from repo root)")
	}
	schemas, typeToPath, err := Load(schemaDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	code, err := Generate(schemas, typeToPath)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Must contain package and both vars
	if !strings.Contains(string(code), "package validator") {
		t.Error("generated code missing package validator")
	}
	if !strings.Contains(string(code), "var generatedTypeToPath") {
		t.Error("generated code missing generatedTypeToPath")
	}
	if !strings.Contains(string(code), "var generatedSchemas") {
		t.Error("generated code missing generatedSchemas")
	}
	if !strings.Contains(string(code), `"indicator": "sdos/indicator.json"`) {
		t.Error("generated code missing indicator entry in typeToPath")
	}
}
