package pattern

import (
	"fmt"
	"strings"
)

// pathRef holds an object type and property path from one objectPath in the pattern.
type pathRef struct {
	ObjectType string
	Path       string
}

// parser holds state for recursive descent parsing.
type parser struct {
	stream *tokenStream
	errors []string
	paths  []pathRef
}

func (p *parser) cur() token {
	return p.stream.Cur()
}

func (p *parser) advance() token {
	return p.stream.Advance()
}

func (p *parser) want(k tokenKind) bool {
	if p.cur().kind == k {
		p.advance()
		return true
	}
	return false
}

func (p *parser) addError(msg string) {
	p.errors = append(p.errors, msg)
}

// parsePattern parses the top-level pattern: observationExpressions EOF.
func (p *parser) parsePattern() {
	p.parseObservationExpressions()
	if p.cur().kind != tokEOF {
		p.addError(fmt.Sprintf("unexpected token %s at end", p.cur().kind))
	}
}

// observationExpressions : observationExpressions FOLLOWEDBY observationExpressions | observationExpressionOr
func (p *parser) parseObservationExpressions() {
	p.parseObservationExpressionOr()
	for p.cur().kind == tokFOLLOWEDBY {
		p.advance()
		p.parseObservationExpressionOr()
	}
}

// observationExpressionOr : observationExpressionOr OR observationExpressionOr | observationExpressionAnd
func (p *parser) parseObservationExpressionOr() {
	p.parseObservationExpressionAnd()
	for p.cur().kind == tokOR {
		p.advance()
		p.parseObservationExpressionAnd()
	}
}

// observationExpressionAnd : observationExpressionAnd AND observationExpressionAnd | observationExpression
func (p *parser) parseObservationExpressionAnd() {
	p.parseObservationExpression()
	for p.cur().kind == tokAND {
		p.advance()
		p.parseObservationExpression()
	}
}

// observationExpression : LBRACK comparisonExpression RBRACK | LPAREN observationExpressions RPAREN | observationExpression qualifier
func (p *parser) parseObservationExpression() {
	switch p.cur().kind {
	case tokLBRACK:
		p.advance()
		p.parseComparisonExpression()
		if !p.want(tokRBRACK) {
			p.addError("expected ]")
		}
	case tokLPAREN:
		p.advance()
		p.parseObservationExpressions()
		if !p.want(tokRPAREN) {
			p.addError("expected )")
		}
	default:
		p.addError(fmt.Sprintf("expected [ or (, got %s", p.cur().kind))
		// Try to recover by advancing
		if p.cur().kind != tokEOF {
			p.advance()
		}
		return
	}
	// Qualifiers: startStop, within, repeated
	for {
		switch p.cur().kind {
		case tokSTART:
			p.advance()
			if !p.want(tokTimestamp) {
				p.addError("expected timestamp after START")
			}
			if !p.want(tokSTOP) {
				p.addError("expected STOP")
			}
			if !p.want(tokTimestamp) {
				p.addError("expected timestamp after STOP")
			}
		case tokWITHIN:
			p.advance()
			if p.cur().kind != tokIntPos && p.cur().kind != tokFloatPos {
				p.addError("expected number after WITHIN")
			} else {
				p.advance()
			}
			if !p.want(tokSECONDS) {
				p.addError("expected SECONDS")
			}
		case tokREPEATS:
			p.advance()
			if !p.want(tokIntPos) {
				p.addError("expected integer after REPEATS")
			}
			if !p.want(tokTIMES) {
				p.addError("expected TIMES")
			}
		default:
			return
		}
	}
}

// comparisonExpression : comparisonExpression OR comparisonExpression | comparisonExpressionAnd
func (p *parser) parseComparisonExpression() {
	p.parseComparisonExpressionAnd()
	for p.cur().kind == tokOR {
		p.advance()
		p.parseComparisonExpressionAnd()
	}
}

// comparisonExpressionAnd : comparisonExpressionAnd AND comparisonExpressionAnd | propTest
func (p *parser) parseComparisonExpressionAnd() {
	p.parsePropTest()
	for p.cur().kind == tokAND {
		p.advance()
		p.parsePropTest()
	}
}

