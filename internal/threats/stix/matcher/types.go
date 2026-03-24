// Package matcher: types for pattern matching.

package matcher

// MatchOptions configures pattern matching.
type MatchOptions struct {
	// Verbose enables debug output (e.g. to stderr). Optional.
	Verbose bool
}

// DefaultMatchOptions returns options with defaults (STIX 2.1 only).
func DefaultMatchOptions() *MatchOptions {
	return &MatchOptions{}
}

// MatchResult is the result of matching a pattern against observed-data.
type MatchResult struct {
	// Matched is true if at least one binding was found.
	Matched bool
	// SDOs is the list of observed-data SDOs that matched (first binding).
	// Empty if !Matched.
	SDOs []map[string]interface{}
}

// ObservedData is a single observed-data SDO (map from JSON).
// Use map[string]interface{} for flexibility; the matcher expects
// "objects", "first_observed", "last_observed", "number_observed" (2.1)
// and optionally "object_refs" for bundle expansion.
type ObservedData = map[string]interface{}
