package validatorcodegen

import (
	"sort"
	"strconv"
	"strings"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/validator"
)

// emitCompiledMap appends Go source for generatedCompiled to b.
// It converts raw schemas to typed and calls CompileRoot(typedSchemas, rootPath).
func emitCompiledMap(b *strings.Builder, typeToPath map[string]string, schemas map[string]Schema) error {
	typedSchemas := validator.ConvertSchemas(schemas)
	b.WriteString("// Helper functions for pointer literals in generated compiled schemas.\n")
	b.WriteString("func intPtr(i int) *int { return &i }\n")
	b.WriteString("func float64Ptr(f float64) *float64 { return &f }\n")
	b.WriteString("func boolPtr(b bool) *bool { return &b }\n\n")
	typeNames := make([]string, 0, len(typeToPath))
	for k := range typeToPath {
		typeNames = append(typeNames, k)
	}
	sort.Strings(typeNames)

	b.WriteString("// generatedCompiled maps STIX type to compiled schema (path→constraints).\n")
	b.WriteString("var generatedCompiled = map[string]*CompiledSchema{\n")
	for _, typeName := range typeNames {
		rootPath := typeToPath[typeName]
		compiled, err := validator.CompileRoot(typedSchemas, rootPath)
		if err != nil {
			return err
		}
		if compiled == nil {
			continue
		}
		b.WriteString("\t")
		b.WriteString(strconv.Quote(typeName))
		b.WriteString(": ")
		emitCompiledSchema(b, compiled, 1)
		b.WriteString(",\n")
	}
	b.WriteString("}\n")
	return nil
}

func emitCompiledSchema(b *strings.Builder, c *validator.CompiledSchema, indent int) {
	if c == nil {
		b.WriteString("nil")
		return
	}
	b.WriteString("&CompiledSchema{\n")
	tab := strings.Repeat("\t", indent+1)
	b.WriteString(tab)
	b.WriteString("PathConstraints: map[string]*Constraints{\n")
	paths := make([]string, 0, len(c.PathConstraints))
	for p := range c.PathConstraints {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		con := c.PathConstraints[path]
		b.WriteString(tab)
		b.WriteString("\t")
		b.WriteString(strconv.Quote(path))
		b.WriteString(": ")
		emitConstraints(b, con, indent+2)
		b.WriteString(",\n")
	}
	b.WriteString(tab)
	b.WriteString("},\n")
	b.WriteString(strings.Repeat("\t", indent))
	b.WriteString("}")
}

func emitConstraints(b *strings.Builder, c *validator.Constraints, indent int) {
	if c == nil {
		b.WriteString("nil")
		return
	}
	b.WriteString("&Constraints{")
	needComma := false
	if c.Type != "" {
		b.WriteString("Type: ")
		b.WriteString(strconv.Quote(c.Type))
		needComma = true
	}
	if len(c.Enum) > 0 {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("Enum: []interface{}{")
		for i, e := range c.Enum {
			if i > 0 {
				b.WriteString(", ")
			}
			emitInterface(b, e)
		}
		b.WriteString("}")
		needComma = true
	}
	if c.Pattern != "" {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("Pattern: ")
		b.WriteString(strconv.Quote(c.Pattern))
		if c.PatternRE != nil {
			b.WriteString(", PatternRE: regexp.MustCompile(")
			b.WriteString(strconv.Quote(c.Pattern))
			b.WriteString(")")
		}
		needComma = true
	}
	if len(c.Required) > 0 {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("Required: []string{")
		for i, r := range c.Required {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(r))
		}
		b.WriteString("}")
		needComma = true
	}
	if c.MinLength != nil {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("MinLength: intPtr(")
		b.WriteString(strconv.Itoa(*c.MinLength))
		b.WriteString(")")
		needComma = true
	}
	if c.MaxLength != nil {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("MaxLength: intPtr(")
		b.WriteString(strconv.Itoa(*c.MaxLength))
		b.WriteString(")")
		needComma = true
	}
	if c.Minimum != nil {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("Minimum: float64Ptr(")
		b.WriteString(strconv.FormatFloat(*c.Minimum, 'g', -1, 64))
		b.WriteString(")")
		needComma = true
	}
	if c.Maximum != nil {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("Maximum: float64Ptr(")
		b.WriteString(strconv.FormatFloat(*c.Maximum, 'g', -1, 64))
		b.WriteString(")")
		needComma = true
	}
	if c.Items != nil {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("Items: ")
		emitConstraints(b, c.Items, indent)
		needComma = true
	}
	if c.MinItems != nil {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("MinItems: intPtr(")
		b.WriteString(strconv.Itoa(*c.MinItems))
		b.WriteString(")")
		needComma = true
	}
	if c.AdditionalProperties != nil {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("AdditionalProperties: boolPtr(")
		if *c.AdditionalProperties {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		b.WriteString(")")
		needComma = true
	}
	if len(c.PatternProperties) > 0 {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("PatternProperties: map[string]*Constraints{")
		patKeys := make([]string, 0, len(c.PatternProperties))
		for k := range c.PatternProperties {
			patKeys = append(patKeys, k)
		}
		sort.Strings(patKeys)
		for _, pat := range patKeys {
			b.WriteString(strconv.Quote(pat))
			b.WriteString(": ")
			emitConstraints(b, c.PatternProperties[pat], indent)
			b.WriteString(", ")
		}
		b.WriteString("}")
		needComma = true
	}
	if c.OneOf != nil && len(c.OneOf) > 0 {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("OneOf: []*Constraints{")
		for i, branch := range c.OneOf {
			if i > 0 {
				b.WriteString(", ")
			}
			emitConstraints(b, branch, indent)
		}
		b.WriteString("}")
		needComma = true
	}
	if c.AnyOf != nil && len(c.AnyOf) > 0 {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("AnyOf: []*Constraints{")
		for i, branch := range c.AnyOf {
			if i > 0 {
				b.WriteString(", ")
			}
			emitConstraints(b, branch, indent)
		}
		b.WriteString("}")
		needComma = true
	}
	if c.Not != nil {
		if needComma {
			b.WriteString(", ")
		}
		b.WriteString("Not: ")
		emitConstraints(b, c.Not, indent)
	}
	b.WriteString("}")
}

func emitInterface(b *strings.Builder, v interface{}) {
	if v == nil {
		b.WriteString("nil")
		return
	}
	switch x := v.(type) {
	case string:
		b.WriteString(strconv.Quote(x))
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case int:
		b.WriteString(strconv.Itoa(x))
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	default:
		b.WriteString(strconv.Quote("?"))
	}
}
