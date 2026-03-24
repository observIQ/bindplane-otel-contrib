// Package codegen parses STIX 2.1 JSON schemas and generates Go structs.
// It uses github.com/goccy/go-json for parsing.
package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Schema is a JSON Schema document (Draft 2020-12) as a tree of maps/slices.
type Schema map[string]interface{}

// LoadSchemaDir loads all .json files under dir into a map keyed by path relative to dir.
// Paths use forward slashes (e.g. "common/bundle.json").
func LoadSchemaDir(dir string) (map[string]Schema, error) {
	out := make(map[string]Schema)
	dir = filepath.Clean(dir)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if filepath.Base(path) == "examples" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".json" {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var s Schema
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		out[key] = s
		return nil
	})
	return out, err
}

// ResolveRef resolves a $ref path relative to basePath.
// basePath and ref are forward-slash paths (e.g. "sdos/identity.json", "../common/core.json").
// Returns the resolved path relative to schema root.
func ResolveRef(basePath, ref string) string {
	ref = strings.TrimPrefix(ref, "#/")
	if ref == "" {
		return basePath
	}
	baseDir := filepath.ToSlash(filepath.Dir(basePath))
	if baseDir == "." {
		baseDir = ""
	}
	parts := strings.Split(ref, "/")
	var out []string
	for _, p := range strings.Split(baseDir, "/") {
		if p != "" {
			out = append(out, p)
		}
	}
	for _, p := range parts {
		switch p {
		case "":
			out = out[:0]
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		case ".":
			// no-op
		default:
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

// GetString returns a string from the schema map.
func GetString(m Schema, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetArray returns a []interface{} from the schema map.
func GetArray(m Schema, key string) []interface{} {
	if v, ok := m[key]; ok && v != nil {
		if a, ok := v.([]interface{}); ok {
			return a
		}
	}
	return nil
}

// GetMap returns a map[string]interface{} from the schema map.
func GetMap(m Schema, key string) map[string]interface{} {
	if v, ok := m[key]; ok && v != nil {
		if mm, ok := v.(map[string]interface{}); ok {
			return mm
		}
	}
	return nil
}

// GetType returns the JSON Schema type (e.g. "object", "string").
func GetType(m Schema) string {
	if s := GetString(m, "type"); s != "" {
		return s
	}
	// allOf/oneOf/anyOf don't have type at top level
	return ""
}

// FlattenObjectSchema resolves $ref and allOf for an object schema and returns
// merged properties and required lists. schemas is the full map from LoadSchemaDir.
func FlattenObjectSchema(schemas map[string]Schema, path string) (props map[string]Schema, required []string) {
	s, ok := schemas[path]
	if !ok {
		return nil, nil
	}
	props = make(map[string]Schema)
	required = nil
	seenRequired := make(map[string]bool)

	var merge func(Schema)
	merge = func(m Schema) {
		if m == nil {
			return
		}
		// Merge "required"
		for _, r := range GetArray(m, "required") {
			if str, ok := r.(string); ok && !seenRequired[str] {
				seenRequired[str] = true
				required = append(required, str)
			}
		}
		// Merge "properties"
		for k, v := range GetMap(m, "properties") {
			if vm, ok := v.(map[string]interface{}); ok {
				props[k] = vm
			}
		}
		// Resolve allOf
		for _, item := range GetArray(m, "allOf") {
			if vm, ok := item.(map[string]interface{}); ok {
				ref := GetString(vm, "$ref")
				if ref != "" {
					nextPath := ResolveRef(path, ref)
					if next, ok := schemas[nextPath]; ok {
						merge(next)
					}
					// Also merge inline properties from this allOf item
					for k, v := range GetMap(Schema(vm), "properties") {
						if vmap, ok := v.(map[string]interface{}); ok {
							props[k] = vmap
						}
					}
					for _, r := range GetArray(Schema(vm), "required") {
						if str, ok := r.(string); ok && !seenRequired[str] {
							seenRequired[str] = true
							required = append(required, str)
						}
					}
				} else {
					merge(Schema(vm))
				}
			}
		}
		// Single $ref (no allOf)
		if ref := GetString(m, "$ref"); ref != "" {
			nextPath := ResolveRef(path, ref)
			if next, ok := schemas[nextPath]; ok {
				merge(next)
			}
		}
	}

	merge(s)
	// Top-level properties/required
	for k, v := range GetMap(s, "properties") {
		if vm, ok := v.(map[string]interface{}); ok {
			props[k] = vm
		}
	}
	for _, r := range GetArray(s, "required") {
		if str, ok := r.(string); ok && !seenRequired[str] {
			required = append(required, str)
		}
	}
	return props, required
}
