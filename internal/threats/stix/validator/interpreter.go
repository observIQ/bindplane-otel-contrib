// schema interpreter using compiled path→constraints (walk instance once, lookup per path).

package validator

import (
	"fmt"
	"regexp"
	"strconv"
	"sync"
)

// patternCache caches compiled regexes for pattern property keys (and any Pattern string)
// so we don't recompile the same pattern on every validation.
var patternCache sync.Map // map[string]*regexp.Regexp

func getCompiledPattern(pat string) *regexp.Regexp {
	if pat == "" {
		return nil
	}
	if v, ok := patternCache.Load(pat); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(pat)
	patternCache.Store(pat, re)
	return re
}

// ValidationError is a single schema validation failure (path + message).
// The public API converts these to validator.SchemaError.
type ValidationError struct {
	Path    string
	Message string
}

// ValidateObject validates a JSON object (as map[string]interface{}) against the compiled schema
// for the given STIX type. It walks the instance once and looks up constraints per path (O(1) or O(depth)).
// Returns nil error slice if valid.
func ValidateObject(instance map[string]interface{}, typeName string, loader *Loader) ([]ValidationError, error) {
	if loader == nil {
		return nil, fmt.Errorf("loader is nil")
	}
	compiled := loader.CompiledForType(typeName)
	if compiled == nil {
		return nil, fmt.Errorf("no compiled schema for type %q", typeName)
	}
	var errs []ValidationError
	walkInstance(instance, "", compiled.PathConstraints, &errs)
	return errs, nil
}

// pathForKey returns the lookup key for constraints: use "[]" for array indices so
// "external_references.0.url" matches compiled path "external_references.[].url".
func pathForKey(segment string, path string) string {
	if path == "" {
		return segment
	}
	return path + "." + segment
}

