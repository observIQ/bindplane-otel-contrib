package validator

import (
	"testing"
)

func TestRunMUSTs_TimestampInvalid(t *testing.T) {
	obj := map[string]interface{}{
		"type":         "indicator",
		"id":           "indicator--x",
		"spec_version": "2.1",
		"created":      "2020-01-32T00:00:00.000Z", // invalid date
		"modified":     "2020-01-01T00:00:00.000Z",
		"pattern":      "[file:name='x']",
		"pattern_type": "stix",
		"valid_from":   "2020-01-01T00:00:00.000Z",
	}
	errs := RunMUSTs(obj, "indicator", nil)
	var found bool
	for _, e := range errs {
		if e.Path == "created" && contains(e.Message, "timestamp") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RunMUSTs: expected timestamp error on created, got %d errors: %v", len(errs), errs)
	}
}

func TestRunMUSTs_TimestampCompare(t *testing.T) {
	obj := map[string]interface{}{
		"type":         "indicator",
		"id":           "indicator--x",
		"spec_version": "2.1",
		"created":      "2020-01-02T00:00:00.000Z",
		"modified":     "2020-01-01T00:00:00.000Z", // before created
		"pattern":      "[file:name='x']",
		"pattern_type": "stix",
		"valid_from":   "2020-01-01T00:00:00.000Z",
		"valid_until":  "2020-01-03T00:00:00.000Z",
	}
	errs := RunMUSTs(obj, "indicator", nil)
	var found bool
	for _, e := range errs {
		if e.Path == "modified" && contains(e.Message, "later than or equal to") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RunMUSTs: expected modified >= created error, got %d errors: %v", len(errs), errs)
	}
}

func TestRunMUSTs_LanguageInvalid(t *testing.T) {
	obj := map[string]interface{}{
		"type":         "campaign",
		"id":           "campaign--x",
		"spec_version": "2.1",
		"created":      "2020-01-01T00:00:00.000Z",
		"modified":     "2020-01-01T00:00:00.000Z",
		"name":         "test",
		"lang":         "not-a-valid-lang-code",
	}
	errs := RunMUSTs(obj, "campaign", nil)
	var found bool
	for _, e := range errs {
		if e.Path == "lang" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RunMUSTs: expected lang error, got %d errors: %v", len(errs), errs)
	}
}

func TestRunMUSTs_ObjectMarkingCircularRefs(t *testing.T) {
	obj := map[string]interface{}{
		"type":                "marking-definition",
		"id":                  "marking-definition--x",
		"spec_version":        "2.1",
		"created":             "2020-01-01T00:00:00.000Z",
		"definition_type":     "tlp",
		"definition":          map[string]interface{}{"tlp": "white"},
		"object_marking_refs": []interface{}{"marking-definition--x"},
	}
	errs := RunMUSTs(obj, "marking-definition", nil)
	var found bool
	for _, e := range errs {
		if e.Path == "object_marking_refs" && contains(e.Message, "circular") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RunMUSTs: expected circular ref error, got %d errors: %v", len(errs), errs)
	}
}

func TestRunMUSTs_ValidIndicatorNoMUSTErrors(t *testing.T) {
	obj := map[string]interface{}{
		"type":         "indicator",
		"id":           "indicator--a0000000-0000-4000-8000-000000000001",
		"spec_version": "2.1",
		"created":      "2020-01-01T00:00:00.000Z",
		"modified":     "2020-01-01T00:00:00.000Z",
		"pattern":      "[file:name = 'x']",
		"pattern_type": "stix",
		"valid_from":   "2020-01-01T00:00:00.000Z",
	}
	errs := RunMUSTs(obj, "indicator", nil)
	for _, e := range errs {
		if e.Path != "" || e.Message != "" {
			t.Errorf("RunMUSTs: expected no MUST errors for valid indicator, got: %v", errs)
			break
		}
	}
}

func TestRunMUSTs_InvalidPattern(t *testing.T) {
	obj := map[string]interface{}{
		"type":         "indicator",
		"id":           "indicator--x",
		"spec_version": "2.1",
		"created":      "2020-01-01T00:00:00.000Z",
		"modified":     "2020-01-01T00:00:00.000Z",
		"pattern":      "[file:name = ", // invalid pattern
		"pattern_type": "stix",
		"valid_from":   "2020-01-01T00:00:00.000Z",
	}
	errs := RunMUSTs(obj, "indicator", nil)
	var found bool
	for _, e := range errs {
		if e.Path == "pattern" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RunMUSTs: expected pattern error, got %d errors: %v", len(errs), errs)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && len(sub) > 0 && findSub(s, sub)))
}

func findSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
