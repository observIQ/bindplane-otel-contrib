package validator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/observiq/bindplane-otel-collector/internal/threats/generate/schemacodegen"
)

func findSchemaDirForCompile(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Skipf("Abs: %v", err)
	}
	for i := 0; i < 5; i++ {
		try := filepath.Join(dir, "pkg", "cti-stix2-json-schemas", "schemas")
		if _, err := os.Stat(try); err == nil {
			return try
		}
		try = filepath.Join(dir, "cti-stix2-json-schemas", "schemas")
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

func TestCompileRoot_Bundle(t *testing.T) {
	schemaDir := findSchemaDirForCompile(t)
	if schemaDir == "" {
		t.Skip("cannot find cti-stix2-json-schemas/schemas")
	}
	raw, err := codegen.LoadSchemaDir(schemaDir)
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	typed := ConvertSchemas(raw)
	compiled, err := CompileRoot(typed, "common/bundle.json")
	if err != nil {
		t.Fatalf("CompileRoot: %v", err)
	}
	if compiled == nil {
		t.Fatal("CompileRoot returned nil")
	}
	if compiled.PathConstraints == nil {
		t.Fatal("PathConstraints is nil")
	}
	// Root "" or "type" should have constraints
	if _, ok := compiled.PathConstraints[""]; !ok {
		hasRoot := false
		for k := range compiled.PathConstraints {
			if k == "" || !strings.Contains(k, ".") {
				hasRoot = true
				break
			}
		}
		if !hasRoot {
			t.Error("expected root or top-level path in PathConstraints")
		}
	}
}

func TestCompileRoot_Indicator(t *testing.T) {
	schemaDir := findSchemaDirForCompile(t)
	if schemaDir == "" {
		t.Skip("cannot find cti-stix2-json-schemas/schemas")
	}
	raw, err := codegen.LoadSchemaDir(schemaDir)
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	typed := ConvertSchemas(raw)
	compiled, err := CompileRoot(typed, "sdos/indicator.json")
	if err != nil {
		t.Fatalf("CompileRoot: %v", err)
	}
	if compiled == nil || compiled.PathConstraints == nil {
		t.Fatal("CompileRoot returned nil or empty PathConstraints")
	}
	// Indicator has pattern, created, etc.
	if c, ok := compiled.PathConstraints["pattern"]; ok {
		if c.Type != "string" && c.Pattern == "" {
			t.Logf("pattern constraints: Type=%q Pattern=%q", c.Type, c.Pattern)
		}
	}
	// external_references.[].url or similar
	for path := range compiled.PathConstraints {
		if strings.Contains(path, "external_references") {
			return
		}
	}
	t.Logf("PathConstraints sample: %v", keys(compiled.PathConstraints))
}

func keys(m map[string]*Constraints) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}

// TestCompileRoot_oneOfAndNot verifies that oneOf and not are preserved and compiled into Constraints.
func TestCompileRoot_oneOfAndNot(t *testing.T) {
	// Use JSON so the schema has the same shape as when loaded from disk (e.g. []interface{} with map elements).
	oneOfJSON := `{"type":"object","oneOf":[{"required":["a"]},{"required":["b"]}]}`
	var rootSchema Schema
	if err := json.Unmarshal([]byte(oneOfJSON), &rootSchema); err != nil {
		t.Fatalf("unmarshal oneOf schema: %v", err)
	}
	notJSON := `{"type":"object","properties":{"tag":{"type":"string","not":{"type":"string","pattern":"^x"}}}}`
	var withNotSchema Schema
	if err := json.Unmarshal([]byte(notJSON), &withNotSchema); err != nil {
		t.Fatalf("unmarshal not schema: %v", err)
	}
	schemas := map[string]*Schema{
		"root.json":     &rootSchema,
		"with_not.json": &withNotSchema,
	}
	// oneOf at root
	compiled, err := CompileRoot(schemas, "root.json")
	if err != nil {
		t.Fatalf("CompileRoot: %v", err)
	}
	if compiled == nil {
		t.Fatal("CompileRoot returned nil")
	}
	c, ok := compiled.PathConstraints[""]
	if !ok {
		t.Fatal("expected root path constraints")
	}
	if c.OneOf == nil || len(c.OneOf) != 2 {
		t.Errorf("expected OneOf with 2 branches, got %v", c.OneOf)
	}
	if c.OneOf != nil && len(c.OneOf) >= 2 {
		if len(c.OneOf[0].Required) != 1 || c.OneOf[0].Required[0] != "a" {
			t.Errorf("first oneOf branch expected required [a], got %v", c.OneOf[0].Required)
		}
		if len(c.OneOf[1].Required) != 1 || c.OneOf[1].Required[0] != "b" {
			t.Errorf("second oneOf branch expected required [b], got %v", c.OneOf[1].Required)
		}
	}

	// not in property
	compiled2, err := CompileRoot(schemas, "with_not.json")
	if err != nil {
		t.Fatalf("CompileRoot(with_not): %v", err)
	}
	if compiled2 == nil {
		t.Fatal("CompileRoot returned nil")
	}
	cTag, ok := compiled2.PathConstraints["tag"]
	if !ok {
		t.Fatal("expected tag path constraints")
	}
	if cTag.Not == nil {
		t.Fatal("expected Not to be set on tag")
	}
	if cTag.Not.Type != "string" || cTag.Not.Pattern != "^x" {
		t.Errorf("expected Not type=string pattern=^x, got type=%q pattern=%q", cTag.Not.Type, cTag.Not.Pattern)
	}
}

// TestResolveFragment_oneOf verifies resolveFragment preserves oneOf (and that extractConstraints reads it).
func TestResolveFragment_oneOf(t *testing.T) {
	oneOfJSON := `{"type":"object","oneOf":[{"required":["a"]},{"required":["b"]}]}`
	var rootSchema Schema
	if err := json.Unmarshal([]byte(oneOfJSON), &rootSchema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	schemas := map[string]*Schema{"root.json": &rootSchema}
	effective := resolveFragment(schemas, "root.json", &rootSchema)
	if effective == nil {
		t.Fatal("resolveFragment returned nil")
	}
	if effective.OneOf == nil || len(effective.OneOf) != 2 {
		t.Fatalf("effective.OneOf expected 2 elements, got %v", effective.OneOf)
	}
	c := extractConstraints(effective)
	if c == nil {
		t.Fatal("extractConstraints returned nil")
	}
	if c.OneOf == nil || len(c.OneOf) != 2 {
		t.Errorf("expected c.OneOf with 2 branches, got %v", c.OneOf)
	}
}
