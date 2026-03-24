package enumcodegen

import (
	"strings"
	"testing"
)

func TestGenerateWithPackage_slice(t *testing.T) {
	raw := map[string]interface{}{
		"FOO": []interface{}{"a", "b", "c"},
	}
	code, err := GenerateWithPackage(raw, "mypkg", "Source: test.")
	if err != nil {
		t.Fatalf("GenerateWithPackage: %v", err)
	}
	s := string(code)
	if !strings.Contains(s, "package mypkg") {
		t.Error("missing package mypkg")
	}
	if !strings.Contains(s, "var FOO = []string{") {
		t.Error("missing var FOO slice")
	}
	if !strings.Contains(s, `"a"`) || !strings.Contains(s, `"b"`) || !strings.Contains(s, `"c"`) {
		t.Error("slice values not in output")
	}
}

func TestGenerateWithPackage_mapStringSlice(t *testing.T) {
	raw := map[string]interface{}{
		"M": map[string]interface{}{
			"k1": []interface{}{"v1"},
			"k2": []interface{}{"a", "b"},
		},
	}
	code, err := GenerateWithPackage(raw, "mypkg", "Source: test.")
	if err != nil {
		t.Fatalf("GenerateWithPackage: %v", err)
	}
	s := string(code)
	if !strings.Contains(s, "var M = map[string][]string{") {
		t.Error("missing map[string][]string")
	}
	if !strings.Contains(s, `"k1": []string{"v1"}`) {
		t.Error("k1 entry missing")
	}
}

func TestGenerateWithPackage_CHECK_CODES(t *testing.T) {
	raw := map[string]interface{}{
		"CHECK_CODES": map[string]interface{}{
			"202": "timestamp",
			"210": "required",
		},
	}
	code, err := GenerateWithPackage(raw, "mypkg", "Source: test.")
	if err != nil {
		t.Fatalf("GenerateWithPackage: %v", err)
	}
	s := string(code)
	if !strings.Contains(s, "var CHECK_CODES = map[string]string{") {
		t.Error("CHECK_CODES missing")
	}
	if !strings.Contains(s, `"202": "timestamp"`) {
		t.Error("CHECK_CODES entry missing")
	}
}

func Test_asStringSlice_valid(t *testing.T) {
	in := []interface{}{"x", "y"}
	out, err := asStringSlice(in, "ctx")
	if err != nil {
		t.Fatalf("asStringSlice: %v", err)
	}
	if len(out) != 2 || out[0] != "x" || out[1] != "y" {
		t.Errorf("got %v", out)
	}
}

func Test_asStringSlice_invalidElement(t *testing.T) {
	in := []interface{}{"a", 42, "b"}
	_, err := asStringSlice(in, "ctx")
	if err == nil {
		t.Fatal("expected error for non-string element")
	}
	if !strings.Contains(err.Error(), "element 1") {
		t.Errorf("error message: %v", err)
	}
}

func Test_relationshipTargets_string(t *testing.T) {
	got := relationshipTargets("indicator")
	if len(got) != 1 || got[0] != "indicator" {
		t.Errorf("relationshipTargets(string) = %v", got)
	}
}

func Test_relationshipTargets_slice(t *testing.T) {
	got := relationshipTargets([]interface{}{"a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("relationshipTargets(slice) = %v", got)
	}
}

func Test_relationshipTargets_invalid(t *testing.T) {
	got := relationshipTargets(123)
	if got != nil {
		t.Errorf("relationshipTargets(non-string/slice) = %v, want nil", got)
	}
}

func Test_sortedKeys(t *testing.T) {
	m := map[string]interface{}{"c": 1, "a": 2, "b": 3}
	keys := sortedKeys(m)
	if len(keys) != 3 {
		t.Fatalf("len(keys) = %d", len(keys))
	}
	if keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Errorf("sortedKeys = %v", keys)
	}
}

func Test_emitGeneric_unsupportedType(t *testing.T) {
	var b strings.Builder
	err := emitGeneric(&b, "X", 42)
	if err == nil {
		t.Fatal("expected error for int")
	}
	if !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("error: %v", err)
	}
}
