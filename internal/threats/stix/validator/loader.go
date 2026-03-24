// loader.go implements the STIX 2.1 validator schema loading.
package validator

import (
	"path/filepath"
	"strings"

	codegen "github.com/observiq/bindplane-otel-collector/internal/threats/generate/schemacodegen"
)

// Loader holds loaded schemas and the type-to-schema-path mapping.
// When compiled schemas are present, validation uses path→constraints lookups (O(1) per path).
type Loader struct {
	// Schemas is the full map from path (e.g. "common/core.json") to schema.
	Schemas map[string]*Schema

	// TypeToPath maps STIX object type (e.g. "indicator", "bundle", "file") to schema path.
	TypeToPath map[string]string

	// Compiled maps STIX type to compiled schema (path→constraints). BuiltinLoader sets from generatedCompiled;
	// Load(schemaDir) populates by compiling each root schema once at init.
	Compiled map[string]*CompiledSchema
}

// Load loads all schemas from schemaDir using codegen.LoadSchemaDir and builds the type→path map.
// schemaDir should be the path to cti-stix2-json-schemas/schemas.
// It also compiles each root schema once (resolve $ref, merge allOf) and caches in Loader.Compiled.
func Load(schemaDir string) (*Loader, error) {
	raw, err := codegen.LoadSchemaDir(schemaDir)
	if err != nil {
		return nil, err
	}
	typed := ConvertSchemas(raw)
	typeToPath := buildTypeToPath(typed)
	compiled := make(map[string]*CompiledSchema)
	for typeName, rootPath := range typeToPath {
		c, err := CompileRoot(typed, rootPath)
		if err != nil {
			return nil, err
		}
		if c != nil {
			compiled[typeName] = c
		}
	}
	return &Loader{Schemas: typed, TypeToPath: typeToPath, Compiled: compiled}, nil
}

// BuiltinLoader returns a Loader that uses the generated schema data (schemas_generated.go).
// No disk I/O or JSON unmarshaling at runtime. Use this when validating against the
// built-in STIX 2.1 schemas. The generated file is produced by go run ./cmd/codegen-validator
// (or make generate-validator).
func BuiltinLoader() *Loader {
	return &Loader{
		Schemas:    generatedSchemasTyped,
		TypeToPath: generatedTypeToPath,
		Compiled:   generatedCompiled,
	}
}

// ResolveRef resolves a $ref relative to basePath (same as codegen).
func ResolveRef(basePath, ref string) string {
	return codegen.ResolveRef(basePath, ref)
}

// schemaType extracts the STIX type string from a root object schema (e.g. "indicator", "bundle").
// It looks at properties.type.enum or allOf[].properties.type.enum.
func schemaType(s *Schema) string {
	if s == nil {
		return ""
	}
	// Top-level properties.type.enum (e.g. bundle, marking-definition)
	if s.Properties != nil {
		if typeProp := s.Properties["type"]; typeProp != nil && len(typeProp.Enum) == 1 {
			if t, ok := typeProp.Enum[0].(string); ok {
				return t
			}
		}
	}
	// allOf[].properties.type.enum (e.g. indicator, identity)
	for _, item := range s.AllOf {
		if item == nil || item.Properties == nil {
			continue
		}
		typeProp := item.Properties["type"]
		if typeProp == nil || len(typeProp.Enum) != 1 {
			continue
		}
		if t, ok := typeProp.Enum[0].(string); ok {
			return t
		}
	}
	return ""
}

// buildTypeToPath builds a map from STIX type to schema path for root object schemas.
func buildTypeToPath(schemas map[string]*Schema) map[string]string {
	out := make(map[string]string)
	// Bundle and key common types that have a single type enum
	roots := []string{
		"common/bundle.json",
		"common/marking-definition.json",
		"common/language-content.json",
	}
	for _, path := range roots {
		s := schemas[path]
		if s == nil {
			continue
		}
		if t := schemaType(s); t != "" {
			out[t] = path
		}
	}
	// SDOs, SROs, observables
	for path := range schemas {
		if strings.HasPrefix(path, "sdos/") || strings.HasPrefix(path, "sros/") || strings.HasPrefix(path, "observables/") {
			s := schemas[path]
			if s == nil {
				continue
			}
			if t := schemaType(s); t != "" {
				out[t] = path
			}
		}
	}
	return out
}

// SchemaForType returns the schema path for the given STIX type (e.g. "indicator"), or "" if unknown.
func (l *Loader) SchemaForType(typeName string) string {
	return l.TypeToPath[typeName]
}

// CompiledForType returns the compiled schema for the given STIX type, or nil if unknown/unavailable.
// O(1) lookup. Use for validation with path→constraints lookups.
func (l *Loader) CompiledForType(typeName string) *CompiledSchema {
	if l.Compiled == nil {
		return nil
	}
	return l.Compiled[typeName]
}

// SchemaAt returns the schema at the given path (e.g. "sdos/indicator.json"), or nil.
func (l *Loader) SchemaAt(path string) *Schema {
	return l.Schemas[path]
}

// NormalizeSchemaDir returns the cleaned absolute path for the schema directory if possible,
// otherwise the input. Used so that refs resolve consistently.
func NormalizeSchemaDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return filepath.Clean(abs)
}
