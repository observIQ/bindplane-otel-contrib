package pattern

// tokenStream wraps a lexer and provides one-token lookahead for the parser.
// The parser pulls tokens via Cur() and Advance(); no full token slice is allocated.
// A single lexer/stream is not safe for concurrent use; multiple goroutines may
// each create their own lexer and stream (e.g. via ValidatePattern) for concurrent
// validation of different patterns with lower per-call memory.
type tokenStream struct {
	lexer *lexer
	cur   token
}

// newTokenStream primes the stream with the first token from the lexer.
func newTokenStream(l *lexer) *tokenStream {
	s := &tokenStream{lexer: l}
	s.cur = l.nextToken()
	return s
}

// Cur returns the current lookahead token without consuming it.
func (s *tokenStream) Cur() token {
	return s.cur
}

// Advance returns the current token and advances to the next.
// At EOF or after an invalid token, subsequent Advance calls return the same token.
func (s *tokenStream) Advance() token {
	prev := s.cur
	if prev.kind != tokEOF && prev.kind != tokInvalid {
		s.cur = s.lexer.nextToken()
	}
	return prev
}

// Err returns any lex error set on the underlying lexer.
func (s *tokenStream) Err() string {
	return s.lexer.err
}

// drainTokenStream collects all tokens from the stream until EOF or invalid.
// Used by lexAll and tests that need a full token slice.
func drainTokenStream(s *tokenStream) ([]token, string) {
	var toks []token
	for {
		t := s.Advance()
		toks = append(toks, t)
		if t.kind == tokEOF || t.kind == tokInvalid {
			break
		}
	}
	return toks, s.Err()
}
