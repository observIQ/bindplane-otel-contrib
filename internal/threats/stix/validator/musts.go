// MUST (mandatory) checks per STIX 2.1 spec.
// Ported from cti-stix-validator v21/musts.py.

package validator

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix"
	stixpattern "github.com/observiq/bindplane-otel-collector/internal/threats/stix/pattern"
)

// MustOptions configures which MUST checks run (e.g. pattern format-checks / custom-prefix can be disabled).
type MustOptions struct {
	// Disabled lists check names to skip (e.g. "format-checks", "custom-prefix", "custom-prefix-lax").
	Disabled []string
}

// RunMUSTs runs all MUST validators on the object and returns any errors.
// For observed-data, also runs observable-scoped MUSTs on each inner object (errors attributed to the observed-data id).
func RunMUSTs(obj map[string]interface{}, typ string, opts *MustOptions) []ValidationError {
	if opts == nil {
		opts = &MustOptions{}
	}
	var errs []ValidationError
	run := func(fn func(map[string]interface{}, string, *MustOptions) []ValidationError) {
		errs = append(errs, fn(obj, typ, opts)...)
	}
	run(mustTimestamp)
	run(mustTimestampCompare)
	run(mustObservableTimestampCompare)
	run(mustObjectMarkingCircularRefs)
	run(mustGranularMarkingsCircularRefs)
	run(mustMarkingSelectorSyntax)
	run(mustObservableObjectReferences)
	run(mustArtifactMIMEType)
	run(mustCharacterSet)
	run(mustLanguage)
	run(mustSoftwareLanguage)
	run(mustPatterns)
	run(mustCPECheck)
	run(mustLanguageContents)
	run(mustUUIDVersionCheck)
	run(mustProcess)

	// For observed-data, run observable-scoped MUSTs on each inner object
	if typ == "observed-data" {
		objects, _ := obj["objects"].(map[string]interface{})
		if objects != nil {
			for _, val := range objects {
				inner, ok := val.(map[string]interface{})
				if !ok {
					continue
				}
				innerTyp, _ := inner["type"].(string)
				if innerTyp == "" {
					continue
				}
				errs = append(errs, mustObservableTimestampCompare(inner, innerTyp, opts)...)
				errs = append(errs, mustArtifactMIMEType(inner, innerTyp, opts)...)
				errs = append(errs, mustCharacterSet(inner, innerTyp, opts)...)
				errs = append(errs, mustSoftwareLanguage(inner, innerTyp, opts)...)
			}
		}
	}
	return errs
}

func disabled(opts *MustOptions, names ...string) bool {
	if opts == nil {
		return false
	}
	for _, d := range opts.Disabled {
		for _, n := range names {
			if d == n {
				return true
			}
		}
	}
	return false
}

