package pattern

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// astParser builds an AST from the same grammar as the path-only parser.
type astParser struct {
	stream *tokenStream
	errors []string
}

// ParseToAST parses the pattern string and returns the AST root.
// Returns (nil, err) on parse or lex error.
func ParseToAST(s string) (*Pattern, error) {
	l := newLexer(s)
	stream := newTokenStream(l)
	if stream.Err() != "" {
		return nil, &invalidPatternError{msg: stream.Err()}
	}
	p := &astParser{stream: stream}
	pat := p.parsePattern()
	if len(p.errors) > 0 {
		return nil, &invalidPatternError{msg: p.errors[0]}
	}
	if stream.Err() != "" {
		return nil, &invalidPatternError{msg: stream.Err()}
	}
	return pat, nil
}

func (p *astParser) cur() token     { return p.stream.Cur() }
func (p *astParser) advance() token { return p.stream.Advance() }
func (p *astParser) want(k tokenKind) bool {
	if p.cur().kind == k {
		p.advance()
		return true
	}
	return false
}
func (p *astParser) addError(msg string) { p.errors = append(p.errors, msg) }

func (p *astParser) parsePattern() *Pattern {
	obs := p.parseObservationExpressions()
	if p.cur().kind != tokEOF {
		p.addError(fmt.Sprintf("unexpected token %s at end", p.cur().kind))
	}
	if obs == nil {
		return nil
	}
	return &Pattern{ObservationExpressions: obs}
}

func (p *astParser) parseObservationExpressions() *ObservationExpressions {
	ors := []*ObservationExpressionOr{p.parseObservationExpressionOr()}
	for p.cur().kind == tokFOLLOWEDBY {
		p.advance()
		ors = append(ors, p.parseObservationExpressionOr())
	}
	return &ObservationExpressions{Ors: ors}
}

func (p *astParser) parseObservationExpressionOr() *ObservationExpressionOr {
	ands := []*ObservationExpressionAnd{p.parseObservationExpressionAnd()}
	for p.cur().kind == tokOR {
		p.advance()
		ands = append(ands, p.parseObservationExpressionAnd())
	}
	return &ObservationExpressionOr{Ands: ands}
}

func (p *astParser) parseObservationExpressionAnd() *ObservationExpressionAnd {
	exprs := []*ObservationExpression{p.parseObservationExpression()}
	for p.cur().kind == tokAND {
		p.advance()
		exprs = append(exprs, p.parseObservationExpression())
	}
	return &ObservationExpressionAnd{Exprs: exprs}
}

func (p *astParser) parseObservationExpression() *ObservationExpression {
	expr := &ObservationExpression{}
	switch p.cur().kind {
	case tokLBRACK:
		p.advance()
		expr.Comparison = p.parseComparisonExpression()
		if !p.want(tokRBRACK) {
			p.addError("expected ]")
		}
	case tokLPAREN:
		p.advance()
		expr.Nested = p.parseObservationExpressions()
		if !p.want(tokRPAREN) {
			p.addError("expected )")
		}
	default:
		p.addError(fmt.Sprintf("expected [ or (, got %s", p.cur().kind))
		if p.cur().kind != tokEOF {
			p.advance()
		}
		return nil
	}
	// Qualifiers
	for {
		switch p.cur().kind {
		case tokSTART:
			p.advance()
			startLit := p.parseTimestampLiteral()
			if !p.want(tokSTOP) {
				p.addError("expected STOP")
			}
			stopLit := p.parseTimestampLiteral()
			expr.StartStop = &StartStopQualifier{Start: startLit, Stop: stopLit}
		case tokWITHIN:
			p.advance()
			secLit := p.parseWithinSecondsLiteral()
			if !p.want(tokSECONDS) {
				p.addError("expected SECONDS")
			}
			expr.Within = &WithinQualifier{Seconds: secLit}
		case tokREPEATS:
			p.advance()
			n := p.parseIntPosLiteral()
			if !p.want(tokTIMES) {
				p.addError("expected TIMES")
			}
			expr.Repeats = &RepeatsQualifier{Times: n}
		default:
			return expr
		}
	}
}

func (p *astParser) parseTimestampLiteral() Literal {
	t := p.cur()
	if t.kind == tokTimestamp {
		p.advance()
		s := unquoteTimestamp(t.lit)
		return Literal{Timestamp: &s}
	}
	return Literal{}
}

func (p *astParser) parseWithinSecondsLiteral() Literal {
	if p.cur().kind == tokFloatPos || p.cur().kind == tokIntPos {
		t := p.advance()
		if f, err := strconv.ParseFloat(t.lit, 64); err == nil {
			return Literal{Float: &f}
		}
		if i, err := strconv.ParseInt(t.lit, 10, 64); err == nil {
			f := float64(i)
			return Literal{Float: &f}
		}
	}
	return Literal{}
}

