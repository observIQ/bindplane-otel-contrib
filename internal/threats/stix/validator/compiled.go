// This file defines compiled schema types for O(1) or O(depth) constraint lookup per instance path.

package validator

import "regexp"

// Constraints holds the validation constraints for a single JSON instance path
// (e.g. type, enum, pattern, required keys, min/max). Nil or zero values mean no constraint.
type Constraints struct {
	// Type is the JSON Schema type: "string", "number", "integer", "boolean", "object", "array".
	Type string

	// Enum restricts values to this set. Non-nil means "must be one of these".
	Enum []interface{}

	// Pattern is a regex pattern for strings (JSON Schema format).
	Pattern string
	// PatternRE is the compiled regex for Pattern. When non-nil, interpreter uses it instead of compiling Pattern each time.
	PatternRE *regexp.Regexp

	// Required lists property keys that must be present when this path is an object.
	Required []string

	// MinLength applies to strings.
	MinLength *int

	// MaxLength applies to strings.
	MaxLength *int

	// Minimum applies to numbers/integers.
	Minimum *float64

	// Maximum applies to numbers/integers.
	Maximum *float64

	// Items applies when Type is "array"; constraints for each element.
	Items *Constraints

	// MinItems applies to arrays; minimum number of elements. Nil = not specified.
	MinItems *int

	// AdditionalProperties when non-nil constrains extra object keys (true = allow any, false = disallow).
	// When nil, not specified in schema.
	AdditionalProperties *bool

	// PatternProperties maps regex pattern strings to constraints for matching object keys.
	// Used when the schema has patternProperties.
	PatternProperties map[string]*Constraints

	// OneOf: value must match exactly one of these subschemas. Nil = no oneOf.
	OneOf []*Constraints
	// AnyOf: value must match at least one of these. Nil = no anyOf.
	AnyOf []*Constraints
	// Not: value must not match this subschema. Nil = no not.
	Not *Constraints
}

// CompiledSchema is the result of compiling a root JSON Schema: resolve $ref, merge allOf,
// and produce a path → constraints map for O(1) lookup during validation.
type CompiledSchema struct {
	// PathConstraints maps instance path (e.g. "pattern", "external_references.0.url") to constraints.
	PathConstraints map[string]*Constraints
}
