package validator

import (
	"testing"
)

func TestValidateObject_MinimalIndicator(t *testing.T) {
	loader := BuiltinLoader()
	if loader == nil {
		t.Fatal("BuiltinLoader() = nil")
	}
	// Minimal valid indicator
	instance := map[string]interface{}{
		"type":         "indicator",
		"spec_version": "2.1",
		"id":           "indicator--00000000-0000-4000-8000-000000000001",
		"created":      "2020-01-01T00:00:00.000Z",
		"modified":     "2020-01-01T00:00:00.000Z",
		"pattern":      "[file:hashes.MD5 = 'd41d8cd98f00b204e9800998ecf8427e']",
		"pattern_type": "stix",
		"valid_from":   "2020-01-01T00:00:00.000Z",
	}
	errs, err := ValidateObject(instance, "indicator", loader)
	if err != nil {
		t.Fatalf("ValidateObject: %v", err)
	}
	if len(errs) > 0 {
		t.Errorf("expected no errors for minimal valid indicator, got %d: %v", len(errs), errs)
	}
}

func TestValidateObject_MissingRequired(t *testing.T) {
	loader := BuiltinLoader()
	instance := map[string]interface{}{
		"type": "indicator",
		// missing id, created, modified, pattern, pattern_type, valid_from
	}
	errs, err := ValidateObject(instance, "indicator", loader)
	if err != nil {
		t.Fatalf("ValidateObject: %v", err)
	}
	if len(errs) == 0 {
		t.Error("expected errors for missing required fields, got none")
	}
}

func TestValidateObject_UnknownType(t *testing.T) {
	loader := BuiltinLoader()
	instance := map[string]interface{}{"type": "unknown-type"}
	_, err := ValidateObject(instance, "unknown-type", loader)
	if err == nil {
		t.Error("expected error for unknown type")
	}
}

func TestValidateObject_NilLoader(t *testing.T) {
	_, err := ValidateObject(map[string]interface{}{}, "indicator", nil)
	if err == nil {
		t.Error("expected error for nil loader")
	}
}

// TestWalkInstance_oneOf verifies oneOf: at least one branch must pass.
func TestWalkInstance_oneOf(t *testing.T) {
	oneOfConstraints := map[string]*Constraints{
		"": {
			OneOf: []*Constraints{
				{Required: []string{"a"}},
				{Required: []string{"b"}},
			},
		},
	}

	// Pass: exactly one branch matches (has "a" only)
	var errs []ValidationError
	walkInstance(map[string]interface{}{"a": 1}, "", oneOfConstraints, &errs)
	if hasMessage(errs, "must match at least one of oneOf") {
		t.Errorf("expected no oneOf error for {\"a\":1}, got %v", errs)
	}

	// Fail: no branch matches
	errs = nil
	walkInstance(map[string]interface{}{}, "", oneOfConstraints, &errs)
	if !hasMessage(errs, "must match at least one of oneOf") {
		t.Errorf("expected oneOf error for {}, got %v", errs)
	}

	// Pass: both branches match (at least one is enough)
	errs = nil
	walkInstance(map[string]interface{}{"a": 1, "b": 2}, "", oneOfConstraints, &errs)
	if hasMessage(errs, "must match at least one of oneOf") {
		t.Errorf("expected no oneOf error for {\"a\":1,\"b\":2} when at least one branch matches, got %v", errs)
	}
}

// TestWalkInstance_not verifies not: value must not match the negated schema.
func TestWalkInstance_not(t *testing.T) {
	notConstraints := map[string]*Constraints{
		"":    nil, // root allows object, no constraint
		"tag": {Not: &Constraints{Pattern: "^(x|y)$"}},
	}

	// Fail: value matches negated pattern
	var errs []ValidationError
	walkInstance(map[string]interface{}{"tag": "x"}, "", notConstraints, &errs)
	if !hasMessage(errs, "must not match schema") {
		t.Errorf("expected must not match schema for tag=x, got %v", errs)
	}

	// Pass: value does not match
	errs = nil
	walkInstance(map[string]interface{}{"tag": "z"}, "", notConstraints, &errs)
	if hasMessage(errs, "must not match schema") {
		t.Errorf("expected no not error for tag=z, got %v", errs)
	}
}

func hasMessage(errs []ValidationError, msg string) bool {
	for _, e := range errs {
		if e.Message == msg {
			return true
		}
	}
	return false
}
