package matcher

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/pattern"
)

// literalToGo converts an AST Literal to a comparable Go value (int64, float64, string, bool, []byte).
// Timestamp strings are kept as string; caller can parse for comparison.
func literalToGo(l pattern.Literal) interface{} {
	if l.Int != nil {
		return *l.Int
	}
	if l.Float != nil {
		return *l.Float
	}
	if l.Str != nil {
		return *l.Str
	}
	if l.Bool != nil {
		return *l.Bool
	}
	if len(l.Binary) > 0 {
		return l.Binary
	}
	if len(l.Hex) > 0 {
		return l.Hex
	}
	if l.Timestamp != nil {
		return *l.Timestamp
	}
	return nil
}

// parseTimestamp parses STIX timestamp "2005-01-21T11:17:41Z" or "2005-01-21T11:17:41.123456Z".
func parseTimestamp(s string) (time.Time, error) {
	if strings.Contains(s, ".") {
		return time.Parse(time.RFC3339Nano, s)
	}
	return time.Parse("2006-01-02T15:04:05Z07:00", s)
}

// valueToComparable normalizes an observed-data value for comparison (e.g. string timestamp -> time.Time when comparing to timestamp literal).
func valueToComparable(val interface{}, patternLiteral pattern.Literal) interface{} {
	if val == nil {
		return nil
	}
	// If pattern has timestamp and value is string, try to parse as timestamp
	if patternLiteral.Timestamp != nil {
		if s, ok := val.(string); ok {
			t, err := parseTimestamp(s)
			if err == nil {
				return t
			}
		}
	}
	return val
}

// compareEq returns true if val matches the pattern literal for equality (or not-equal when negate).
func compareEq(val interface{}, lit pattern.Literal, negate bool) bool {
	patternVal := literalToGo(lit)
	val = valueToComparable(val, lit)
	result := valuesEqual(val, patternVal)
	if negate {
		result = !result
	}
	return result
}

func valuesEqual(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch x := a.(type) {
	case int64:
		switch y := b.(type) {
		case int64:
			return x == y
		case float64:
			return float64(x) == y
		}
	case float64:
		switch y := b.(type) {
		case float64:
			return x == y
		case int64:
			return x == float64(y)
		}
	case string:
		if y, ok := b.(string); ok {
			return x == y
		}
		// binary vs string: string must be all codepoints < 256, then compare as bytes
		if y, ok := b.([]byte); ok {
			return binaryStringEqual(y, x)
		}
	case []byte:
		if y, ok := b.([]byte); ok {
			return bytes.Equal(x, y)
		}
		if y, ok := b.(string); ok {
			return binaryStringEqual(x, y)
		}
	case bool:
		if y, ok := b.(bool); ok {
			return x == y
		}
	case time.Time:
		if y, ok := b.(time.Time); ok {
			return x.Equal(y)
		}
	}
	return false
}

func binaryStringEqual(bin []byte, s string) bool {
	rIdx := 0
	for _, r := range s {
		if r >= 256 {
			return false
		}
		if rIdx >= len(bin) {
			return false
		}
		if bin[rIdx] != byte(r) {
			return false
		}
		rIdx++
	}
	return rIdx == len(bin)
}

// compareOrder returns true for the given op (>, <, >=, <=).
func compareOrder(val interface{}, lit pattern.Literal, op string, negate bool) bool {
	patternVal := literalToGo(lit)
	val = valueToComparable(val, lit)
	cmp, ok := valuesCompare(val, patternVal)
	if !ok {
		return false
	}
	var result bool
	switch op {
	case ">":
		result = cmp > 0
	case "<":
		result = cmp < 0
	case ">=":
		result = cmp >= 0
	case "<=":
		result = cmp <= 0
	default:
		return false
	}
	if negate {
		result = !result
	}
	return result
}

// valuesCompare returns (cmp, ok): cmp <0, 0, >0; ok false if types not comparable.
func valuesCompare(a, b interface{}) (int, bool) {
	if a == nil || b == nil {
		return 0, false
	}
	switch x := a.(type) {
	case int64:
		switch y := b.(type) {
		case int64:
			if x < y {
				return -1, true
			}
			if x > y {
				return 1, true
			}
			return 0, true
		case float64:
			return compareFloat(float64(x), y)
		}
	case float64:
		switch y := b.(type) {
		case float64:
			return compareFloat(x, y)
		case int64:
			return compareFloat(x, float64(y))
		}
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y), true
		}
		if y, ok := b.([]byte); ok {
			return binaryStringCompare(x, y)
		}
	case []byte:
		if y, ok := b.([]byte); ok {
			return bytes.Compare(x, y), true
		}
		if y, ok := b.(string); ok {
			cmp, ok := binaryStringCompare(y, x)
			return -cmp, ok
		}
	case time.Time:
		if y, ok := b.(time.Time); ok {
			if x.Before(y) {
				return -1, true
			}
			if x.After(y) {
				return 1, true
			}
			return 0, true
		}
	}
	return 0, false
}

func compareFloat(a, b float64) (int, bool) {
	if a < b {
		return -1, true
	}
	if a > b {
		return 1, true
	}
	return 0, true
}

