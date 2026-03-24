package pattern

import (
	"reflect"
	"testing"
)

func TestLexer_SimplePattern(t *testing.T) {
	toks, err := lexAll(`[file:name = 'x']`)
	if err != "" {
		t.Fatalf("lex error: %s", err)
	}
	expected := []tokenKind{tokLBRACK, tokIdentNoHyphen, tokCOLON, tokIdentNoHyphen, tokEQ, tokString, tokRBRACK, tokEOF}
	if len(toks) != len(expected) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(expected), toks)
	}
	for i := range expected {
		if toks[i].kind != expected[i] {
			t.Errorf("token %d: got %v want %v", i, toks[i].kind, expected[i])
		}
	}
	if toks[1].lit != "file" || toks[3].lit != "name" {
		t.Errorf("idents: file=%q name=%q", toks[1].lit, toks[3].lit)
	}
}

func TestLexer_StringWithEscapes(t *testing.T) {
	// In Go source we need \\ to get one \ in the string, so the pattern is [x = '\'foo\\' ]
	toks, err := lexAll(`[x = '\'foo\\' ]`)
	if err != "" {
		t.Fatalf("lex error: %s", err)
	}
	// [ x = '\'foo\\' ] -> LBRACK ident EQ string RBRACK EOF
	var gotKinds []tokenKind
	for _, tok := range toks {
		gotKinds = append(gotKinds, tok.kind)
		if tok.kind == tokString {
			if tok.lit != `'\'foo\\'` {
				t.Errorf("string token lit = %q", tok.lit)
			}
			return
		}
	}
	t.Errorf("expected string token in sequence: %v", gotKinds)
}

func TestLexer_InvalidQuote(t *testing.T) {
	_, err := lexAll(`[file:hashes."SHA-256" = 'x']`)
	if err == "" {
		t.Error("expected lex error for double-quoted string")
	}
}

func TestLexer_IdentWithHyphen(t *testing.T) {
	toks, err := lexAll(`[windows-registry-key:values[*].data='x']`)
	if err != "" {
		t.Fatalf("lex error: %s", err)
	}
	// windows-registry-key should be IdentWithHyphen
	var found bool
	for _, tok := range toks {
		if tok.kind == tokIdentWithHyphen && tok.lit == "windows-registry-key" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected identifier with hyphen windows-registry-key in %v", toks)
	}
}

func TestLexer_Keywords(t *testing.T) {
	toks, err := lexAll(`[a AND b OR c NOT d]`)
	if err != "" {
		t.Fatalf("lex error: %s", err)
	}
	kinds := make([]tokenKind, 0, len(toks))
	for _, tok := range toks {
		kinds = append(kinds, tok.kind)
	}
	expected := []tokenKind{tokLBRACK, tokIdentNoHyphen, tokAND, tokIdentNoHyphen, tokOR, tokIdentNoHyphen, tokNOT, tokIdentNoHyphen, tokRBRACK, tokEOF}
	if !reflect.DeepEqual(kinds, expected) {
		t.Errorf("got %v, want %v", kinds, expected)
	}
}
