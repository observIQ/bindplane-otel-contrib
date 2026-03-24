package matcher

import (
	stixpat "github.com/observiq/bindplane-otel-collector/internal/threats/stix/pattern"
)

// Match matches a STIX 2.1 pattern string against observed-data and returns the first binding's SDOs.
// observedData can be a list of observed-data SDOs or bundles (with "objects"); they are normalized
// for STIX 2.1 (object_refs expanded). Returns (nil, err) on parse error.
func Match(pattern string, observedData []map[string]interface{}, opts *MatchOptions) (*MatchResult, error) {
	if opts == nil {
		opts = DefaultMatchOptions()
	}
	pat, err := stixpat.ParseToAST(pattern)
	if err != nil {
		return nil, err
	}
	observations, err := normalizeObservedDataSTIX21(observedData)
	if err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return &MatchResult{Matched: false, SDOs: nil}, nil
	}
	matched, firstBinding := eval(pat, observations)
	if !matched {
		return &MatchResult{Matched: false, SDOs: nil}, nil
	}
	sdos := make([]map[string]interface{}, 0, len(firstBinding))
	seen := make(map[int]bool)
	for _, obsIdx := range firstBinding {
		if obsIdx < 0 || obsIdx >= len(observations) || seen[obsIdx] {
			continue
		}
		seen[obsIdx] = true
		sdos = append(sdos, observations[obsIdx].sdo)
	}
	return &MatchResult{Matched: true, SDOs: sdos}, nil
}

// CompiledPattern is a parsed pattern ready for repeated matching.
type CompiledPattern struct {
	ast *stixpat.Pattern
}

// Compile parses the pattern and returns a CompiledPattern for repeated Match calls.
func Compile(pattern string) (*CompiledPattern, error) {
	pat, err := stixpat.ParseToAST(pattern)
	if err != nil {
		return nil, err
	}
	return &CompiledPattern{ast: pat}, nil
}

// Match matches the compiled pattern against observed-data.
func (c *CompiledPattern) Match(observedData []map[string]interface{}, opts *MatchOptions) (*MatchResult, error) {
	if opts == nil {
		opts = DefaultMatchOptions()
	}
	observations, err := normalizeObservedDataSTIX21(observedData)
	if err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return &MatchResult{Matched: false, SDOs: nil}, nil
	}
	matched, firstBinding := eval(c.ast, observations)
	if !matched {
		return &MatchResult{Matched: false, SDOs: nil}, nil
	}
	sdos := make([]map[string]interface{}, 0, len(firstBinding))
	seen := make(map[int]bool)
	for _, obsIdx := range firstBinding {
		if obsIdx < 0 || obsIdx >= len(observations) || seen[obsIdx] {
			continue
		}
		seen[obsIdx] = true
		sdos = append(sdos, observations[obsIdx].sdo)
	}
	return &MatchResult{Matched: true, SDOs: sdos}, nil
}
