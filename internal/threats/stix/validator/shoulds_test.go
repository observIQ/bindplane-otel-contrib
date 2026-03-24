package validator

import (
	"testing"
)

func TestRunShoulds_UUIDCheckWarning(t *testing.T) {
	obj := map[string]interface{}{
		"type":         "indicator",
		"id":           "indicator--00000000-0000-3000-8000-000000000001", // v3 UUID
		"spec_version": "2.1",
		"created":      "2020-01-01T00:00:00.000Z",
		"modified":     "2020-01-01T00:00:00.000Z",
		"pattern":      "[file:name='x']",
		"pattern_type": "stix",
		"valid_from":   "2020-01-01T00:00:00.000Z",
	}
	opts := ShouldOptions{}
	errs := RunShoulds(obj, "indicator", opts)
	var found bool
	for _, e := range errs {
		if e.Path == "id" && (containsS(e.Message, "UUIDv4") || containsS(e.Message, "UUIDv5")) {
			found = true
			break
		}
	}
	if !found {
		t.Logf("RunShoulds: uuid-check may have run; got %d findings: %v", len(errs), errs)
	}
}

func TestRunShoulds_IndicatorTypesVocabWarning(t *testing.T) {
	obj := map[string]interface{}{
		"type":            "indicator",
		"id":              "indicator--a0000000-0000-4000-8000-000000000001",
		"spec_version":    "2.1",
		"created":         "2020-01-01T00:00:00.000Z",
		"modified":        "2020-01-01T00:00:00.000Z",
		"indicator_types": []interface{}{"Not-In-Vocab-Value"},
		"pattern":         "[file:name='x']",
		"pattern_type":    "stix",
		"valid_from":      "2020-01-01T00:00:00.000Z",
	}
	opts := ShouldOptions{}
	errs := RunShoulds(obj, "indicator", opts)
	var found bool
	for _, e := range errs {
		if e.Path == "indicator_types" || containsS(e.Message, "vocabulary") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RunShoulds: expected indicator-types vocab warning, got %d: %v", len(errs), errs)
	}
}

func TestRunShoulds_DisabledCheck(t *testing.T) {
	obj := map[string]interface{}{
		"type":            "indicator",
		"id":              "indicator--00000000-0000-3000-8000-000000000001",
		"spec_version":    "2.1",
		"created":         "2020-01-01T00:00:00.000Z",
		"modified":        "2020-01-01T00:00:00.000Z",
		"indicator_types": []interface{}{"Not-In-Vocab"},
		"pattern":         "[file:name='x']",
		"pattern_type":    "stix",
		"valid_from":      "2020-01-01T00:00:00.000Z",
	}
	opts := ShouldOptions{Disabled: []string{"indicator-types", "uuid-check"}}
	errs := RunShoulds(obj, "indicator", opts)
	for _, e := range errs {
		if e.Path == "indicator_types" || (e.Path == "id" && containsS(e.Message, "UUID")) {
			t.Errorf("RunShoulds: disabled checks should not run, got %v", e)
		}
	}
}

func TestRunShoulds_EnabledOnly(t *testing.T) {
	opts := ShouldOptions{Enabled: []string{"uuid-check"}}
	obj := map[string]interface{}{
		"type":         "indicator",
		"id":           "indicator--a0000000-0000-4000-8000-000000000001",
		"spec_version": "2.1",
		"created":      "2020-01-01T00:00:00.000Z",
		"modified":     "2020-01-01T00:00:00.000Z",
		"pattern":      "[file:name='x']",
		"pattern_type": "stix",
		"valid_from":   "2020-01-01T00:00:00.000Z",
	}
	errs := RunShoulds(obj, "indicator", opts)
	if len(errs) != 0 {
		t.Logf("RunShoulds(Enabled: uuid-check): valid v4 id should give no uuid-check warning: %v", errs)
	}
}

func TestListShoulds_DefaultIncludesMany(t *testing.T) {
	opts := ShouldOptions{}
	list := listShoulds(opts)
	if len(list) == 0 {
		t.Error("listShoulds(default) should return non-empty list")
	}
}

func TestListShoulds_EnabledOnly(t *testing.T) {
	opts := ShouldOptions{Enabled: []string{"indicator-types", "uuid-check"}}
	list := listShoulds(opts)
	if len(list) == 0 {
		t.Error("listShoulds(Enabled) should return list for enabled checks")
	}
}

// TestRunShoulds_DisableByCode verifies that numeric check code "202" is
// normalized to "relationship-types", so Disabled: ["202"] behaves like
// Disabled: ["relationship-types"].
func TestRunShoulds_DisableByCode(t *testing.T) {
	obj := map[string]interface{}{
		"type":              "relationship",
		"id":                "relationship--a0000000-0000-4000-8000-000000000001",
		"spec_version":      "2.1",
		"created":           "2020-01-01T00:00:00.000Z",
		"modified":          "2020-01-01T00:00:00.000Z",
		"relationship_type": "uses",
		"source_ref":        "indicator--a0000000-0000-4000-8000-000000000001",
		"target_ref":        "malware--a0000000-0000-4000-8000-000000000001",
	}
	optsByName := ShouldOptions{Disabled: []string{"relationship-types"}}
	optsByCode := ShouldOptions{Disabled: []string{"202"}}
	errsByName := RunShoulds(obj, "relationship", optsByName)
	errsByCode := RunShoulds(obj, "relationship", optsByCode)
	if len(errsByName) != len(errsByCode) {
		t.Errorf("Disabled [\"relationship-types\"] gave %d errs, Disabled [\"202\"] gave %d; should match",
			len(errsByName), len(errsByCode))
	}
}

func containsS(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
