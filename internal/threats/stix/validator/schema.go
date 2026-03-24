// typed JSON Schema representation and conversion from raw maps.

package validator

import (
	codegen "github.com/observiq/bindplane-otel-collector/internal/threats/generate/schemacodegen"
)

// RawSchema is the untyped JSON Schema representation (map[string]interface{}).
// Used in generated code; ConvertSchemas turns it into map[string]*Schema.
type RawSchema = codegen.Schema

// Schema is a typed JSON Schema node used by the compiler and loader.
// It can be unmarshaled from schema JSON or produced by ConvertSchemas.
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Pattern              string             `json:"pattern,omitempty"`
	Enum                 []interface{}      `json:"enum,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	AllOf                []*Schema          `json:"allOf,omitempty"`
	OneOf                []*Schema          `json:"oneOf,omitempty"`
	AnyOf                []*Schema          `json:"anyOf,omitempty"`
	Not                  *Schema            `json:"not,omitempty"`
	MinLength            *float64           `json:"minLength,omitempty"`
	MaxLength            *float64           `json:"maxLength,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	PatternProperties    map[string]*Schema `json:"patternProperties,omitempty"`
}

// ConvertSchemas recursively converts a map of raw schemas to map[string]*Schema.
func ConvertSchemas(raw map[string]RawSchema) map[string]*Schema {
	out := make(map[string]*Schema, len(raw))
	for path, r := range raw {
		out[path] = convertOne(r)
	}
	return out
}

func convertOne(raw RawSchema) *Schema {
	if raw == nil {
		return nil
	}
	s := &Schema{}

	if v := codegen.GetString(raw, "$ref"); v != "" {
		s.Ref = v
	}
	if v := codegen.GetType(raw); v != "" {
		s.Type = v
	}
	if v := codegen.GetString(raw, "pattern"); v != "" {
		s.Pattern = v
	}
	if e := codegen.GetArray(raw, "enum"); len(e) > 0 {
		s.Enum = e
	}
	var required []string
	for _, r := range codegen.GetArray(raw, "required") {
		if str, ok := r.(string); ok {
			required = append(required, str)
		}
	}
	if len(required) > 0 {
		s.Required = required
	}
	if m := codegen.GetMap(raw, "properties"); len(m) > 0 {
		s.Properties = make(map[string]*Schema, len(m))
		for k, v := range m {
			if vm, ok := v.(map[string]interface{}); ok {
				s.Properties[k] = convertOne(RawSchema(vm))
			}
		}
	}
	if items := codegen.GetMap(raw, "items"); items != nil {
		s.Items = convertOne(RawSchema(items))
	}
	if n := toIntPtr(raw["minItems"]); n != nil {
		s.MinItems = n
	}
	for _, item := range codegen.GetArray(raw, "allOf") {
		if vm, ok := item.(map[string]interface{}); ok {
			s.AllOf = append(s.AllOf, convertOne(RawSchema(vm)))
		}
	}
	for _, el := range codegen.GetArray(raw, "oneOf") {
		if vm, ok := el.(map[string]interface{}); ok {
			s.OneOf = append(s.OneOf, convertOne(RawSchema(vm)))
		}
	}
	for _, el := range codegen.GetArray(raw, "anyOf") {
		if vm, ok := el.(map[string]interface{}); ok {
			s.AnyOf = append(s.AnyOf, convertOne(RawSchema(vm)))
		}
	}
	if not := codegen.GetMap(raw, "not"); not != nil {
		s.Not = convertOne(RawSchema(not))
	}
	if v, ok := toFloat64Ptr(raw["minLength"]); ok {
		s.MinLength = v
	}
	if v, ok := toFloat64Ptr(raw["maxLength"]); ok {
		s.MaxLength = v
	}
	if v, ok := toFloat64Ptr(raw["minimum"]); ok {
		s.Minimum = v
	}
	if v, ok := toFloat64Ptr(raw["maximum"]); ok {
		s.Maximum = v
	}
	if v, ok := raw["additionalProperties"].(bool); ok {
		s.AdditionalProperties = &v
	}
	if pp := codegen.GetMap(raw, "patternProperties"); len(pp) > 0 {
		s.PatternProperties = make(map[string]*Schema, len(pp))
		for pat, sub := range pp {
			if subM, ok := sub.(map[string]interface{}); ok {
				s.PatternProperties[pat] = convertOne(RawSchema(subM))
			}
		}
	}
	return s
}

func toIntPtr(v interface{}) *int {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case float64:
		n := int(x)
		return &n
	case int:
		return &x
	case int64:
		n := int(x)
		return &n
	default:
		return nil
	}
}

func toFloat64Ptr(v interface{}) (*float64, bool) {
	if v == nil {
		return nil, false
	}
	switch x := v.(type) {
	case float64:
		return &x, true
	case int:
		f := float64(x)
		return &f, true
	case int64:
		f := float64(x)
		return &f, true
	default:
		return nil, false
	}
}

// toFloat64 converts a numeric value from JSON (float64, int, int64) to float64. Used by interpreter.
func toFloat64(v interface{}) (float64, bool) {
	if p, ok := toFloat64Ptr(v); ok && p != nil {
		return *p, true
	}
	return 0, false
}
