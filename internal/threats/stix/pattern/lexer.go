package pattern

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// Token kind for the STIX pattern grammar.
type tokenKind int

const (
	_ tokenKind = iota
	tokEOF
	tokInvalid
	// Keywords (must match before identifiers)
	tokAND
	tokOR
	tokNOT
	tokFOLLOWEDBY
	tokLIKE
	tokMATCHES
	tokISSUPERSET
	tokISSUBSET
	tokEXISTS
	tokLAST
	tokIN
	tokSTART
	tokSTOP
	tokSECONDS
	tokTRUE
	tokFALSE
	tokWITHIN
	tokREPEATS
	tokTIMES
	// Literals
	tokIntPos
	tokIntNeg
	tokFloatPos
	tokFloatNeg
	tokString
	tokBinary
	tokHex
	tokTimestamp
	tokBool // true/false already consumed as keyword
	// Identifiers
	tokIdentNoHyphen   // [a-zA-Z_][a-zA-Z0-9_]*
	tokIdentWithHyphen // [a-zA-Z_][a-zA-Z0-9_-]*
	// Operators
	tokEQ  // = or ==
	tokNEQ // != or <>
	tokLT
	tokLE
	tokGT
	tokGE
	// Punctuation
	tokLBRACK
	tokRBRACK
	tokLPAREN
	tokRPAREN
	tokCOLON
	tokDOT
	tokCOMMA
	tokQUOTE
	tokPLUS
	tokMINUS
	tokASTERISK
	tokPOWER
	tokDIVIDE
)

func (k tokenKind) String() string {
	switch k {
	case tokEOF:
		return "EOF"
	case tokInvalid:
		return "Invalid"
	case tokAND:
		return "AND"
	case tokOR:
		return "OR"
	case tokNOT:
		return "NOT"
	case tokFOLLOWEDBY:
		return "FOLLOWEDBY"
	case tokLIKE:
		return "LIKE"
	case tokMATCHES:
		return "MATCHES"
	case tokISSUPERSET:
		return "ISSUPERSET"
	case tokISSUBSET:
		return "ISSUBSET"
	case tokEXISTS:
		return "EXISTS"
	case tokLAST:
		return "LAST"
	case tokIN:
		return "IN"
	case tokSTART:
		return "START"
	case tokSTOP:
		return "STOP"
	case tokSECONDS:
		return "SECONDS"
	case tokTRUE:
		return "TRUE"
	case tokFALSE:
		return "FALSE"
	case tokWITHIN:
		return "WITHIN"
	case tokREPEATS:
		return "REPEATS"
	case tokTIMES:
		return "TIMES"
	case tokIntPos:
		return "IntPosLiteral"
	case tokIntNeg:
		return "IntNegLiteral"
	case tokFloatPos:
		return "FloatPosLiteral"
	case tokFloatNeg:
		return "FloatNegLiteral"
	case tokString:
		return "StringLiteral"
	case tokBinary:
		return "BinaryLiteral"
	case tokHex:
		return "HexLiteral"
	case tokTimestamp:
		return "TimestampLiteral"
	case tokIdentNoHyphen:
		return "IdentifierWithoutHyphen"
	case tokIdentWithHyphen:
		return "IdentifierWithHyphen"
	case tokEQ:
		return "EQ"
	case tokNEQ:
		return "NEQ"
	case tokLT:
		return "LT"
	case tokLE:
		return "LE"
	case tokGT:
		return "GT"
	case tokGE:
		return "GE"
	case tokLBRACK:
		return "LBRACK"
	case tokRBRACK:
		return "RBRACK"
	case tokLPAREN:
		return "LPAREN"
	case tokRPAREN:
		return "RPAREN"
	case tokCOLON:
		return "COLON"
	case tokDOT:
		return "DOT"
	case tokCOMMA:
		return "COMMA"
	case tokQUOTE:
		return "QUOTE"
	case tokPLUS:
		return "PLUS"
	case tokMINUS:
		return "MINUS"
	case tokASTERISK:
		return "ASTERISK"
	case tokPOWER:
		return "POWER"
	case tokDIVIDE:
		return "DIVIDE"
	default:
		return fmt.Sprintf("tokenKind(%d)", k)
	}
}

// token holds a lexed token with kind and literal text.
type token struct {
	kind tokenKind
	lit  string
	pos  int // byte offset in input
}

// lexer tokenizes a STIX pattern string.
type lexer struct {
	input string
	pos   int
	start int
	err   string
}

func newLexer(input string) *lexer {
	return &lexer{input: input}
}

