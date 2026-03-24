// Package pattern validates STIX 2.1 pattern strings and inspects them
// to return (observable type, property path) pairs. Validation and inspection
// use a streaming lexer so that concurrent validation of many patterns uses
// less memory per call (no full token slice per pattern).
package pattern

// ValidatePattern checks the syntax of a STIX 2.1 pattern string.
// Valid pattern returns (nil, nil) or ([], nil). Invalid pattern returns
// (non-empty []string, nil) with one message per validation issue.
// A fatal error (e.g. internal bug) returns (nil, err).
// Validation uses a streaming lexer so concurrent validation of many patterns uses less memory per call.
func ValidatePattern(s string) ([]string, error) {
	l := newLexer(s)
	stream := newTokenStream(l)
	_, parseErrs := parse(stream, stream.Err())
	if len(parseErrs) > 0 {
		return parseErrs, nil
	}
	if stream.Err() != "" {
		return []string{stream.Err()}, nil
	}
	return nil, nil
}

// InspectPattern parses a valid STIX 2.1 pattern and returns a map from
// observable object type to the list of property path strings used in
// comparisons. Paths use the same form as in the pattern (e.g. "name",
// "hashes.'SHA-256'", "values[*].data"). When err == nil, the map is
// always non-nil (empty map when there are no comparisons). Invalid
// pattern returns (nil, err).
func InspectPattern(s string) (map[string][]string, error) {
	l := newLexer(s)
	stream := newTokenStream(l)
	if stream.Err() != "" {
		return nil, &invalidPatternError{msg: stream.Err()}
	}
	paths, parseErrs := parse(stream, "")
	if len(parseErrs) > 0 {
		return nil, &invalidPatternError{msg: parseErrs[0]}
	}
	if stream.Err() != "" {
		return nil, &invalidPatternError{msg: stream.Err()}
	}
	return buildInspectionMap(paths), nil
}

// invalidPatternError is returned by InspectPattern when the pattern is invalid.
type invalidPatternError struct{ msg string }

func (e *invalidPatternError) Error() string { return e.msg }
