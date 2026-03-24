package pattern

import (
	"reflect"
	"testing"
)

// Test patterns from cti-stix-validator indicator_tests.py.
// Valid: syntax is valid per STIXPattern.g4.
// Invalid: syntax errors (wrong quotes, malformed); semantic rules (e.g. type/property length) are enforced by the validator, not the pattern package.
var (
	validPatterns = []string{
		`[file:hashes.'SHA-256' = 'aec070645fe53ee3b3763059376134f058cc337247c978add178b6ccdfb0019f']`,
		`[file:name = 'x']`,
		`[foo:name = 'something']`,
		`[file:foo = 'something']`,
		`[windows-registry-key:values[*].data='badstuff']`,
		`[windows-registry-key:key LIKE 'HKEY_LOCAL_MACHINE\\Foo\\Bar%']`,
		`[x-foo-bar:bizz MATCHES 'buzz']`,
		`[file:hashes.'MD5' = 'y']`,
	}
	invalidPatterns = []string{
		// Wrong quotes (double quote not valid in pattern string literal)
		`[file:hashes."SHA-256" = 'aec070645fe53ee3b3763059376134f058cc337247c978add178b6ccdfb0019f']`,
		// Invalid character / malformed
		`[file:name = 'x'`,  // missing ]
		`[file:name ! 'x']`, // invalid operator
	}
)

func TestValidatePattern_ValidPatterns(t *testing.T) {
	for i, pat := range validPatterns {
		msgs, err := ValidatePattern(pat)
		if err != nil {
			t.Errorf("pattern %d: unexpected error: %v", i, err)
			continue
		}
		if len(msgs) != 0 {
			t.Errorf("pattern %d: expected no validation errors, got %v", i, msgs)
		}
	}
}

func TestValidatePattern_InvalidPatterns(t *testing.T) {
	for i, pat := range invalidPatterns {
		msgs, err := ValidatePattern(pat)
		if err != nil {
			t.Errorf("pattern %d: unexpected error: %v", i, err)
			continue
		}
		if len(msgs) == 0 {
			t.Errorf("pattern %d: expected validation errors for %q", i, pat)
		}
	}
}

func TestInspectPattern_KnownPatterns(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		expected map[string][]string
	}{
		{
			name:     "file name",
			pattern:  `[file:name = 'x']`,
			expected: map[string][]string{"file": {"name"}},
		},
		{
			name:     "file hashes SHA-256",
			pattern:  `[file:hashes.'SHA-256' = 'a']`,
			expected: map[string][]string{"file": {"hashes.'SHA-256'"}},
		},
		{
			name:     "windows-registry-key values star data",
			pattern:  `[windows-registry-key:values[*].data='badstuff']`,
			expected: map[string][]string{"windows-registry-key": {"values[*].data"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InspectPattern(tt.pattern)
			if err != nil {
				t.Fatalf("InspectPattern: %v", err)
			}
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("InspectPattern:\n  got %v\n  want %v", got, tt.expected)
			}
		})
	}
}

func TestInspectPattern_InvalidPattern(t *testing.T) {
	_, err := InspectPattern(`[file:hashes."SHA-256" = 'x']`)
	if err == nil {
		t.Error("InspectPattern: expected error for invalid pattern, got nil")
	}
}

func TestParse_Simple(t *testing.T) {
	l := newLexer(`[file:name = 'x']`)
	stream := newTokenStream(l)
	if stream.Err() != "" {
		t.Fatalf("lex: %s", stream.Err())
	}
	paths, errs := parse(stream, "")
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 path, got %d", len(paths))
	}
	if paths[0].ObjectType != "file" || paths[0].Path != "name" {
		t.Errorf("got %+v", paths[0])
	}
}

func TestParseToAST_Valid(t *testing.T) {
	pat, err := ParseToAST(`[file:name = 'x']`)
	if err != nil {
		t.Fatalf("ParseToAST: %v", err)
	}
	if pat == nil || pat.ObservationExpressions == nil {
		t.Fatal("expected non-nil pattern")
	}
	if len(pat.ObservationExpressions.Ors) != 1 {
		t.Fatalf("expected 1 Or, got %d", len(pat.ObservationExpressions.Ors))
	}
	or := pat.ObservationExpressions.Ors[0]
	if len(or.Ands) != 1 {
		t.Fatalf("expected 1 And, got %d", len(or.Ands))
	}
	and := or.Ands[0]
	if len(and.Exprs) != 1 {
		t.Fatalf("expected 1 Expr, got %d", len(and.Exprs))
	}
	expr := and.Exprs[0]
	if expr.Comparison == nil {
		t.Fatal("expected Comparison")
	}
	if len(expr.Comparison.Ands) != 1 {
		t.Fatalf("expected 1 comparison And, got %d", len(expr.Comparison.Ands))
	}
	pt := expr.Comparison.Ands[0].PropTests[0]
	eq, ok := pt.(*PropTestEqual)
	if !ok {
		t.Fatalf("expected PropTestEqual, got %T", pt)
	}
	if eq.ObjectPath.ObjectType != "file" {
		t.Errorf("ObjectType: got %q", eq.ObjectPath.ObjectType)
	}
	if len(eq.ObjectPath.Path) != 1 || eq.ObjectPath.Path[0].Key != "name" {
		t.Errorf("Path: got %+v", eq.ObjectPath.Path)
	}
	if eq.Literal.Str == nil || *eq.Literal.Str != "x" {
		t.Errorf("Literal: got %+v", eq.Literal)
	}
}

func TestParseToAST_Invalid(t *testing.T) {
	_, err := ParseToAST(`[file:name = 'x'`)
	if err == nil {
		t.Error("ParseToAST: expected error for invalid pattern")
	}
}