func (l *lexer) eof() bool {
	return l.pos >= len(l.input)
}

func (l *lexer) peek() rune {
	if l.pos >= len(l.input) {
		return -1
	}
	r, _ := utf8.DecodeRuneInString(l.input[l.pos:])
	return r
}

func (l *lexer) next() rune {
	if l.pos >= len(l.input) {
		return -1
	}
	r, w := utf8.DecodeRuneInString(l.input[l.pos:])
	l.pos += w
	return r
}

func (l *lexer) lit() string {
	return l.input[l.start:l.pos]
}

func (l *lexer) setError(msg string) {
	if l.err == "" {
		l.err = msg
	}
}

// skip whitespace and comments (grammar: WS, COMMENT, LINE_COMMENT -> skip)
func (l *lexer) skipSkip() {
	for {
		if l.eof() {
			return
		}
		r := l.peek()
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' || unicode.IsSpace(r) {
			l.next()
			continue
		}
		if r == '/' {
			rest := l.input[l.pos:]
			if len(rest) >= 2 && rest[1] == '*' {
				// block comment /* ... */
				l.pos += 2
				for l.pos+1 < len(l.input) && l.input[l.pos:l.pos+2] != "*/" {
					l.pos++
				}
				if l.pos+2 <= len(l.input) {
					l.pos += 2
				}
				continue
			}
			if len(rest) >= 2 && rest[1] == '/' {
				// line comment // ...
				l.pos += 2
				for l.pos < len(l.input) && l.input[l.pos] != '\n' && l.input[l.pos] != '\r' {
					l.pos++
				}
				continue
			}
		}
		break
	}
}

// nextToken returns the next token. Sets l.err and returns tokInvalid on error.
func (l *lexer) nextToken() token {
	l.skipSkip()
	l.start = l.pos
	if l.eof() {
		return token{kind: tokEOF, pos: l.start}
	}

	r := l.next()
	pos := l.start

	// Single-rune tokens (no quote here - quote starts string literal)
	switch r {
	case ':':
		return token{kind: tokCOLON, lit: ":", pos: pos}
	case '.':
		return token{kind: tokDOT, lit: ".", pos: pos}
	case ',':
		return token{kind: tokCOMMA, lit: ",", pos: pos}
	case '(':
		return token{kind: tokLPAREN, lit: "(", pos: pos}
	case ')':
		return token{kind: tokRPAREN, lit: ")", pos: pos}
	case '[':
		return token{kind: tokLBRACK, lit: "[", pos: pos}
	case ']':
		return token{kind: tokRBRACK, lit: "]", pos: pos}
	case '^':
		return token{kind: tokPOWER, lit: "^", pos: pos}
	case '/':
		return token{kind: tokDIVIDE, lit: "/", pos: pos}
	case '*':
		return token{kind: tokASTERISK, lit: "*", pos: pos}
	case -1:
		return token{kind: tokEOF, pos: pos}
	case '\'':
		return l.lexString()
	}

	// Two-rune or keyword/identifier
	switch r {
	case '=':
		if l.peek() == '=' {
			l.next()
			return token{kind: tokEQ, lit: "==", pos: pos}
		}
		return token{kind: tokEQ, lit: "=", pos: pos}
	case '!':
		if l.peek() == '=' {
			l.next()
			return token{kind: tokNEQ, lit: "!=", pos: pos}
		}
		l.setError("unexpected '!'")
		return token{kind: tokInvalid, lit: l.lit(), pos: pos}
	case '<':
		if l.peek() == '>' {
			l.next()
			return token{kind: tokNEQ, lit: "<>", pos: pos}
		}
		if l.peek() == '=' {
			l.next()
			return token{kind: tokLE, lit: "<=", pos: pos}
		}
		return token{kind: tokLT, lit: "<", pos: pos}
	case '>':
		if l.peek() == '=' {
			l.next()
			return token{kind: tokGE, lit: ">=", pos: pos}
		}
		return token{kind: tokGT, lit: ">", pos: pos}
	case '+':
		// IntPosLiteral can start with +, or standalone PLUS
		return l.lexIntOrFloatOrPlus()
	case '-':
		return l.lexIntNegOrFloatNegOrMinus()
	case 't':
		// true or timestamp t'...' or identifier
		if l.eof() {
			return token{kind: tokIdentNoHyphen, lit: "t", pos: pos}
		}
		rest := l.input[l.pos:]
		if len(rest) >= 4 && rest[:4] == "rue " || (len(rest) == 4 && rest == "rue") || (len(rest) > 4 && rest[:4] == "rue" && !isIdentChar(rune(rest[4]))) {
			l.pos += 4
			return token{kind: tokTRUE, lit: "true", pos: pos}
		}
		if len(rest) >= 1 && rest[0] == '\'' {
			return l.lexTimestamp()
		}
		return l.lexIdentifier()
	case 'f':
		if len(l.input) >= l.pos+4 && l.input[l.pos:l.pos+4] == "alse" &&
			(l.pos+4 >= len(l.input) || !isIdentChar(rune(l.input[l.pos+4]))) {
			l.pos += 4
			return token{kind: tokFALSE, lit: "false", pos: pos}
		}
		return l.lexIdentifier()
	case 'h':
		if !l.eof() && l.peek() == '\'' {
			return l.lexHex()
		}
		return l.lexIdentifier()
	case 'b':
		if !l.eof() && l.peek() == '\'' {
			return l.lexBinary()
		}
		return l.lexIdentifier()
	case '\'':
		// StringLiteral: already consumed ', need content and closing '
		return l.lexString()
	default:
		if r >= '0' && r <= '9' {
			return l.lexIntOrFloat()
		}
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return l.lexKeywordOrIdent()
		}
		l.setError(fmt.Sprintf("invalid character %q", r))
		return token{kind: tokInvalid, lit: string(r), pos: pos}
	}
}

