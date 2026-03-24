// Package validatorcodegen generates validator schema data (type→path map and full
// schema literals) from the STIX 2.1 JSON schema directory. Unmarshal happens once
// at codegen time; runtime uses the generated Go data with no JSON parsing.
package validatorcodegen

import (
	"strings"

	codegen "github.com/observiq/bindplane-otel-collector/internal/threats/generate/schemacodegen"
)

// Schema is the codegen schema type.
type Schema = codegen.Schema

// Load reads the schema directory and returns schemas plus the type→path map.
func Load(schemaDir string) (schemas map[string]Schema, typeToPath map[string]string, err error) {
	schemas, err = codegen.LoadSchemaDir(schemaDir)
	if err != nil {
		return nil, nil, err
	}
	typeToPath = buildTypeToPath(schemas)
	return schemas, typeToPath, nil
}

func schemaType(s Schema) string {
	if props := codegen.GetMap(s, "properties"); props != nil {
		if typeProp, ok := props["type"].(map[string]interface{}); ok {
			if enum := codegen.GetArray(Schema(typeProp), "enum"); len(enum) == 1 {
				if t, ok := enum[0].(string); ok {
					return t
				}
			}
		}
	}
	for _, item := range codegen.GetArray(s, "allOf") {
		if m, ok := item.(map[string]interface{}); ok {
			props := codegen.GetMap(Schema(m), "properties")
			if props == nil {
				continue
			}
			typeProp, ok := props["type"].(map[string]interface{})
			if !ok {
				continue
			}
			enum := codegen.GetArray(Schema(typeProp), "enum")
			if len(enum) != 1 {
				continue
			}
			if t, ok := enum[0].(string); ok {
				return t
			}
		}
	}
	return ""
}

func buildTypeToPath(schemas map[string]Schema) map[string]string {
	out := make(map[string]string, 64)
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
	for path, s := range schemas {
		if s == nil {
			continue
		}
		seg := path
		if i := strings.IndexByte(path, '/'); i > 0 {
			seg = path[:i]
		}
		if seg == "sdos" || seg == "sros" || seg == "observables" {
			if t := schemaType(s); t != "" {
				out[t] = path
			}
		}
	}
	return out
}
