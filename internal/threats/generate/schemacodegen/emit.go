package codegen

import (
	"fmt"
	"sort"
	"strings"
)

// GoType returns the Go type string for a property schema (e.g. "string", "[]string").
// refs is used to resolve $ref to a Go type name (e.g. identifier -> string).
func GoType(prop Schema, basePath string, schemas map[string]Schema, refs map[string]string) string {
	if prop == nil {
		return "interface{}"
	}
	ref := GetString(prop, "$ref")
	if ref != "" {
		resolved := ResolveRef(basePath, ref)
		if name, ok := refs[resolved]; ok {
			return name
		}
		// Resolve and infer from referenced schema
		if s, ok := schemas[resolved]; ok {
			t := GetType(s)
			if t == "string" {
				return "string"
			}
			if t == "object" {
				return goTypeNameFromPath(resolved)
			}
			if t == "" {
				// allOf only - use title or path
				title := GetString(s, "title")
				if title != "" {
					return goTypeName(title)
				}
				return goTypeNameFromPath(resolved)
			}
		}
		return "interface{}"
	}
	for _, item := range GetArray(prop, "allOf") {
		if m, ok := item.(map[string]interface{}); ok {
			ref := GetString(Schema(m), "$ref")
			if ref != "" {
				return GoType(Schema(m), basePath, schemas, refs)
			}
		}
	}
	typ := GetString(prop, "type")
	switch typ {
	case "string":
		return "string"
	case "integer":
		return "int64"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "array":
		items := GetMap(prop, "items")
		if items == nil {
			return "[]interface{}"
		}
		elem := GoType(items, basePath, schemas, refs)
		return "[]" + elem
	case "object":
		return "map[string]interface{}"
	default:
		return "interface{}"
	}
}

func goTypeNameFromPath(path string) string {
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	base = strings.TrimSuffix(base, ".json")
	return goTypeName(base)
}

// goTypeName converts a schema title or filename to PascalCase Go type name.
func goTypeName(s string) string {
	s = strings.TrimSuffix(s, ".json")
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// EmitStruct writes a Go struct for the given flattened properties and required list.
func EmitStruct(buf *strings.Builder, typeName string, props map[string]Schema, required []string, basePath string, schemas map[string]Schema, refs map[string]string) {
	requiredSet := make(map[string]bool)
	for _, r := range required {
		requiredSet[r] = true
	}
	buf.WriteString("// " + typeName + " represents a STIX " + typeName + " object.\n")
	buf.WriteString("type " + typeName + " struct {\n")
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		prop := props[k]
		goName := jsonToGoField(k)
		goType := GoType(prop, basePath, schemas, refs)
		tag := k
		if !requiredSet[k] {
			tag += ",omitempty"
		}
		buf.WriteString(fmt.Sprintf("\t%s %s `json:\"%s\"`\n", goName, goType, tag))
	}
	buf.WriteString("}\n\n")
}

func jsonToGoField(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}