func isIdentChar(r rune) bool {
	return r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func (l *lexer) lexKeywordOrIdent() token {
	pos := l.start
	for !l.eof() {
		r := l.peek()
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			l.next()
			continue
		}
		break
	}
	lit := l.lit()
	// Keywords (longest first)
	switch lit {
	case "AND":
		return token{kind: tokAND, lit: lit, pos: pos}
	case "OR":
		return token{kind: tokOR, lit: lit, pos: pos}
	case "NOT":
		return token{kind: tokNOT, lit: lit, pos: pos}
	case "FOLLOWEDBY":
		return token{kind: tokFOLLOWEDBY, lit: lit, pos: pos}
	case "LIKE":
		return token{kind: tokLIKE, lit: lit, pos: pos}
	case "MATCHES":
		return token{kind: tokMATCHES, lit: lit, pos: pos}
	case "ISSUPERSET":
		return token{kind: tokISSUPERSET, lit: lit, pos: pos}
	case "ISSUBSET":
		return token{kind: tokISSUBSET, lit: lit, pos: pos}
	case "EXISTS":
		return token{kind: tokEXISTS, lit: lit, pos: pos}
	case "LAST":
		return token{kind: tokLAST, lit: lit, pos: pos}
	case "IN":
		return token{kind: tokIN, lit: lit, pos: pos}
	case "START":
		return token{kind: tokSTART, lit: lit, pos: pos}
	case "STOP":
		return token{kind: tokSTOP, lit: lit, pos: pos}
	case "SECONDS":
		return token{kind: tokSECONDS, lit: lit, pos: pos}
	case "WITHIN":
		return token{kind: tokWITHIN, lit: lit, pos: pos}
	case "REPEATS":
		return token{kind: tokREPEATS, lit: lit, pos: pos}
	case "TIMES":
		return token{kind: tokTIMES, lit: lit, pos: pos}
	}
	// Identifier with or without hyphen
	for _, r := range lit {
		if r == '-' {
			return token{kind: tokIdentWithHyphen, lit: lit, pos: pos}
		}
	}
	return token{kind: tokIdentNoHyphen, lit: lit, pos: pos}
}

func (l *lexer) lexIdentifier() token {
	pos := l.start
	hasHyphen := false
	for !l.eof() {
		r := l.peek()
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if r == '-' {
				hasHyphen = true
			}
			l.next()
			continue
		}
		break
	}
	lit := l.lit()
	if hasHyphen {
		return token{kind: tokIdentWithHyphen, lit: lit, pos: pos}
	}
	return token{kind: tokIdentNoHyphen, lit: lit, pos: pos}
}

// lexString parses StringLiteral: QUOTE ( ~['\\] | '\\” | '\\\\' )* QUOTE
// We already consumed the opening QUOTE; l.start points to it.
func (l *lexer) lexString() token {
	pos := l.start
	for {
		if l.eof() {
			l.setError("unterminated string literal")
			return token{kind: tokInvalid, lit: l.lit(), pos: pos}
		}
		r := l.next()
		if r == '\\' {
			if l.eof() {
				l.setError("unterminated string literal")
				return token{kind: tokInvalid, lit: l.lit(), pos: pos}
			}
			n := l.next()
			if n != '\\' && n != '\'' {
				l.setError("invalid escape in string")
				return token{kind: tokInvalid, lit: l.lit(), pos: pos}
			}
			continue
		}
		if r == '\'' {
			return token{kind: tokString, lit: l.lit(), pos: pos}
		}
	}
}