var (
	timestampFormatRE = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[012])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):([0-5][0-9]):([0-5][0-9]|60)(\.[0-9]+)?Z$`)
	typeFormatRE      = regexp.MustCompile(`^\-?[a-z0-9]+(-[a-z0-9]+)*\-?$`)
	propertyFormatRE  = regexp.MustCompile(`^[a-z0-9_]{3,250}$`)
	customTypePrefix  = regexp.MustCompile(`^x\-.+\-.+$`)
	customTypeLax     = regexp.MustCompile(`^x\-.+$`)
	customPropPrefix  = regexp.MustCompile(`^x_.+_.+$`)
	customPropLax     = regexp.MustCompile(`^x_.+$`)
	cpe23RE           = regexp.MustCompile(`^cpe:2\.3:[aho\*]:[^:]*:[^:]*:[^:]*:[^:]*:[^:]*:[^:]*:[^:]*:[^:]*:[^:]*:[^:]*$`)
	listIndexRE       = regexp.MustCompile(`^\[(\d+)\]$`)
)

func mustTimestamp(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	var errs []ValidationError
	props := []string{"created", "modified"}
	if list, ok := TIMESTAMP_PROPERTIES[typ]; ok {
		props = append(props, list...)
	}
	for _, p := range props {
		val, ok := obj[p].(string)
		if !ok || val == "" {
			continue
		}
		// Try parsing; if it fails, report error. Only skip if format is clearly not a timestamp (no T).
		if _, err := time.Parse(time.RFC3339Nano, val); err != nil {
			if _, err2 := time.Parse("2006-01-02T15:04:05.999999999Z07:00", val); err2 != nil {
				// Report error for created/modified and other known timestamp props (e.g. "2020-01-32")
				errs = append(errs, ValidationError{Path: p, Message: "'" + p + "': '" + val + "' is not a valid timestamp: " + err.Error()})
			}
		}
	}
	if typ == "observed-data" {
		objects, _ := obj["objects"].(map[string]interface{})
		for _, v := range objects {
			inner, _ := v.(map[string]interface{})
			if inner == nil {
				continue
			}
			innerTyp, _ := inner["type"].(string)
			for _, p := range TIMESTAMP_OBSERVABLE_PROPERTIES[innerTyp] {
				val, ok := inner[p].(string)
				if !ok || !timestampFormatRE.MatchString(val) {
					continue
				}
				if _, err := time.Parse(time.RFC3339Nano, val); err != nil {
					errs = append(errs, ValidationError{Path: "objects", Message: "'" + innerTyp + "': '" + p + "': '" + val + "' is not a valid timestamp: " + err.Error()})
				}
			}
			for embed, m := range TIMESTAMP_EMBEDDED_PROPERTIES[innerTyp] {
				embedVal := inner[embed]
				embedMap, ok := embedVal.(map[string]interface{})
				if !ok {
					continue
				}
				for _, tprop := range m {
					if embed == "extensions" {
						for _, extVal := range embedMap {
							extObj, _ := extVal.(map[string]interface{})
							if extObj == nil {
								continue
							}
							if tv, ok := extObj[tprop].(string); ok && timestampFormatRE.MatchString(tv) {
								if _, err := time.Parse(time.RFC3339Nano, tv); err != nil {
									errs = append(errs, ValidationError{Path: "objects", Message: "invalid timestamp in extensions"})
								}
							}
						}
					} else if tv, ok := embedMap[tprop].(string); ok && timestampFormatRE.MatchString(tv) {
						if _, err := time.Parse(time.RFC3339Nano, tv); err != nil {
							errs = append(errs, ValidationError{Path: "objects", Message: "invalid embedded timestamp"})
						}
					}
				}
			}
		}
	} else if list, ok := TIMESTAMP_OBSERVABLE_PROPERTIES[typ]; ok {
		for _, p := range list {
			val, ok := obj[p].(string)
			if !ok || !timestampFormatRE.MatchString(val) {
				continue
			}
			if _, err := time.Parse(time.RFC3339Nano, val); err != nil {
				errs = append(errs, ValidationError{Path: p, Message: "'" + typ + "': '" + p + "': '" + val + "' is not a valid timestamp"})
			}
		}
	}
	return errs
}

func parseTimestamp(s string) (t time.Time, ok bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t, true
	}
	t, err = time.Parse("2006-01-02T15:04:05Z07:00", s)
	if err == nil {
		return t, true
	}
	t, err = time.Parse("2006-01-02T15:04:05.999999999Z07:00", s)
	return t, err == nil
}

func mustTimestampCompare(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	var errs []ValidationError
	compares := [][3]string{{"modified", "ge", "created"}}
	if list, ok := TIMESTAMP_COMPARE[typ]; ok {
		compares = append(compares, list...)
	}
	for _, c := range compares {
		first, fok := obj[c[0]].(string)
		second, sok := obj[c[2]].(string) // c[0]=firstProp, c[1]=op, c[2]=secondProp
		if !fok || !sok {
			continue
		}
		if !timestampFormatRE.MatchString(first) || !timestampFormatRE.MatchString(second) {
			continue
		}
		t1, ok1 := parseTimestamp(first)
		t2, ok2 := parseTimestamp(second)
		if !ok1 || !ok2 {
			continue
		}
		valid := false
		switch c[1] {
		case "ge":
			valid = !t1.Before(t2)
		case "gt":
			valid = t1.After(t2)
		default:
			continue
		}
		if !valid {
			compStr := "later than or equal to"
			if c[1] == "gt" {
				compStr = "later than"
			}
			errs = append(errs, ValidationError{Path: c[0], Message: "'" + c[0] + "' (" + first + ") must be " + compStr + " '" + c[2] + "' (" + second + ")"})
		}
	}
	return errs
}

func mustObservableTimestampCompare(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	compares, ok := TIMESTAMP_COMPARE_OBSERVABLE[typ]
	if !ok {
		return nil
	}
	var errs []ValidationError
	for _, c := range compares {
		first, fok := obj[c[0]].(string)
		second, sok := obj[c[2]].(string) // c[0]=firstProp, c[1]=op, c[2]=secondProp
		if !fok || !sok {
			continue
		}
		t1, ok1 := parseTimestamp(first)
		t2, ok2 := parseTimestamp(second)
		if !ok1 || !ok2 {
			continue
		}
		valid := false
		switch c[1] {
		case "ge":
			valid = !t1.Before(t2)
		case "gt":
			valid = t1.After(t2)
		default:
			continue
		}
		if !valid {
			id, _ := obj["id"].(string)
			if id == "" {
				id = typ
			}
			errs = append(errs, ValidationError{Path: c[0], Message: "In object '" + id + "', '" + c[0] + "' (" + first + ") must be later than or equal to '" + c[2] + "' (" + second + ")"})
		}
	}
	return errs
}

func mustObjectMarkingCircularRefs(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	if typ != "marking-definition" {
		return nil
	}
	id, _ := obj["id"].(string)
	refs, _ := obj["object_marking_refs"].([]interface{})
	for _, r := range refs {
		if ref, ok := r.(string); ok && ref == id {
			return []ValidationError{{Path: "object_marking_refs", Message: "`object_marking_refs` cannot contain any references to this marking definition object (no circular references)."}}
		}
	}
	return nil
}

func mustGranularMarkingsCircularRefs(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	if typ != "marking-definition" {
		return nil
	}
	id, _ := obj["id"].(string)
	gm, _ := obj["granular_markings"].([]interface{})
	for _, m := range gm {
		marking, _ := m.(map[string]interface{})
		if marking == nil {
			continue
		}
		if ref, ok := marking["marking_ref"].(string); ok && ref == id {
			return []ValidationError{{Path: "granular_markings", Message: "`granular_markings` cannot contain any references to this marking definition object (no circular references)."}}
		}
	}
	return nil
}

func mustMarkingSelectorSyntax(obj map[string]interface{}, _ string, _ *MustOptions) []ValidationError {
	var errs []ValidationError
	gm, _ := obj["granular_markings"].([]interface{})
	for _, m := range gm {
		marking, _ := m.(map[string]interface{})
		if marking == nil {
			continue
		}
		selectors, _ := marking["selectors"].([]interface{})
		for _, sel := range selectors {
			selector, ok := sel.(string)
			if !ok {
				continue
			}
			segments := strings.Split(selector, ".")
			current := interface{}(obj)
			var prevSeg string
			for _, seg := range segments {
				if m := listIndexRE.FindStringSubmatch(seg); len(m) > 0 {
					idx, _ := strconv.Atoi(m[1])
					arr, ok := current.([]interface{})
					if !ok {
						errs = append(errs, ValidationError{Path: "granular_markings", Message: "'" + selector + "' is not a valid selector because '" + prevSeg + "' is not a list."})
						break
					}
					if idx < 0 || idx >= len(arr) {
						errs = append(errs, ValidationError{Path: "granular_markings", Message: "'" + selector + "' is not a valid selector because " + strconv.Itoa(idx) + " is not a valid index."})
						break
					}
					current = arr[idx]
				} else {
					o, ok := current.(map[string]interface{})
					if !ok {
						errs = append(errs, ValidationError{Path: "granular_markings", Message: "'" + selector + "' is not a valid selector because '" + seg + "' is not a property."})
						break
					}
					var exists bool
					current, exists = o[seg]
					if !exists {
						errs = append(errs, ValidationError{Path: "granular_markings", Message: "'" + selector + "' is not a valid selector because " + seg + " is not a property."})
						break
					}
				}
				prevSeg = seg
			}
		}
	}
	return errs
}

func mustObservableObjectReferences(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	if typ != "observed-data" {
		return nil
	}
	var errs []ValidationError
	objects, _ := obj["objects"].(map[string]interface{})
	if objects == nil {
		return nil
	}
	for key, v := range objects {
		inner, _ := v.(map[string]interface{})
		if inner == nil {
			continue
		}
		errs = append(errs, observableObjectReferencesHelper(inner, key, objects)...)
	}
	return errs
}

// refsFromValue normalizes a property value to a slice of refs (single ref or list).
func refsFromValue(val interface{}) []interface{} {
	if list, ok := val.([]interface{}); ok {
		return list
	}
	if val != nil {
		return []interface{}{val}
	}
	return nil
}

// allowedTypesFromRefSpec extracts allowed observable types from a ref spec (e.g. []interface{} of type names).
func allowedTypesFromRefSpec(spec interface{}) []string {
	arr, ok := spec.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// validateObservableRefList checks each ref resolves to an object whose type is in allowed; returns validation errors.
func validateObservableRefList(refs []interface{}, allowed []string, objects map[string]interface{}, objKey, propLabel string) []ValidationError {
	var errs []ValidationError
	for _, r := range refs {
		refID, ok := r.(string)
		if !ok {
			continue
		}
		refed, ok := objects[refID]
		if !ok {
			errs = append(errs, ValidationError{Path: "objects", Message: propLabel + " in observable object '" + objKey + "' can't resolve reference '" + refID + "'."})
			continue
		}
		refedObj, _ := refed.(map[string]interface{})
		if refedObj == nil {
			continue
		}
		refedType, _ := refedObj["type"].(string)
		allowedType := false
		for _, a := range allowed {
			if refedType == a {
				allowedType = true
				break
			}
		}
		if !allowedType {
			valids := "'" + strings.Join(allowed, "' or '") + "'"
			errs = append(errs, ValidationError{Path: "objects", Message: "'" + propLabel + "' in observable object '" + objKey + "' must refer to an object of type " + valids + "."})
		}
	}
	return errs
}

func observableObjectReferencesHelper(obj map[string]interface{}, key string, objects map[string]interface{}) []ValidationError {
	var errs []ValidationError
	objType, _ := obj["type"].(string)
	if objType == "" {
		return nil
	}
	refSpec, ok := OBSERVABLE_PROP_REFS[objType]
	if !ok {
		return nil
	}
	refMap, ok := refSpec.(map[string]interface{})
	if !ok {
		return nil
	}
	for objProp, enumProp := range refMap {
		val := obj[objProp]
		if val == nil {
			continue
		}
		switch ev := enumProp.(type) {
		case []interface{}:
			refs := refsFromValue(val)
			allowed := allowedTypesFromRefSpec(ev)
			errs = append(errs, validateObservableRefList(refs, allowed, objects, key, objProp)...)
		case map[string]interface{}:
			objPropVal, _ := obj[objProp].(map[string]interface{})
			if objPropVal != nil {
				for embeddedProp, embedSpec := range ev {
					embeddedObj, ok := objPropVal[embeddedProp].(map[string]interface{})
					if !ok {
						continue
					}
					embedSpecMap, ok := embedSpec.(map[string]interface{})
					if !ok {
						continue
					}
					for eoProp, eoAllowed := range embedSpecMap {
						refs, ok := embeddedObj[eoProp].([]interface{})
						if !ok {
							continue
						}
						allowed := allowedTypesFromRefSpec(eoAllowed)
						errs = append(errs, validateObservableRefList(refs, allowed, objects, key, objProp)...)
					}
				}
			}
			objPropList, ok := obj[objProp].([]interface{})
			if ok {
				for _, item := range objPropList {
					embeddedObj, _ := item.(map[string]interface{})
					if embeddedObj == nil {
						continue
					}
					for embeddedProp, embedAllowed := range ev {
						refs, ok := embeddedObj[embeddedProp].([]interface{})
						if !ok {
							continue
						}
						allowed := allowedTypesFromRefSpec(embedAllowed)
						errs = append(errs, validateObservableRefList(refs, allowed, objects, key, objProp)...)
					}
				}
			}
		}
	}
	return errs
}

func mustArtifactMIMEType(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	if typ != "artifact" {
		return nil
	}
	mime, ok := obj["mime_type"].(string)
	if !ok {
		return nil
	}
	if stix.IsValidMIMEType(mime) {
		return nil
	}
	id, _ := obj["id"].(string)
	return []ValidationError{{Path: "mime_type", Message: "The 'mime_type' property of object '" + id + "' ('" + mime + "') must be an IANA registered MIME Type of the form 'type/subtype'."}}
}

func mustCharacterSet(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	var errs []ValidationError
	id, _ := obj["id"].(string)
	if typ == "directory" {
		if v, ok := obj["path_enc"].(string); ok {
			if !stix.IsValidCharset(v) {
				errs = append(errs, ValidationError{Path: "path_enc", Message: "The 'path_enc' property of object '" + id + "' ('" + v + "') must be an IANA registered character set."})
			}
		}
	}
	if typ == "file" {
		if v, ok := obj["name_enc"].(string); ok {
			if !stix.IsValidCharset(v) {
				errs = append(errs, ValidationError{Path: "name_enc", Message: "The 'name_enc' property of object '" + id + "' ('" + v + "') must be an IANA registered character set."})
			}
		}
	}
	return errs
}

func mustLanguage(obj map[string]interface{}, _ string, _ *MustOptions) []ValidationError {
	lang, ok := obj["lang"].(string)
	if !ok {
		return nil
	}
	if sliceContains(LANG_CODES, lang) {
		return nil
	}
	return []ValidationError{{Path: "lang", Message: "'" + lang + "' is not a valid RFC 5646 language code."}}
}

func mustSoftwareLanguage(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	if typ != "software" {
		return nil
	}
	langs, ok := obj["languages"].([]interface{})
	if !ok {
		return nil
	}
	var errs []ValidationError
	id, _ := obj["id"].(string)
	for _, l := range langs {
		lang, ok := l.(string)
		if !ok {
			continue
		}
		if sliceContains(LANG_CODES, lang) || sliceContains(SOFTWARE_LANG_CODES, lang) {
			continue
		}
		errs = append(errs, ValidationError{Path: "languages", Message: "The 'languages' property of object '" + id + "' contains an invalid language code ('" + lang + "'). Expected RFC 5646 language code."})
	}
	return errs
}

func mustPatterns(obj map[string]interface{}, typ string, opts *MustOptions) []ValidationError {
	if typ != "indicator" {
		return nil
	}
	patternType, _ := obj["pattern_type"].(string)
	if patternType != "stix" {
		return nil
	}
	pattern, ok := obj["pattern"].(string)
	if !ok {
		return nil
	}
	var errs []ValidationError
	msgs, err := stixpattern.ValidatePattern(pattern)
	if err != nil {
		return []ValidationError{{Path: "pattern", Message: err.Error()}}
	}
	for _, msg := range msgs {
		errs = append(errs, ValidationError{Path: "pattern", Message: msg})
	}
	if len(errs) > 0 {
		return errs
	}
	inspection, err := stixpattern.InspectPattern(pattern)
	if err != nil {
		return []ValidationError{{Path: "pattern", Message: err.Error()}}
	}
	for objType, pathList := range inspection {
		if sliceContains(OBSERVABLE_TYPES, objType) {
			continue
		}
		if !typeFormatRE.MatchString(objType) || len(objType) < 3 || len(objType) > 250 {
			errs = append(errs, ValidationError{Path: "pattern", Message: "'" + objType + "' is not a valid observable type name"})
			continue
		}
		extDisabled := sliceContains(opts.Disabled, "extensions-use")
		if !disabled(opts, "all", "format-checks", "custom-prefix") && extDisabled && !customTypePrefix.MatchString(objType) {
			errs = append(errs, ValidationError{Path: "pattern", Message: "Custom Observable Object type '" + objType + "' should start with 'x-' followed by a source unique identifier (like a domain name with dots replaced by hyphens), a hyphen and then the name"})
		} else if !disabled(opts, "all", "format-checks", "custom-prefix-lax") && extDisabled && !customTypeLax.MatchString(objType) {
			errs = append(errs, ValidationError{Path: "pattern", Message: "Custom Observable Object type '" + objType + "' should start with 'x-'"})
		}
		for _, pathPart := range pathList {
			propStr := pathPart
			if idx := strings.Index(pathPart, "."); idx >= 0 {
				propStr = pathPart[:idx]
			}
			if idx := strings.Index(propStr, "["); idx >= 0 {
				propStr = propStr[:idx]
			}
			if props, ok := OBSERVABLE_PROPERTIES[objType]; ok && sliceContains(props, propStr) {
				continue
			}
			if !propertyFormatRE.MatchString(propStr) {
				errs = append(errs, ValidationError{Path: "pattern", Message: "'" + propStr + "' is not a valid observable property name"})
				continue
			}
			if !sliceContains(OBSERVABLE_TYPES, objType) {
				continue
			}
			extDisabled := sliceContains(opts.Disabled, "extensions-use")
			if !disabled(opts, "all", "format-checks", "custom-prefix") && extDisabled && !customPropPrefix.MatchString(propStr) {
				errs = append(errs, ValidationError{Path: "pattern", Message: "Cyber Observable Object custom property '" + propStr + "' should start with 'x_' followed by a source unique identifier"})
			} else if !disabled(opts, "all", "format-checks", "custom-prefix-lax") && extDisabled && !customPropLax.MatchString(propStr) {
				errs = append(errs, ValidationError{Path: "pattern", Message: "Cyber Observable Object custom property '" + propStr + "' should start with 'x_'"})
			}
		}
	}
	return errs
}

func mustCPECheck(obj map[string]interface{}, _ string, _ *MustOptions) []ValidationError {
	cpe, ok := obj["cpe"].(string)
	if !ok {
		return nil
	}
	if !cpe23RE.MatchString(cpe) {
		return []ValidationError{{Path: "cpe", Message: "Provided CPE value '" + cpe + "' is not CPE v2.3 compliant."}}
	}
	return nil
}

func mustLanguageContents(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	if typ != "language-content" {
		return nil
	}
	contents, ok := obj["contents"].(map[string]interface{})
	if !ok {
		return nil
	}
	var errs []ValidationError
	for key, value := range contents {
		if !sliceContains(LANG_CODES, key) {
			errs = append(errs, ValidationError{Path: "contents", Message: "Invalid key '" + key + "' in 'contents' property must be an RFC 5646 code"})
		}
		subMap, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		for subkey := range subMap {
			if !propertyFormatRE.MatchString(subkey) {
				errs = append(errs, ValidationError{Path: "contents", Message: "'" + subkey + "' in '" + key + "' of the 'contents' property is invalid and must match a valid property name"})
			}
		}
	}
	return errs
}

func uuidVersion(id string) int {
	parts := strings.Split(id, "--")
	if len(parts) != 2 {
		return -1
	}
	hex := parts[1]
	if len(hex) != 36 {
		return -1
	}
	if len(hex) >= 15 && hex[14] == '4' {
		return 4
	}
	return -1
}

func mustUUIDVersionCheck(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	id, ok := obj["id"].(string)
	if !ok {
		return nil
	}
	switch typ {
	case "artifact":
		if hasAny(obj, "hashes", "payload_bin") {
			return nil
		}
	case "email-message":
		if hasAny(obj, "from_ref", "subject", "body") {
			return nil
		}
	case "user-account":
		if hasAny(obj, "account_type", "user_id", "account_login") {
			return nil
		}
	case "windows-registry-key":
		if hasAny(obj, "key", "values") {
			return nil
		}
	case "x509-certificate":
		if hasAny(obj, "hashes", "serial_number") {
			return nil
		}
	default:
		return nil
	}
	if uuidVersion(id) != 4 {
		return []ValidationError{{Path: "id", Message: "If no Contributing Properties are present, a UUIDv4 must be used"}}
	}
	return nil
}

func hasAny(obj map[string]interface{}, keys ...string) bool {
	for _, k := range keys {
		if _, ok := obj[k]; ok {
			return true
		}
	}
	return false
}

func mustProcess(obj map[string]interface{}, typ string, _ *MustOptions) []ValidationError {
	if typ != "process" {
		return nil
	}
	id, ok := obj["id"].(string)
	if !ok {
		return nil
	}
	if uuidVersion(id) != 4 {
		return []ValidationError{{Path: "id", Message: "A process object must use UUIDv4 for its id"}}
	}
	return nil
}

func sliceContains(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}
