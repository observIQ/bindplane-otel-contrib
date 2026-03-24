// compile typed JSON Schema to path→constraints for fast validation.

package validator

import "regexp"

// CompileRoot compiles a root schema (resolve $ref, merge allOf) into a CompiledSchema
// keyed by instance path. Paths use "[]" for array indices (e.g. "external_references.[].url").
// For the bundle schema, objects.[] is replaced with a minimal constraint so each bundle
// element (objects.0, objects.1, ...) is validated as an object; validateBundle then
// validates by STIX type. Using Type: "array" here would wrongly require each element
// to be an array (causing "expected type array, got object" for valid bundles).
func CompileRoot(schemas map[string]*Schema, rootPath string) (*CompiledSchema, error) {
	s, ok := schemas[rootPath]
	if !ok || s == nil {
		return nil, nil
	}
	out := &CompiledSchema{PathConstraints: make(map[string]*Constraints)}
	effective := resolveFragment(schemas, rootPath, s)
	compileRec(schemas, rootPath, effective, "", out)
	// For the bundle schema, objects.[] is replaced with a minimal constraint so each bundle
	// element (objects.0, objects.1, ...) is validated as an object; validateBundle then
	// validates by STIX type. Using Type: "array" here would wrongly require each element
	// to be an array (causing "expected type array, got object" for valid bundles).
	if rootPath == "common/bundle.json" {
		out.PathConstraints["objects.[]"] = &Constraints{Type: "object"}
	}
	return out, nil
}

// resolveFragment merges $ref and allOf at schema node s (basePath is the file path for ref resolution).
// Returns a single *Schema with no top-level $ref or allOf (merged into the result).
func resolveFragment(schemas map[string]*Schema, basePath string, s *Schema) *Schema {
	if s == nil {
		return nil
	}
	result := &Schema{}
	seenRequired := make(map[string]bool)
	var required []string

	var merge func(m *Schema, base string)
	merge = func(m *Schema, base string) {
		if m == nil {
			return
		}
		for _, r := range m.Required {
			if !seenRequired[r] {
				seenRequired[r] = true
				required = append(required, r)
			}
		}
		if m.Type != "" && result.Type == "" {
			result.Type = m.Type
		}
		if m.Pattern != "" && result.Pattern == "" {
			result.Pattern = m.Pattern
		}
		if len(m.Enum) > 0 && len(result.Enum) == 0 {
			result.Enum = m.Enum
		}
		if m.Minimum != nil && result.Minimum == nil {
			result.Minimum = m.Minimum
		}
		if m.Maximum != nil && result.Maximum == nil {
			result.Maximum = m.Maximum
		}
		if m.MinLength != nil && result.MinLength == nil {
			result.MinLength = m.MinLength
		}
		if m.MaxLength != nil && result.MaxLength == nil {
			result.MaxLength = m.MaxLength
		}
		if m.AdditionalProperties != nil && result.AdditionalProperties == nil {
			result.AdditionalProperties = m.AdditionalProperties
		}
		if len(m.PatternProperties) > 0 && len(result.PatternProperties) == 0 {
			result.PatternProperties = m.PatternProperties
		}
		if result.Properties == nil {
			result.Properties = make(map[string]*Schema)
		}
		if m.Properties != nil {
			for k, v := range m.Properties {
				result.Properties[k] = v
			}
		}
		if m.Items != nil {
			result.Items = m.Items
		}
		if len(m.OneOf) > 0 {
			var resolved []*Schema
			for _, el := range m.OneOf {
				resolved = append(resolved, resolveFragment(schemas, base, el))
			}
			result.OneOf = resolved
		}
		if len(m.AnyOf) > 0 {
			var resolved []*Schema
			for _, el := range m.AnyOf {
				resolved = append(resolved, resolveFragment(schemas, base, el))
			}
			result.AnyOf = resolved
		}
		if m.Not != nil {
			result.Not = resolveFragment(schemas, base, m.Not)
		}
		for _, item := range m.AllOf {
			if item == nil {
				continue
			}
			if item.Ref != "" {
				nextPath := ResolveRef(base, item.Ref)
				if next, ok := schemas[nextPath]; ok {
					merge(next, nextPath)
				}
				if item.Properties != nil {
					if result.Properties == nil {
						result.Properties = make(map[string]*Schema)
					}
					for k, v := range item.Properties {
						result.Properties[k] = v
					}
				}
				for _, r := range item.Required {
					if !seenRequired[r] {
						seenRequired[r] = true
						required = append(required, r)
					}
				}
			} else {
				merge(item, base)
			}
		}
		if m.Ref != "" {
			nextPath := ResolveRef(base, m.Ref)
			if next, ok := schemas[nextPath]; ok {
				merge(next, nextPath)
			}
		}
	}
	merge(s, basePath)
	if len(required) > 0 {
		result.Required = required
	}
	return result
}

func compileRec(schemas map[string]*Schema, basePath string, s *Schema, path string, out *CompiledSchema) {
	if s == nil {
		return
	}
	c := extractConstraints(s)
	if path != "" {
		out.PathConstraints[path] = c
	} else if c != nil {
		out.PathConstraints[""] = c
	}

	if s.Type == "object" && s.Properties != nil {
		for k, v := range s.Properties {
			if v == nil {
				continue
			}
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			effective := resolveFragment(schemas, basePath, v)
			compileRec(schemas, basePath, effective, childPath, out)
		}
	}
	if s.Type == "array" && s.Items != nil {
		itemPath := path + ".[]"
		if path == "" {
			itemPath = "[]"
		}
		effective := resolveFragment(schemas, basePath, s.Items)
		compileRec(schemas, basePath, effective, itemPath, out)
	}
}

func extractConstraints(s *Schema) *Constraints {
	if s == nil {
		return nil
	}
	c := &Constraints{}
	c.Type = s.Type
	c.Pattern = s.Pattern
	if s.Pattern != "" {
		c.PatternRE = regexp.MustCompile(s.Pattern)
	}
	if len(s.Enum) > 0 {
		c.Enum = s.Enum
	}
	c.Required = append(c.Required, s.Required...)
	if s.MinLength != nil {
		n := int(*s.MinLength)
		c.MinLength = &n
	}
	if s.MaxLength != nil {
		n := int(*s.MaxLength)
		c.MaxLength = &n
	}
	if s.Minimum != nil {
		c.Minimum = s.Minimum
	}
	if s.Maximum != nil {
		c.Maximum = s.Maximum
	}
	if s.AdditionalProperties != nil {
		c.AdditionalProperties = s.AdditionalProperties
	}
	if len(s.PatternProperties) > 0 {
		c.PatternProperties = make(map[string]*Constraints)
		for pat, sub := range s.PatternProperties {
			c.PatternProperties[pat] = extractConstraints(sub)
		}
	}
	if s.Items != nil {
		c.Items = extractConstraints(s.Items)
	}
	if s.MinItems != nil {
		n := *s.MinItems
		c.MinItems = &n
	}
	if len(s.OneOf) > 0 {
		for _, sub := range s.OneOf {
			c.OneOf = append(c.OneOf, extractConstraints(sub))
		}
	}
	if len(s.AnyOf) > 0 {
		for _, sub := range s.AnyOf {
			c.AnyOf = append(c.AnyOf, extractConstraints(sub))
		}
	}
	if s.Not != nil {
		c.Not = extractConstraints(s.Not)
	}
	return c
}