// lexTimestamp parses t'YYYY-MM-DDTHH:MM:SS(.s)?Z'
// Caller consumed 't'; next char should be '.
func (l *lexer) lexTimestamp() token {
	pos := l.start
	if l.next() != '\'' {
		l.setError("expected ' after t for timestamp")
		return token{kind: tokInvalid, lit: l.lit(), pos: pos}
	}
	// [0-9][0-9][0-9][0-9] - [0-9] [0-9] - [0-9] [0-9] T ...
	const want = "YYYY-MM-DDTHH:MM:SS"
	for l.pos < len(l.input) {
		r := l.next()
		if r == '\'' {
			lit := l.lit()
			if len(lit) >= 21 && lit[len(lit)-1] == '\'' {
				return token{kind: tokTimestamp, lit: lit, pos: pos}
			}
			l.setError("invalid timestamp literal")
			return token{kind: tokInvalid, lit: lit, pos: pos}
		}
	}
	l.setError("unterminated timestamp literal")
	return token{kind: tokInvalid, lit: l.lit(), pos: pos}
}

// lexHex parses h'[A-Fa-f0-9]*'
func (l *lexer) lexHex() token {
	pos := l.start
	l.next() // consume '
	for !l.eof() {
		r := l.peek()
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'F') || (r >= 'a' && r <= 'f') {
			l.next()
			continue
		}
		if r == '\'' {
			l.next()
			return token{kind: tokHex, lit: l.lit(), pos: pos}
		}
		l.setError("invalid hex literal")
		return token{kind: tokInvalid, lit: l.lit(), pos: pos}
	}
	l.setError("unterminated hex literal")
	return token{kind: tokInvalid, lit: l.lit(), pos: pos}
}

// lexBinary parses b' base64* '
func (l *lexer) lexBinary() token {
	pos := l.start
	l.next() // consume '
	for !l.eof() {
		r := l.peek()
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' {
			l.next()
			continue
		}
		if r == '\'' {
			l.next()
			return token{kind: tokBinary, lit: l.lit(), pos: pos}
		}
		l.setError("invalid binary literal")
		return token{kind: tokInvalid, lit: l.lit(), pos: pos}
	}
	l.setError("unterminated binary literal")
	return token{kind: tokInvalid, lit: l.lit(), pos: pos}
}

func (l *lexer) lexIntOrFloatOrPlus() token {
	pos := l.start
	if l.eof() {
		return token{kind: tokPLUS, lit: "+", pos: pos}
	}
	r := l.peek()
	if r >= '0' && r <= '9' {
		return l.lexIntOrFloat()
	}
	return token{kind: tokPLUS, lit: "+", pos: pos}
}

func (l *lexer) lexIntNegOrFloatNegOrMinus() token {
	pos := l.start
	if l.eof() {
		return token{kind: tokMINUS, lit: "-", pos: pos}
	}
	if l.peek() >= '0' && l.peek() <= '9' {
		for l.peek() >= '0' && l.peek() <= '9' {
			l.next()
		}
		if l.peek() == '.' {
			l.next()
			for l.peek() >= '0' && l.peek() <= '9' {
				l.next()
			}
			return token{kind: tokFloatNeg, lit: l.lit(), pos: pos}
		}
		return token{kind: tokIntNeg, lit: l.lit(), pos: pos}
	}
	return token{kind: tokMINUS, lit: "-", pos: pos}
}

func (l *lexer) lexIntOrFloat() token {
	pos := l.start
	for l.peek() >= '0' && l.peek() <= '9' {
		l.next()
	}
	if l.peek() == '.' {
		l.next()
		for l.peek() >= '0' && l.peek() <= '9' {
			l.next()
		}
		return token{kind: tokFloatPos, lit: l.lit(), pos: pos}
	}
	return token{kind: tokIntPos, lit: l.lit(), pos: pos}
}

// lexAll tokenizes the entire input and returns tokens and any lex error.
// It uses the streaming lexer plus a drain so tests can still assert on full token sequences.
func lexAll(input string) ([]token, string) {
	l := newLexer(input)
	s := newTokenStream(l)
	return drainTokenStream(s)
}