func binaryStringCompare(s string, bin []byte) (int, bool) {
	var b []byte
	for _, r := range s {
		if r >= 256 {
			return 0, false
		}
		b = append(b, byte(r))
	}
	return bytes.Compare(b, bin), true
}

// compareIn returns true if val is in the set.
func compareIn(val interface{}, set *pattern.SetLiteral, negate bool) bool {
	val = valueToComparableForSet(val, set)
	inSet := false
	for _, lit := range set.Values {
		if valuesEqual(val, literalToGo(lit)) {
			inSet = true
			break
		}
	}
	if negate {
		inSet = !inSet
	}
	return inSet
}

func valueToComparableForSet(val interface{}, set *pattern.SetLiteral) interface{} {
	if val == nil || len(set.Values) == 0 {
		return val
	}
	if set.Values[0].Timestamp != nil {
		if s, ok := val.(string); ok {
			t, err := parseTimestamp(s)
			if err == nil {
				return t
			}
		}
	}
	return val
}

// likeToRegex converts SQL-like pattern (% = .*, _ = .) to regex.
func likeToRegex(like string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range like {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			if r < 128 && (r == '.' || r == '[' || r == ']' || r == '(' || r == ')' || r == '\\' || r == '*' || r == '+' || r == '?' || r == '|') {
				b.WriteRune('\\')
			}
			b.WriteRune(r)
		}
	}
	b.WriteString("$")
	return b.String()
}

// compareLike returns true if val (string or []byte) matches the LIKE pattern.
func compareLike(val interface{}, pattern string, negate bool) bool {
	pattern = likeToRegex(pattern)
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	var s string
	switch x := val.(type) {
	case string:
		s = x
	case []byte:
		// pattern must be all < 256 for binary
		allASCII := true
		for _, r := range pattern {
			if r >= 256 {
				allASCII = false
				break
			}
		}
		if allASCII {
			s = string(x)
		} else {
			return false
		}
	default:
		return negate
	}
	matched := re.MatchString(s)
	if negate {
		matched = !matched
	}
	return matched
}

// compareMatches returns true if val matches the regex.
func compareMatches(val interface{}, regex string, negate bool) bool {
	re, err := regexp.Compile(regex)
	if err != nil {
		return false
	}
	var matched bool
	switch x := val.(type) {
	case string:
		matched = re.MatchString(x)
	case []byte:
		matched = re.Match(x)
	default:
		return negate
	}
	if negate {
		matched = !matched
	}
	return matched
}

// compareIsSubset returns true if val (IP or CIDR) is in the subnet.
func compareIsSubset(val interface{}, subnetCIDR string, negate bool) bool {
	s, ok := val.(string)
	if !ok {
		return negate
	}
	result := ipOrCIDRInSubnet(s, subnetCIDR)
	if negate {
		result = !result
	}
	return result
}

// compareIsSuperset returns true if subnetCIDR (pattern) contains val.
func compareIsSuperset(val interface{}, subnetCIDR string, negate bool) bool {
	s, ok := val.(string)
	if !ok {
		return negate
	}
	// ISSUPERSET: val is the container, pattern is the containee
	result := ipOrCIDRInSubnet(subnetCIDR, s)
	if negate {
		result = !result
	}
	return result
}

func ipOrCIDRInSubnet(ipOrCIDR, subnetCIDR string) bool {
	containeeIP, containeePrefix := parseIPOrCIDR(ipOrCIDR)
	containerIP, containerPrefix := parseCIDR(subnetCIDR)
	if containeeIP == nil || containerIP == nil {
		return false
	}
	if containerPrefix > containeePrefix {
		return false
	}
	mask := uint32(0xFFFFFFFF) << (32 - containerPrefix)
	return (containeeIP.toUint32() & mask) == (containerIP.toUint32() & mask)
}

type ipv4 [4]byte

func (i ipv4) toUint32() uint32 {
	return uint32(i[0])<<24 | uint32(i[1])<<16 | uint32(i[2])<<8 | uint32(i[3])
}

func parseIPOrCIDR(s string) (*ipv4, int) {
	if idx := strings.Index(s, "/"); idx >= 0 {
		ip := parseIPv4(s[:idx])
		if ip == nil {
			return nil, 0
		}
		p, err := strconv.Atoi(s[idx+1:])
		if err != nil || p < 1 || p > 32 {
			return nil, 0
		}
		return ip, p
	}
	ip := parseIPv4(s)
	if ip == nil {
		return nil, 0
	}
	return ip, 32
}

func parseCIDR(s string) (*ipv4, int) {
	idx := strings.Index(s, "/")
	if idx < 0 {
		return nil, 0
	}
	ip := parseIPv4(s[:idx])
	if ip == nil {
		return nil, 0
	}
	p, err := strconv.Atoi(s[idx+1:])
	if err != nil || p < 1 || p > 32 {
		return nil, 0
	}
	return ip, p
}

func parseIPv4(s string) *ipv4 {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return nil
	}
	var out ipv4
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return nil
		}
		out[i] = byte(n)
	}
	return &out
}