// isNumericSegment returns true if s is non-empty and all digits (no allocation).
func isNumericSegment(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

var normalizePathCache sync.Map // map[string]string

// normalizePathForLookup replaces numeric array indices with "[]" for constraint lookup.
func normalizePathForLookup(path string) string {
	if v, ok := normalizePathCache.Load(path); ok {
		return v.(string)
	}
	out := ""
	for i := 0; i < len(path); i++ {
		if path[i] == '.' {
			out += "."
			continue
		}
		j := i
		for j < len(path) && path[j] != '.' {
			j++
		}
		seg := path[i:j]
		i = j - 1
		if isNumericSegment(seg) {
			out += "[]"
		} else {
			out += seg
		}
	}
	normalizePathCache.Store(path, out)
	return out
}

// validateAgainstConstraints validates value at path against a single Constraints tree only.
// It does not use PathConstraints; use for oneOf/anyOf branches and not. Returns errors (empty if valid).
func validateAgainstConstraints(value interface{}, c *Constraints, path string) []ValidationError {
	if c == nil {
		return nil
	}
	var errs []ValidationError
	// AnyOf: value must match at least one branch (recurse first so we evaluate branches correctly)
	if len(c.AnyOf) > 0 {
		anyPassed := false
		for _, branch := range c.AnyOf {
			if len(validateAgainstConstraints(value, branch, path)) == 0 {
				anyPassed = true
				break
			}
		}
		if !anyPassed {
			errs = append(errs, ValidationError{Path: path, Message: "must match at least one of anyOf"})
			return errs
		}
	}
	// OneOf: value must match at least one branch
	if len(c.OneOf) > 0 {
		var passCount int
		for _, branch := range c.OneOf {
			if len(validateAgainstConstraints(value, branch, path)) == 0 {
				passCount++
			}
		}
		if passCount < 1 {
			errs = append(errs, ValidationError{Path: path, Message: "must match at least one of oneOf"})
			return errs
		}
	}
	// Not: value must not match this subschema (if it matches, that's an error)
	if c.Not != nil {
		if len(validateAgainstConstraints(value, c.Not, path)) == 0 {
			errs = append(errs, ValidationError{Path: path, Message: "must not match schema"})
			return errs
		}
	}
	// Type
	if c.Type != "" {
		switch value.(type) {
		case map[string]interface{}:
			if c.Type != "object" {
				errs = append(errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got object"})
			}
		case []interface{}:
			if c.Type != "array" {
				errs = append(errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got array"})
			}
		case string:
			if c.Type != "string" {
				errs = append(errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got string"})
			}
		case float64:
			if c.Type != "number" && c.Type != "integer" {
				errs = append(errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got number"})
			}
		case bool:
			if c.Type != "boolean" {
				errs = append(errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got boolean"})
			}
		case nil:
			errs = append(errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got null"})
		default:
			if _, ok := toFloat64(value); ok {
				if c.Type != "number" && c.Type != "integer" {
					errs = append(errs, ValidationError{Path: path, Message: "expected type " + c.Type})
				}
			} else {
				errs = append(errs, ValidationError{Path: path, Message: "expected type " + c.Type})
			}
		}
	}
	// Required (for objects)
	if len(c.Required) > 0 {
		if obj, ok := value.(map[string]interface{}); ok {
			for _, req := range c.Required {
				if _, has := obj[req]; !has {
					errs = append(errs, ValidationError{Path: pathForKey(req, path), Message: "missing required property: " + req})
				}
			}
		}
	}
	// Enum
	if len(c.Enum) > 0 {
		ok := false
		for _, e := range c.Enum {
			if eqInterface(value, e) {
				ok = true
				break
			}
		}
		if !ok {
			errs = append(errs, ValidationError{Path: path, Message: "value not in enum"})
		}
	}
	// Pattern, MinLength, MaxLength (strings)
	if s, ok := value.(string); ok {
		if c.Pattern != "" || c.PatternRE != nil {
			var matched bool
			var err error
			if c.PatternRE != nil {
				matched = c.PatternRE.MatchString(s)
			} else {
				matched, err = regexp.MatchString(c.Pattern, s)
			}
			if err != nil || !matched {
				errs = append(errs, ValidationError{Path: path, Message: "string does not match pattern " + c.Pattern})
			}
		}
		if c.MinLength != nil && len(s) < *c.MinLength {
			errs = append(errs, ValidationError{Path: path, Message: "string length < minLength"})
		}
		if c.MaxLength != nil && len(s) > *c.MaxLength {
			errs = append(errs, ValidationError{Path: path, Message: "string length > maxLength"})
		}
	}
	// Minimum, Maximum (numbers)
	if f, ok := toFloat64(value); ok {
		if c.Minimum != nil && f < *c.Minimum {
			errs = append(errs, ValidationError{Path: path, Message: "value < minimum"})
		}
		if c.Maximum != nil && f > *c.Maximum {
			errs = append(errs, ValidationError{Path: path, Message: "value > maximum"})
		}
	}
	// Items (array)
	if c.Items != nil {
		if arr, ok := value.([]interface{}); ok {
			for i, item := range arr {
				childPath := path + "." + strconv.Itoa(i)
				errs = append(errs, validateAgainstConstraints(item, c.Items, childPath)...)
			}
		}
	}
	// PatternProperties (object)
	if len(c.PatternProperties) > 0 {
		if obj, ok := value.(map[string]interface{}); ok {
			for k, val := range obj {
				for pat, sub := range c.PatternProperties {
					re := getCompiledPattern(pat)
					if re != nil && re.MatchString(k) {
						childPath := pathForKey(k, path)
						errs = append(errs, validateAgainstConstraints(val, sub, childPath)...)
						break
					}
				}
			}
		}
	}
	return errs
}

func walkInstance(value interface{}, path string, constraints map[string]*Constraints, errs *[]ValidationError) {
	key := normalizePathForLookup(path)
	c := constraints[key]

	if c != nil {
		// oneOf / anyOf / not first (JSON Schema: all keywords apply, but these constrain branch / negation)
		if len(c.OneOf) > 0 {
			var passCount int
			for _, branch := range c.OneOf {
				if len(validateAgainstConstraints(value, branch, path)) == 0 {
					passCount++
				}
			}
			// oneOf: at least one branch must match (accept multiple matches due to compiler flattening)
			if passCount < 1 {
				*errs = append(*errs, ValidationError{Path: path, Message: "must match at least one of oneOf"})
			}
		}
		if len(c.AnyOf) > 0 {
			anyPassed := false
			for _, branch := range c.AnyOf {
				if len(validateAgainstConstraints(value, branch, path)) == 0 {
					anyPassed = true
					break
				}
			}
			if !anyPassed {
				*errs = append(*errs, ValidationError{Path: path, Message: "must match at least one of anyOf"})
			}
		}
		if c.Not != nil {
			if len(validateAgainstConstraints(value, c.Not, path)) == 0 {
				*errs = append(*errs, ValidationError{Path: path, Message: "must not match schema"})
			}
		}
		// Check required (for objects): ensure all required keys are present
		if len(c.Required) > 0 {
			if obj, ok := value.(map[string]interface{}); ok {
				for _, req := range c.Required {
					if _, has := obj[req]; !has {
						*errs = append(*errs, ValidationError{Path: pathForKey(req, path), Message: "missing required property: " + req})
					}
				}
			}
		}
	}

	switch v := value.(type) {
	case map[string]interface{}:
		if c != nil && c.Type != "" && c.Type != "object" {
			*errs = append(*errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got object"})
			return
		}
		for k, val := range v {
			childPath := pathForKey(k, path)
			walkInstance(val, childPath, constraints, errs)
		}
	case []interface{}:
		if c != nil && c.Type != "" && c.Type != "array" {
			*errs = append(*errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got array"})
			return
		}
		if c != nil && c.MinItems != nil && len(v) < *c.MinItems {
			*errs = append(*errs, ValidationError{Path: path, Message: "array has fewer than minItems elements"})
		}
		for i, item := range v {
			childPath := path + "." + strconv.Itoa(i)
			walkInstance(item, childPath, constraints, errs)
		}
	case string:
		checkPrimitive(c, path, "string", v, errs)
	case float64:
		checkPrimitive(c, path, "number", v, errs)
	case bool:
		checkPrimitive(c, path, "boolean", v, errs)
	case nil:
		// nil is valid unless type forbids it
		if c != nil && c.Type != "" {
			*errs = append(*errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got null"})
		}
	default:
		// int, int64, etc. may come from JSON
		if c != nil && c.Type != "" && c.Type != "number" && c.Type != "integer" {
			*errs = append(*errs, ValidationError{Path: path, Message: "expected type " + c.Type})
		}
	}
}

func checkPrimitive(c *Constraints, path, typ string, value interface{}, errs *[]ValidationError) {
	if c == nil {
		return
	}
	if c.Type != "" && c.Type != typ {
		if typ == "number" && c.Type == "integer" {
			// allow float64 for integer in JSON
		} else {
			*errs = append(*errs, ValidationError{Path: path, Message: "expected type " + c.Type + ", got " + typ})
			return
		}
	}
	if len(c.Enum) > 0 {
		ok := false
		for _, e := range c.Enum {
			if eqInterface(value, e) {
				ok = true
				break
			}
		}
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "value not in enum"})
		}
	}
	if s, ok := value.(string); ok {
		if c.Pattern != "" || c.PatternRE != nil {
			var matched bool
			var err error
			if c.PatternRE != nil {
				matched = c.PatternRE.MatchString(s)
			} else {
				matched, err = regexp.MatchString(c.Pattern, s)
			}
			if err != nil || !matched {
				*errs = append(*errs, ValidationError{Path: path, Message: "string does not match pattern " + c.Pattern})
			}
		}
		if c.MinLength != nil && len(s) < *c.MinLength {
			*errs = append(*errs, ValidationError{Path: path, Message: "string length < minLength"})
		}
		if c.MaxLength != nil && len(s) > *c.MaxLength {
			*errs = append(*errs, ValidationError{Path: path, Message: "string length > maxLength"})
		}
	}
	if f, ok := toFloat64(value); ok {
		if c.Minimum != nil && f < *c.Minimum {
			*errs = append(*errs, ValidationError{Path: path, Message: "value < minimum"})
		}
		if c.Maximum != nil && f > *c.Maximum {
			*errs = append(*errs, ValidationError{Path: path, Message: "value > maximum"})
		}
	}
}

func eqInterface(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch x := a.(type) {
	case string:
		if y, ok := b.(string); ok {
			return x == y
		}
	case float64:
		if y, ok := b.(float64); ok {
			return x == y
		}
		if y, ok := b.(int); ok {
			return x == float64(y)
		}
	case bool:
		if y, ok := b.(bool); ok {
			return x == y
		}
	}
	return false
}