// propTest : objectPath NOT? (EQ|NEQ) primitiveLiteral | ... | LPAREN comparisonExpression RPAREN | NOT? EXISTS objectPath
func (p *parser) parsePropTest() {
	switch p.cur().kind {
	case tokLPAREN:
		p.advance()
		p.parseComparisonExpression()
		if !p.want(tokRPAREN) {
			p.addError("expected )")
		}
		return
	case tokNOT:
		p.advance()
		if p.cur().kind == tokEXISTS {
			p.advance()
			objType, path := p.parseObjectPath()
			if objType != "" {
				p.paths = append(p.paths, pathRef{ObjectType: objType, Path: path})
			}
		} else {
			p.addError("expected EXISTS after NOT")
		}
		return
	case tokEXISTS:
		p.advance()
		objType, path := p.parseObjectPath()
		if objType != "" {
			p.paths = append(p.paths, pathRef{ObjectType: objType, Path: path})
		}
		return
	}

	// objectPath NOT? (EQ|NEQ|GT|LT|GE|LE|IN|LIKE|MATCHES|ISSUBSET|ISSUPERSET) literal
	objType, path := p.parseObjectPath()
	if objType != "" || path != "" {
		p.paths = append(p.paths, pathRef{ObjectType: objType, Path: path})
	}
	if p.cur().kind == tokNOT {
		p.advance()
	}
	switch p.cur().kind {
	case tokEQ, tokNEQ:
		p.advance()
		p.parsePrimitiveLiteral()
	case tokGT, tokLT, tokGE, tokLE:
		p.advance()
		p.parseOrderableLiteral()
	case tokIN:
		p.advance()
		p.parseSetLiteral()
	case tokLIKE, tokMATCHES, tokISSUBSET, tokISSUPERSET:
		p.advance()
		if p.cur().kind != tokString {
			p.addError("expected string literal")
		} else {
			p.advance()
		}
	default:
		if len(p.errors) == 0 {
			p.addError(fmt.Sprintf("expected comparison operator, got %s", p.cur().kind))
		}
		if p.cur().kind != tokEOF {
			p.advance()
		}
	}
}

// parseObjectPath returns (objectType, pathString). pathString is the full path as in the pattern (e.g. "name", "hashes.'SHA-256'", "values[*].data").
func (p *parser) parseObjectPath() (string, string) {
	// objectType : firstPathComponent objectPathComponent?
	objectType := p.parseObjectType()
	if objectType == "" {
		return "", ""
	}
	if !p.want(tokCOLON) {
		p.addError("expected : after object type")
		return "", ""
	}
	path := p.parseFirstPathComponent()
	if path == "" {
		return "", ""
	}
	path += p.parseObjectPathComponent()
	return objectType, path
}

func (p *parser) parseObjectType() string {
	switch p.cur().kind {
	case tokIdentNoHyphen, tokIdentWithHyphen:
		t := p.advance()
		return t.lit
	default:
		return ""
	}
}

// parseFirstPathComponent returns the path segment for the first component (identifier or string literal).
func (p *parser) parseFirstPathComponent() string {
	switch p.cur().kind {
	case tokIdentNoHyphen:
		t := p.advance()
		return t.lit
	case tokString:
		t := p.advance()
		return t.lit
	default:
		return ""
	}
}

// parseObjectPathComponent returns the rest of the path (e.g. .key, [*], [0], .'key').
func (p *parser) parseObjectPathComponent() string {
	var b strings.Builder
	for {
		switch p.cur().kind {
		case tokDOT:
			p.advance()
			switch p.cur().kind {
			case tokIdentNoHyphen:
				t := p.advance()
				b.WriteByte('.')
				b.WriteString(t.lit)
			case tokString:
				t := p.advance()
				b.WriteByte('.')
				b.WriteString(t.lit)
			default:
				return b.String()
			}
		case tokLBRACK:
			p.advance()
			switch p.cur().kind {
			case tokIntPos, tokIntNeg:
				t := p.advance()
				b.WriteByte('[')
				b.WriteString(t.lit)
				b.WriteByte(']')
			case tokASTERISK:
				p.advance()
				b.WriteString("[*]")
			default:
				p.addError("expected index or * in path")
				return b.String()
			}
			if !p.want(tokRBRACK) {
				p.addError("expected ]")
			}
		default:
			return b.String()
		}
	}
}

func (p *parser) parsePrimitiveLiteral() {
	switch p.cur().kind {
	case tokIntPos, tokIntNeg, tokFloatPos, tokFloatNeg, tokString, tokBinary, tokHex, tokTimestamp:
		p.advance()
	case tokTRUE, tokFALSE:
		p.advance()
	default:
		p.addError(fmt.Sprintf("expected primitive literal, got %s", p.cur().kind))
		if p.cur().kind != tokEOF {
			p.advance()
		}
	}
}

func (p *parser) parseOrderableLiteral() {
	switch p.cur().kind {
	case tokIntPos, tokIntNeg, tokFloatPos, tokFloatNeg, tokString, tokBinary, tokHex, tokTimestamp:
		p.advance()
	default:
		p.addError(fmt.Sprintf("expected orderable literal, got %s", p.cur().kind))
		if p.cur().kind != tokEOF {
			p.advance()
		}
	}
}

func (p *parser) parseSetLiteral() {
	if !p.want(tokLPAREN) {
		p.addError("expected ( for set literal")
		return
	}
	if p.cur().kind == tokRPAREN {
		p.advance()
		return
	}
	p.parsePrimitiveLiteral()
	for p.cur().kind == tokCOMMA {
		p.advance()
		p.parsePrimitiveLiteral()
	}
	if !p.want(tokRPAREN) {
		p.addError("expected ) to close set literal")
	}
}

// parse runs the parser on the token stream. Returns collected path refs and any parse errors.
func parse(stream *tokenStream, lexErr string) ([]pathRef, []string) {
	p := &parser{stream: stream}
	if lexErr != "" {
		p.errors = append(p.errors, lexErr)
		return nil, p.errors
	}
	p.parsePattern()
	if len(p.errors) > 0 {
		return nil, p.errors
	}
	return p.paths, nil
}