func (p *astParser) parseIntPosLiteral() int {
	if p.cur().kind == tokIntPos {
		t := p.advance()
		n, _ := strconv.Atoi(t.lit)
		return n
	}
	return 0
}

func (p *astParser) parseComparisonExpression() *ComparisonExpression {
	ands := []*ComparisonExpressionAnd{p.parseComparisonExpressionAnd()}
	for p.cur().kind == tokOR {
		p.advance()
		ands = append(ands, p.parseComparisonExpressionAnd())
	}
	return &ComparisonExpression{Ands: ands}
}

func (p *astParser) parseComparisonExpressionAnd() *ComparisonExpressionAnd {
	tests := []PropTest{p.parsePropTest()}
	for p.cur().kind == tokAND {
		p.advance()
		tests = append(tests, p.parsePropTest())
	}
	return &ComparisonExpressionAnd{PropTests: tests}
}

func (p *astParser) parsePropTest() PropTest {
	switch p.cur().kind {
	case tokLPAREN:
		p.advance()
		c := p.parseComparisonExpression()
		if !p.want(tokRPAREN) {
			p.addError("expected )")
		}
		return &PropTestParens{Comparison: c}
	case tokNOT:
		p.advance()
		if p.cur().kind == tokEXISTS {
			p.advance()
			op := p.parseObjectPath()
			return &PropTestExists{Not: true, ObjectPath: op}
		}
		p.addError("expected EXISTS after NOT")
		return nil
	case tokEXISTS:
		p.advance()
		op := p.parseObjectPath()
		return &PropTestExists{Not: false, ObjectPath: op}
	}
	op := p.parseObjectPath()
	not := p.want(tokNOT)
	switch p.cur().kind {
	case tokEQ:
		p.advance()
		return &PropTestEqual{ObjectPath: op, Not: not, Op: "=", Literal: p.parsePrimitiveLiteral()}
	case tokNEQ:
		p.advance()
		return &PropTestEqual{ObjectPath: op, Not: not, Op: "!=", Literal: p.parsePrimitiveLiteral()}
	case tokGT:
		p.advance()
		return &PropTestOrder{ObjectPath: op, Not: not, Op: ">", Literal: p.parseOrderableLiteral()}
	case tokLT:
		p.advance()
		return &PropTestOrder{ObjectPath: op, Not: not, Op: "<", Literal: p.parseOrderableLiteral()}
	case tokGE:
		p.advance()
		return &PropTestOrder{ObjectPath: op, Not: not, Op: ">=", Literal: p.parseOrderableLiteral()}
	case tokLE:
		p.advance()
		return &PropTestOrder{ObjectPath: op, Not: not, Op: "<=", Literal: p.parseOrderableLiteral()}
	case tokIN:
		p.advance()
		return &PropTestSet{ObjectPath: op, Not: not, Set: p.parseSetLiteral()}
	case tokLIKE:
		p.advance()
		s := p.parseStringLiteral()
		return &PropTestLike{ObjectPath: op, Not: not, Pattern: s}
	case tokMATCHES:
		p.advance()
		s := p.parseStringLiteral()
		return &PropTestRegex{ObjectPath: op, Not: not, Regex: s}
	case tokISSUBSET:
		p.advance()
		s := p.parseStringLiteral()
		return &PropTestIsSubset{ObjectPath: op, Not: not, Value: s}
	case tokISSUPERSET:
		p.advance()
		s := p.parseStringLiteral()
		return &PropTestIsSuperset{ObjectPath: op, Not: not, Value: s}
	default:
		if len(p.errors) == 0 {
			p.addError(fmt.Sprintf("expected comparison operator, got %s", p.cur().kind))
		}
		if p.cur().kind != tokEOF {
			p.advance()
		}
		return nil
	}
}

func (p *astParser) parseObjectPath() *ObjectPath {
	objectType := p.parseObjectType()
	if objectType == "" {
		return nil
	}
	if !p.want(tokCOLON) {
		p.addError("expected : after object type")
		return nil
	}
	path := p.parsePath()
	return &ObjectPath{ObjectType: objectType, Path: path}
}

func (p *astParser) parseObjectType() string {
	switch p.cur().kind {
	case tokIdentNoHyphen, tokIdentWithHyphen:
		t := p.advance()
		return t.lit
	}
	return ""
}

// parsePath returns the full path as a slice of steps (first component + rest).
func (p *astParser) parsePath() []PathStep {
	first := p.parseFirstPathComponent()
	if first == "" {
		return nil
	}
	steps := []PathStep{{Key: first}}
	for {
		switch p.cur().kind {
		case tokDOT:
			p.advance()
			k := p.parseKeyComponent()
			if k == "" {
				return steps
			}
			steps = append(steps, PathStep{Key: k})
		case tokLBRACK:
			p.advance()
			switch p.cur().kind {
			case tokIntPos, tokIntNeg:
				t := p.advance()
				n, _ := strconv.Atoi(t.lit)
				steps = append(steps, PathStep{Index: &n})
			case tokASTERISK:
				p.advance()
				steps = append(steps, PathStep{Star: true})
			default:
				p.addError("expected index or * in path")
				return steps
			}
			if !p.want(tokRBRACK) {
				p.addError("expected ]")
			}
		default:
			return steps
		}
	}
}

