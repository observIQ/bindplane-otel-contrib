package ocsf

import (
	"testing"
)

func TestByFieldName(t *testing.T) {
	tests := []struct {
		name     string
		wantType string
		wantProp string
		wantOk   bool
	}{
		{"file.name", "file", "name", true},
		{"device.ip", "ipv4-addr", "value", true},
		{"process.name", "process", "name", true},
		{"hostname", "domain-name", "value", true},
		{"actor.user.uid", "user-account", "user_id", true},
		{"", "", "", false},
		{"  ", "", "", false},
		{"unknown.field.xyz", "artifact", "extensions.ocsf.value", true}, // default, never drop
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ok := ByFieldName(tt.name)
			if ok != tt.wantOk {
				t.Errorf("ByFieldName(%q) ok = %v, want %v", tt.name, ok, tt.wantOk)
			}
			if ok && (m.Type != tt.wantType || m.Property != tt.wantProp) {
				t.Errorf("ByFieldName(%q) = %+v, want Type=%q Property=%q", tt.name, m, tt.wantType, tt.wantProp)
			}
		})
	}
}

func TestByObservableTypeID(t *testing.T) {
	tests := []struct {
		typeID   int
		wantType string
		wantOk   bool
	}{
		{1, "domain-name", true},
		{2, "ipv4-addr", true},
		{7, "file", true},
		{9, "process", true},
		{0, "", false},
		{10, "", false},
		{17, "", false},
		{18, "", false},
		{99, "", false},
	}
	for _, tt := range tests {
		m, ok := ByObservableTypeID(tt.typeID)
		if ok != tt.wantOk {
			t.Errorf("ByObservableTypeID(%d) ok = %v, want %v", tt.typeID, ok, tt.wantOk)
		}
		if ok && m.Type != tt.wantType {
			t.Errorf("ByObservableTypeID(%d) Type = %q, want %q", tt.typeID, m.Type, tt.wantType)
		}
	}
}

func TestByObservableTypeName(t *testing.T) {
	tests := []struct {
		typeName string
		wantType string
		wantOk   bool
	}{
		{"Hostname", "domain-name", true},
		{"IP Address", "ipv4-addr", true},
		{"File Name", "file", true},
		{"Process", "process", true},
		{"Uniform Resource Locator", "url", true},
		{"", "", false},
		{"Unknown", "", false},
	}
	for _, tt := range tests {
		m, ok := ByObservableTypeName(tt.typeName)
		if ok != tt.wantOk {
			t.Errorf("ByObservableTypeName(%q) ok = %v, want %v", tt.typeName, ok, tt.wantOk)
		}
		if ok && m.Type != tt.wantType {
			t.Errorf("ByObservableTypeName(%q) Type = %q, want %q", tt.typeName, m.Type, tt.wantType)
		}
	}
}

func TestPatternPath(t *testing.T) {
	m := STIXMapping{Type: "file", Property: "name"}
	if g := m.PatternPath(); g != "file:name" {
		t.Errorf("PatternPath() = %q, want file:name", g)
	}
}

func TestMappingsMerge(t *testing.T) {
	// Curated overrides take precedence; we should have at least curated count + generated
	if len(ocsfFieldToSTIX) < len(curatedFieldOverrides) {
		t.Errorf("ocsfFieldToSTIX has %d entries, want at least %d (curated)", len(ocsfFieldToSTIX), len(curatedFieldOverrides))
	}
	// Curated keys must be present with strong mapping (not artifact)
	for k, v := range curatedFieldOverrides {
		got, ok := ocsfFieldToSTIX[k]
		if !ok {
			t.Errorf("curated key %q missing from ocsfFieldToSTIX", k)
			continue
		}
		if got.Type != v.Type || got.Property != v.Property {
			t.Errorf("curated %q: got %+v, want %+v", k, got, v)
		}
	}
}
