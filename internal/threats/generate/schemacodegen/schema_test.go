package codegen

import (
	"testing"
)

func TestResolveRef_emptyRef(t *testing.T) {
	got := ResolveRef("sdos/identity.json", "")
	if got != "sdos/identity.json" {
		t.Errorf("ResolveRef(empty ref) = %q, want sdos/identity.json", got)
	}
}

func TestResolveRef_sameDir(t *testing.T) {
	got := ResolveRef("sdos/identity.json", "#/../common/core.json")
	if got != "common/core.json" {
		t.Errorf("ResolveRef(../common/core.json) = %q, want common/core.json", got)
	}
}

func TestResolveRef_child(t *testing.T) {
	got := ResolveRef("common/bundle.json", "#/objects")
	if got != "common/objects" {
		t.Errorf("ResolveRef(#/objects) = %q, want common/objects", got)
	}
}

func TestResolveRef_rootFromSubdir(t *testing.T) {
	got := ResolveRef("sdos/indicator.json", "#/../../common/core.json")
	if got != "common/core.json" {
		t.Errorf("ResolveRef(../../common/core.json) = %q, want common/core.json", got)
	}
}

func TestGetString(t *testing.T) {
	s := Schema{"title": "Identity", "type": "object"}
	if got := GetString(s, "title"); got != "Identity" {
		t.Errorf("GetString(title) = %q", got)
	}
	if got := GetString(s, "missing"); got != "" {
		t.Errorf("GetString(missing) = %q", got)
	}
	if got := GetString(s, "type"); got != "object" {
		t.Errorf("GetString(type) = %q", got)
	}
}

func TestGetArray(t *testing.T) {
	s := Schema{"required": []interface{}{"id", "type"}}
	arr := GetArray(s, "required")
	if len(arr) != 2 {
		t.Fatalf("GetArray(required) len = %d", len(arr))
	}
	arr = GetArray(s, "missing")
	if arr != nil {
		t.Errorf("GetArray(missing) = %v", arr)
	}
}

func TestGetMap(t *testing.T) {
	s := Schema{"properties": map[string]interface{}{"id": map[string]interface{}{"type": "string"}}}
	m := GetMap(s, "properties")
	if m == nil || m["id"] == nil {
		t.Errorf("GetMap(properties) = %v", m)
	}
	if GetMap(s, "missing") != nil {
		t.Error("GetMap(missing) should be nil")
	}
}

func TestGetType(t *testing.T) {
	if got := GetType(Schema{"type": "string"}); got != "string" {
		t.Errorf("GetType = %q", got)
	}
	if got := GetType(Schema{"type": "object"}); got != "object" {
		t.Errorf("GetType = %q", got)
	}
	if got := GetType(Schema{"allOf": []interface{}{}}); got != "" {
		t.Errorf("GetType(allOf) = %q", got)
	}
}

func TestFlattenObjectSchema_simple(t *testing.T) {
	schemas := map[string]Schema{
		"test.json": {
			"type": "object",
			"properties": map[string]interface{}{
				"id":   map[string]interface{}{"type": "string"},
				"name": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"id"},
		},
	}
	props, required := FlattenObjectSchema(schemas, "test.json")
	if props == nil || len(props) != 2 {
		t.Fatalf("FlattenObjectSchema props = %v", props)
	}
	if len(required) != 1 || required[0] != "id" {
		t.Errorf("required = %v", required)
	}
}

func TestFlattenObjectSchema_allOf_ref(t *testing.T) {
	schemas := map[string]Schema{
		"sdos/identity.json": {
			"allOf": []interface{}{
				map[string]interface{}{"$ref": "../common/core.json"},
				map[string]interface{}{
					"properties": map[string]interface{}{
						"name": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
		"common/core.json": {
			"type": "object",
			"properties": map[string]interface{}{
				"id":   map[string]interface{}{"type": "string"},
				"type": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"id", "type"},
		},
	}
	props, required := FlattenObjectSchema(schemas, "sdos/identity.json")
	if props == nil {
		t.Fatal("FlattenObjectSchema returned nil props")
	}
	if _, ok := props["id"]; !ok {
		t.Error("missing id from $ref common/core.json")
	}
	if _, ok := props["name"]; !ok {
		t.Error("missing name from inline allOf")
	}
	if len(required) != 2 {
		t.Errorf("required = %v", required)
	}
}

func TestRootObjectPaths(t *testing.T) {
	schemas := map[string]Schema{
		"common/bundle.json":    {"type": "object"},
		"sdos/identity.json":    {"type": "object"},
		"sdos/indicator.json":   {"type": "object"},
		"observables/file.json": {"type": "object"},
		"observables/ipv4.json": {"type": "object"},
	}
	paths := RootObjectPaths(schemas)
	seen := make(map[string]bool)
	for _, p := range paths {
		seen[p] = true
	}
	if !seen["common/bundle.json"] {
		t.Error("RootObjectPaths missing common/bundle.json")
	}
	if !seen["sdos/identity.json"] || !seen["sdos/indicator.json"] {
		t.Error("RootObjectPaths missing sdos")
	}
	if !seen["observables/file.json"] || !seen["observables/ipv4.json"] {
		t.Error("RootObjectPaths missing observables")
	}
}