func (p *astParser) parseFirstPathComponent() string {
	switch p.cur().kind {
	case tokIdentNoHyphen:
		t := p.advance()
		return t.lit
	case tokString:
		t := p.advance()
		return unquoteString(t.lit)
	}
	return ""
}

func (p *astParser) parseKeyComponent() string {
	switch p.cur().kind {
	case tokIdentNoHyphen:
		t := p.advance()
		return t.lit
	case tokString:
		t := p.advance()
		return unquoteString(t.lit)
	}
	return ""
}

func (p *astParser) parseStringLiteral() string {
	if p.cur().kind == tokString {
		t := p.advance()
		return unquoteString(t.lit)
	}
	return ""
}

func (p *astParser) parsePrimitiveLiteral() Literal {
	switch p.cur().kind {
	case tokIntPos:
		t := p.advance()
		n, _ := strconv.ParseInt(t.lit, 10, 64)
		return Literal{Int: &n}
	case tokIntNeg:
		t := p.advance()
		n, _ := strconv.ParseInt(t.lit, 10, 64)
		return Literal{Int: &n}
	case tokFloatPos:
		t := p.advance()
		f, _ := strconv.ParseFloat(t.lit, 64)
		return Literal{Float: &f}
	case tokFloatNeg:
		t := p.advance()
		f, _ := strconv.ParseFloat(t.lit, 64)
		return Literal{Float: &f}
	case tokString:
		t := p.advance()
		s := unquoteString(t.lit)
		return Literal{Str: &s}
	case tokBinary:
		t := p.advance()
		b := unquoteBinary(t.lit)
		return Literal{Binary: b}
	case tokHex:
		t := p.advance()
		b := unquoteHex(t.lit)
		return Literal{Hex: b}
	case tokTimestamp:
		t := p.advance()
		s := unquoteTimestamp(t.lit)
		return Literal{Timestamp: &s}
	case tokTRUE:
		p.advance()
		t := true
		return Literal{Bool: &t}
	case tokFALSE:
		p.advance()
		f := false
		return Literal{Bool: &f}
	}
	return Literal{}
}

func (p *astParser) parseOrderableLiteral() Literal {
	// Same as primitive but no bool
	switch p.cur().kind {
	case tokIntPos, tokIntNeg, tokFloatPos, tokFloatNeg, tokString, tokBinary, tokHex, tokTimestamp:
		return p.parsePrimitiveLiteral()
	}
	return Literal{}
}

func (p *astParser) parseSetLiteral() *SetLiteral {
	if !p.want(tokLPAREN) {
		p.addError("expected ( for set literal")
		return nil
	}
	if p.cur().kind == tokRPAREN {
		p.advance()
		return &SetLiteral{Values: nil}
	}
	vals := []Literal{p.parsePrimitiveLiteral()}
	for p.cur().kind == tokCOMMA {
		p.advance()
		vals = append(vals, p.parsePrimitiveLiteral())
	}
	if !p.want(tokRPAREN) {
		p.addError("expected ) to close set literal")
	}
	return &SetLiteral{Values: vals}
}

func unquoteString(lit string) string {
	if len(lit) < 2 || lit[0] != '\'' || lit[len(lit)-1] != '\'' {
		return lit
	}
	lit = lit[1 : len(lit)-1]
	return strings.ReplaceAll(strings.ReplaceAll(lit, "\\'", "'"), "\\\\", "\\")
}

func unquoteTimestamp(lit string) string {
	if len(lit) >= 3 && lit[0] == 't' && lit[1] == '\'' && lit[len(lit)-1] == '\'' {
		return lit[2 : len(lit)-1]
	}
	return lit
}

func unquoteBinary(lit string) []byte {
	if len(lit) < 3 || lit[0] != 'b' || lit[1] != '\'' || lit[len(lit)-1] != '\'' {
		return nil
	}
	b64 := lit[2 : len(lit)-1]
	out, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	return out
}

func unquoteHex(lit string) []byte {
	if len(lit) < 3 || lit[0] != 'h' || lit[1] != '\'' || lit[len(lit)-1] != '\'' {
		return nil
	}
	hexStr := lit[2 : len(lit)-1]
	out := make([]byte, len(hexStr)/2)
	for i := 0; i+1 < len(hexStr); i += 2 {
		b, err := strconv.ParseUint(hexStr[i:i+2], 16, 8)
		if err != nil {
			return nil
		}
		out[i/2] = byte(b)
	}
	return out
}
