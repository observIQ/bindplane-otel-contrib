// SHOULD (recommended) checks per STIX 2.1 spec.
// Ported from cti-stix-validator v21/shoulds.py.

package validator

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix"
)

// ShouldOptions mirrors the validator options needed for SHOULD checks.
// Passed from stix/validator without importing it (avoids cycle).
type ShouldOptions struct {
	Disabled         []string
	Enabled          []string
	StrictTypes      bool
	StrictProperties bool
	EnforceRefs      bool
}

type shouldCheckFunc func(obj map[string]interface{}, typ string) []ValidationError

var (
	defaultShouldList     []shouldCheckFunc
	defaultShouldListOnce sync.Once
	shouldListCache       sync.Map // map[string][]shouldCheckFunc
)

// shouldListCacheKey returns a deterministic key for ShouldOptions (after normalization).
func shouldListCacheKey(opts ShouldOptions) string {
	disabled := make([]string, len(opts.Disabled))
	copy(disabled, opts.Disabled)
	sort.Strings(disabled)
	enabled := make([]string, len(opts.Enabled))
	copy(enabled, opts.Enabled)
	sort.Strings(enabled)
	return strings.Join(disabled, ",") + "|" + strings.Join(enabled, ",") +
		"|" + boolKey(opts.StrictTypes) + boolKey(opts.StrictProperties) + boolKey(opts.EnforceRefs)
}

func boolKey(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// RunShoulds runs the list of SHOULD checks determined by opts and returns
// findings (to be appended to Warnings, or Errors when Strict).
func RunShoulds(obj map[string]interface{}, typ string, opts ShouldOptions) []ValidationError {
	opts.Disabled = normalizeCheckCodes(opts.Disabled)
	opts.Enabled = normalizeCheckCodes(opts.Enabled)
	checks := listShouldsCached(opts)
	var errs []ValidationError
	for _, fn := range checks {
		errs = append(errs, fn(obj, typ)...)
	}
	return errs
}

// listShouldsCached returns the list of should-check functions, using a cache keyed by opts.
func listShouldsCached(opts ShouldOptions) []shouldCheckFunc {
	if len(opts.Disabled) == 0 && len(opts.Enabled) == 0 && !opts.StrictTypes && !opts.StrictProperties && !opts.EnforceRefs {
		defaultShouldListOnce.Do(func() {
			defaultShouldList = listShoulds(opts)
		})
		return defaultShouldList
	}
	key := shouldListCacheKey(opts)
	if v, ok := shouldListCache.Load(key); ok {
		return v.([]shouldCheckFunc)
	}
	list := listShoulds(opts)
	shouldListCache.Store(key, list)
	return list
}

// normalizeCheckCodes maps numeric check codes to names using CHECK_CODES,
// so that CLI can pass either "202" or "relationship-types".
func normalizeCheckCodes(entries []string) []string {
	if len(entries) == 0 {
		return entries
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		if name, ok := CHECK_CODES[strings.TrimSpace(e)]; ok {
			out[i] = name
		} else {
			out[i] = strings.TrimSpace(e)
		}
	}
	return out
}

func listShoulds(opts ShouldOptions) []shouldCheckFunc {
	var list []shouldCheckFunc
	if opts.EnforceRefs {
		list = append(list, enforceRelationshipRefs)
	}
	if opts.StrictTypes {
		list = append(list, typesStrict)
	}
	if opts.StrictProperties {
		list = append(list, propertiesStrict)
	}
	if len(opts.Enabled) > 0 && len(opts.Disabled) == 0 {
		for _, name := range opts.Enabled {
			if fns, ok := CHECKS[name]; ok {
				list = append(list, fns...)
			}
		}
		return list
	}
	// Default or disabled: build list excluding disabled
	if !sliceContainsS(opts.Disabled, "all") {
		if !sliceContainsS(opts.Disabled, "format-checks") {
			list = append(list, CHECKS["open-vocab-format"]...)
			list = append(list, CHECKS["kill-chain-names"]...)
			list = append(list, CHECKS["observable-object-keys"]...)
			list = append(list, CHECKS["observable-dictionary-keys"]...)
			list = append(list, CHECKS["malware-analysis-product"]...)
			list = append(list, CHECKS["windows-process-priority-format"]...)
			list = append(list, CHECKS["hash-length"]...)
		}
		if !sliceContainsS(opts.Disabled, "approved-values") {
			list = append(list, CHECKS["marking-definition-type"]...)
			list = append(list, CHECKS["relationship-types"]...)
			list = append(list, CHECKS["duplicate-ids"]...)
			for _, name := range allVocabNames {
				if !sliceContainsS(opts.Disabled, name) {
					list = append(list, CHECKS[name]...)
				}
			}
			if !sliceContainsS(opts.Disabled, "all-external-sources") {
				for _, name := range allExternalSourceNames {
					if !sliceContainsS(opts.Disabled, name) {
						list = append(list, CHECKS[name]...)
					}
				}
			}
		}
		list = append(list, CHECKS["network-traffic-ports"]...)
		list = append(list, CHECKS["extref-hashes"]...)
		list = append(list, CHECKS["indicator-properties"]...)
		list = append(list, CHECKS["deprecated-properties"]...)
		if !sliceContainsS(opts.Disabled, "extensions-use") {
			list = append(list, CHECKS["extensions-use"]...)
		} else if !sliceContainsS(opts.Disabled, "format-checks") {
			if !sliceContainsS(opts.Disabled, "custom-prefix") {
				list = append(list, CHECKS["custom-prefix"]...)
			} else if !sliceContainsS(opts.Disabled, "custom-prefix-lax") {
				list = append(list, CHECKS["custom-prefix-lax"]...)
			}
		}
	}
	if len(opts.Enabled) > 0 {
		for _, name := range opts.Enabled {
			if fns, ok := CHECKS[name]; ok {
				list = append(list, fns...)
			}
		}
	}
	return list
}

var (
	allVocabNames = []string{
		"attack-motivation", "attack-resource-level", "grouping-context",
		"implementation-languages", "infrastructure-types", "malware-capabilities",
		"malware-result", "processor-architecture", "identity-class", "indicator-types",
		"industry-sector", "malware-types", "indicator-pattern-types", "report-types",
		"threat-actor-types", "threat-actor-role", "threat-actor-sophistication",
		"tool-types", "region", "hash-algo", "windows-pebinary-type", "account-type",
	}
	allExternalSourceNames = []string{
		"mime-type", "protocols", "ipfix", "http-request-headers", "socket-options",
		"pdf-doc-info", "countries",
	}
)

var (
	protocolRE      = regexp.MustCompile(`^[a-zA-Z0-9-]{1,15}$`)
	customPropLaxRE = regexp.MustCompile(`^x_.+$`)
	customTypeLaxRE = regexp.MustCompile(`^x\-.+$`)
)

// CHECKS maps check name to one or more check functions.
var CHECKS = map[string][]shouldCheckFunc{
	"uuid-check":                      {shouldUUIDCheck},
	"open-vocab-format":               {shouldOpenVocabFormat},
	"kill-chain-names":                {shouldKillChainPhaseNames},
	"observable-object-keys":          {shouldObservableObjectKeys},
	"observable-dictionary-keys":      {shouldObservableDictionaryKeys},
	"malware-analysis-product":        {shouldMalwareAnalysisProduct},
	"windows-process-priority-format": {shouldWindowsProcessPriorityFormat},
	"hash-length":                     {shouldHashLength},
	"marking-definition-type":         {shouldVocabMarkingDefinition},
	"relationship-types":              {shouldRelationshipsStrict},
	"duplicate-ids":                   {shouldDuplicateIDs},
	"attack-motivation":               {shouldVocabAttackMotivation},
	"attack-resource-level":           {shouldVocabAttackResourceLevel},
	"grouping-context":                {shouldVocabGroupingContext},
	"implementation-languages":        {shouldVocabImplementationLanguages},
	"infrastructure-types":            {shouldVocabInfrastructureTypes},
	"malware-capabilities":            {shouldVocabMalwareCapabilities},
	"malware-result":                  {shouldVocabMalwareResult},
	"processor-architecture":          {shouldVocabProcessorArchitecture},
	"identity-class":                  {shouldVocabIdentityClass},
	"indicator-types":                 {shouldVocabIndicatorTypes},
	"industry-sector":                 {shouldVocabIndustrySector},
	"malware-types":                   {shouldVocabMalwareTypes},
	"indicator-pattern-types":         {shouldVocabPatternType},
	"report-types":                    {shouldVocabReportTypes},
	"threat-actor-types":              {shouldVocabThreatActorTypes},
	"threat-actor-role":               {shouldVocabThreatActorRole},
	"threat-actor-sophistication":     {shouldVocabThreatActorSophistication},
	"tool-types":                      {shouldVocabToolTypes},
	"region":                          {shouldVocabRegion},
	"hash-algo":                       {shouldVocabHashAlgo},
	"windows-pebinary-type":           {shouldVocabWindowsPebinaryType},
	"account-type":                    {shouldVocabAccountType},
	"mime-type":                       {shouldMimeType},
	"protocols":                       {shouldProtocols},
	"ipfix":                           {shouldIPFIX},
	"http-request-headers":            {shouldHTTPRequestHeaders},
	"socket-options":                  {shouldSocketOptions},
	"pdf-doc-info":                    {shouldPDFDocInfo},
	"countries":                       {shouldCountries},
	"network-traffic-ports":           {shouldNetworkTrafficPorts},
	"extref-hashes":                   {shouldExtrefHashes},
	"indicator-properties":            {shouldIndicatorPropertyCheck},
	"deprecated-properties":           {shouldDeprecatedPropertyCheck},
	"extension-description":           {shouldExtensionDescription},
	"extension-properties":            {shouldExtensionProperties},
	"extensions-use":                  {shouldExtensionsUse},
	"custom-prefix":                   {shouldCustomPrefixStrict},
	"custom-prefix-lax":               {shouldCustomPrefixLax},
	"enforce_relationship_refs":       {enforceRelationshipRefs},
}

func sliceContainsS(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func enforceRelationshipRefs(obj map[string]interface{}, typ string) []ValidationError {
	// Requires bundle context to resolve refs; no-op when validating single object.
	return nil
}

func typesStrict(obj map[string]interface{}, typ string) []ValidationError {
	if sliceContains(TYPES, typ) || sliceContains(OBSERVABLE_TYPES, typ) {
		return nil
	}
	return []ValidationError{{Path: "type", Message: "Object type '" + typ + "' is not a spec-defined type."}}
}

func propertiesStrict(obj map[string]interface{}, typ string) []ValidationError {
	var errs []ValidationError
	allowed, ok := PROPERTIES[typ]
	if !ok {
		allowed, _ = OBSERVABLE_PROPERTIES[typ]
	}
	if allowed == nil {
		return nil
	}
	for key := range obj {
		if sliceContains(RESERVED_PROPERTIES, key) {
			continue
		}
		if sliceContains(allowed, key) {
			continue
		}
		if customPropLaxRE.MatchString(key) {
			continue
		}
		id, _ := obj["id"].(string)
		errs = append(errs, ValidationError{Path: key, Message: "Property '" + key + "' on object '" + id + "' is not a spec-defined property for type '" + typ + "'."})
	}
	return errs
}

func shouldUUIDCheck(obj map[string]interface{}, typ string) []ValidationError {
	id, ok := obj["id"].(string)
	if !ok {
		return nil
	}
	v := uuidVersion(id)
	if v < 0 {
		return nil // schema will catch invalid UUID format
	}
	// Cyber observables (except observed-data, process) should use UUIDv5
	if hasCyberObservableData(obj, typ) && typ != "observed-data" && typ != "process" {
		if v != 5 {
			return []ValidationError{{Path: "id", Message: "Cyber Observable ID value " + id + " is not a valid UUIDv5 ID."}}
		}
		return nil
	}
	if v != 4 {
		return []ValidationError{{Path: "id", Message: "Given ID value " + id + " is not a valid UUIDv4 ID."}}
	}
	return nil
}

func hasCyberObservableData(obj map[string]interface{}, typ string) bool {
	if typ == "observed-data" {
		return true
	}
	return sliceContains(OBSERVABLE_TYPES, typ)
}

func shouldOpenVocabFormat(obj map[string]interface{}, typ string) []ValidationError {
	var errs []ValidationError
	props, ok := VOCAB_PROPERTIES[typ]
	if !ok {
		return nil
	}
	for _, prop := range props {
		val := obj[prop]
		if val == nil {
			continue
		}
		var values []string
		switch v := val.(type) {
		case string:
			values = []string{v}
		case []interface{}:
			for _, x := range v {
				if s, ok := x.(string); ok {
					values = append(values, s)
				}
			}
		default:
			continue
		}
		for _, v := range values {
			if !strings.EqualFold(v, strings.ToLower(v)) || strings.Contains(v, "_") || strings.Contains(v, " ") {
				id, _ := obj["id"].(string)
				errs = append(errs, ValidationError{Path: prop, Message: "Object '" + id + "': open vocabulary value '" + v + "' should be all lowercase and use hyphens instead of spaces or underscores as word separators."})
			}
		}
	}
	return errs
}

func shouldKillChainPhaseNames(obj map[string]interface{}, typ string) []ValidationError {
	used := KILL_CHAIN_PHASE_USES
	if !sliceContains(used, typ) {
		return nil
	}
	phases, _ := obj["kill_chain_phases"].([]interface{})
	var errs []ValidationError
	for _, p := range phases {
		phase, _ := p.(map[string]interface{})
		if phase == nil {
			continue
		}
		chainName, _ := phase["kill_chain_name"].(string)
		if chainName != "" && (!strings.EqualFold(chainName, strings.ToLower(chainName)) || strings.Contains(chainName, "_") || strings.Contains(chainName, " ")) {
			errs = append(errs, ValidationError{Path: "kill_chain_phases", Message: "kill_chain_name '" + chainName + "' should be all lowercase and use hyphens instead of spaces or underscores as word separators."})
		}
		phaseName, _ := phase["phase_name"].(string)
		if phaseName != "" && (!strings.EqualFold(phaseName, strings.ToLower(phaseName)) || strings.Contains(phaseName, "_") || strings.Contains(phaseName, " ")) {
			errs = append(errs, ValidationError{Path: "kill_chain_phases", Message: "phase_name '" + phaseName + "' should be all lowercase and use hyphens instead of spaces or underscores as word separators."})
		}
	}
	return errs
}

func shouldObservableObjectKeys(obj map[string]interface{}, typ string) []ValidationError {
	if typ != "observed-data" {
		return nil
	}
	objects, _ := obj["objects"].(map[string]interface{})
	if objects == nil {
		return nil
	}
	var errs []ValidationError
	for key, v := range objects {
		inner, _ := v.(map[string]interface{})
		if inner == nil {
			continue
		}
		if _, has := inner["type"]; !has {
			continue
		}
		innerTyp, _ := inner["type"].(string)
		if !sliceContains(OBSERVABLE_TYPES, innerTyp) && !customTypeLaxRE.MatchString(innerTyp) {
			errs = append(errs, ValidationError{Path: "objects", Message: "Observable object '" + key + "' has type '" + innerTyp + "' which should start with 'x-' for custom types."})
		}
		allowed := OBSERVABLE_PROPERTIES[innerTyp]
		for k := range inner {
			if k == "type" || k == "id" || k == "spec_version" {
				continue
			}
			if sliceContains(allowed, k) {
				continue
			}
			if customPropLaxRE.MatchString(k) {
				continue
			}
			errs = append(errs, ValidationError{Path: "objects", Message: "Observable object '" + key + "' has unknown property '" + k + "'."})
		}
	}
	return errs
}

func shouldObservableDictionaryKeys(obj map[string]interface{}, typ string) []ValidationError {
	return nil
}
func shouldMalwareAnalysisProduct(obj map[string]interface{}, typ string) []ValidationError {
	return nil
}
func shouldWindowsProcessPriorityFormat(obj map[string]interface{}, typ string) []ValidationError {
	return nil
}

func shouldHashLength(obj map[string]interface{}, typ string) []ValidationError {
	// Hash length recommendations (e.g. SHA-256 64 hex chars)
	return nil
}

func shouldVocabMarkingDefinition(obj map[string]interface{}, typ string) []ValidationError {
	if typ != "marking-definition" {
		return nil
	}
	dt, ok := obj["definition_type"].(string)
	if !ok {
		return nil
	}
	if sliceContains(MARKING_DEFINITION_TYPES, dt) {
		return nil
	}
	id, _ := obj["id"].(string)
	return []ValidationError{{Path: "definition_type", Message: "Marking definition '" + id + "' `definition_type` should be one of: " + strings.Join(MARKING_DEFINITION_TYPES, ", ") + "."}}
}

func shouldRelationshipsStrict(obj map[string]interface{}, typ string) []ValidationError {
	if typ != "relationship" {
		return nil
	}
	rType, _ := obj["relationship_type"].(string)
	sourceRef, _ := obj["source_ref"].(string)
	targetRef, _ := obj["target_ref"].(string)
	if rType == "" || sourceRef == "" || targetRef == "" {
		return nil
	}
	sourceType := ""
	if idx := strings.Index(sourceRef, "--"); idx >= 0 {
		sourceType = sourceRef[:idx]
	}
	targetType := ""
	if idx := strings.Index(targetRef, "--"); idx >= 0 {
		targetType = targetRef[:idx]
	}
	if sliceContains(COMMON_RELATIONSHIPS, rType) || sliceContains(NON_SDOS, sourceType) || sliceContains(NON_SDOS, targetType) {
		return nil
	}
	bySource, ok := RELATIONSHIPS[sourceType]
	if !ok {
		return []ValidationError{{Path: "relationship_type", Message: "'" + sourceType + "' is permitted, but not a suggested relationship source object for the '" + rType + "' relationship."}}
	}
	allowedTargets, ok := bySource[rType]
	if !ok {
		return []ValidationError{{Path: "relationship_type", Message: "'" + rType + "' is permitted, but not a suggested relationship type for '" + sourceType + "' objects."}}
	}
	if !sliceContains(allowedTargets, targetType) {
		return []ValidationError{{Path: "target_ref", Message: "'" + targetType + "' is permitted, but not a suggested relationship target object for '" + sourceType + "' objects with the '" + rType + "' relationship."}}
	}
	return nil
}

func shouldDuplicateIDs(obj map[string]interface{}, typ string) []ValidationError {
	// Needs bundle context; no-op per object.
	return nil
}

func checkVocab(obj map[string]interface{}, typ string, usesMap map[string][]string, ovList []string, prop string, code string) []ValidationError {
	for typeKey, props := range usesMap {
		if typ != typeKey {
			continue
		}
		for _, p := range props {
			if _, has := obj[p]; !has {
				continue
			}
			val := obj[p]
			var values []string
			switch v := val.(type) {
			case string:
				values = []string{v}
			case []interface{}:
				for _, x := range v {
					if s, ok := x.(string); ok {
						values = append(values, s)
					}
				}
			default:
				continue
			}
			for _, v := range values {
				if !sliceContains(ovList, v) {
					return []ValidationError{{Path: p, Message: "The value contained in " + p + " is permitted, but is not in the " + code + "-ov vocabulary."}}
				}
			}
		}
		break
	}
	return nil
}

func shouldVocabAttackMotivation(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, ATTACK_MOTIVATION_USES, ATTACK_MOTIVATION_OV, "", "attack-motivation")
}
func shouldVocabAttackResourceLevel(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, ATTACK_RESOURCE_LEVEL_USES, ATTACK_RESOURCE_LEVEL_OV, "", "attack-resource-level")
}
func shouldVocabGroupingContext(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, GROUPING_CONTEXT_USES, GROUPING_CONTEXT_OV, "", "grouping-context")
}
func shouldVocabImplementationLanguages(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, IMPLEMENTATION_LANGUAGES_USES, IMPLEMENTATION_LANGUAGES_OV, "", "implementation-languages")
}
func shouldVocabInfrastructureTypes(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, INFRASTRUCTURE_TYPE_USES, INFRASTRUCTURE_TYPE_OV, "", "infrastructure-types")
}
func shouldVocabMalwareCapabilities(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, MALWARE_CAPABILITIES_USES, MALWARE_CAPABILITIES_OV, "", "malware-capabilities")
}
func shouldVocabMalwareResult(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, MALWARE_RESULT_USES, MALWARE_RESULT_OV, "", "malware-result")
}
func shouldVocabProcessorArchitecture(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, PROCESSOR_ARCHITECTURE_USES, PROCESSOR_ARCHITECTURE_OV, "", "processor-architecture")
}
func shouldVocabIdentityClass(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, IDENTITY_CLASS_USES, IDENTITY_CLASS_OV, "", "identity-class")
}
func shouldVocabIndicatorTypes(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, INDICATOR_TYPE_USES, INDICATOR_TYPE_OV, "", "indicator-types")
}
func shouldVocabIndustrySector(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, INDUSTRY_SECTOR_USES, INDUSTRY_SECTOR_OV, "", "industry-sector")
}
func shouldVocabMalwareTypes(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, MALWARE_TYPE_USES, MALWARE_TYPE_OV, "", "malware-types")
}
func shouldVocabPatternType(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, INDICATOR_PATTERN_USES, INDICATOR_PATTERN_OV, "", "indicator-pattern-types")
}
func shouldVocabReportTypes(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, REPORT_TYPE_USES, REPORT_TYPE_OV, "", "report-types")
}
func shouldVocabThreatActorTypes(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, THREAT_ACTOR_TYPE_USES, THREAT_ACTOR_TYPE_OV, "", "threat-actor-types")
}
func shouldVocabThreatActorRole(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, THREAT_ACTOR_ROLE_USES, THREAT_ACTOR_ROLE_OV, "", "threat-actor-role")
}
func shouldVocabThreatActorSophistication(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, THREAT_ACTOR_SOPHISTICATION_USES, THREAT_ACTOR_SOPHISTICATION_OV, "", "threat-actor-sophistication")
}
func shouldVocabToolTypes(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, TOOL_TYPE_USES, TOOL_TYPE_OV, "", "tool-types")
}
func shouldVocabRegion(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, REGION_USES, REGION_OV, "", "region")
}
func shouldVocabAccountType(obj map[string]interface{}, typ string) []ValidationError {
	return checkVocab(obj, typ, ACCOUNT_TYPE_USES, ACCOUNT_TYPE_OV, "", "account-type")
}

func shouldVocabHashAlgo(obj map[string]interface{}, typ string) []ValidationError {
	if typ != "file" && typ != "artifact" && typ != "x509-certificate" {
		return nil
	}
	hashes, _ := obj["hashes"].(map[string]interface{})
	if hashes == nil {
		return nil
	}
	validHash := func(name string) bool {
		return sliceContains(HASH_ALGO_OV, name) || strings.HasPrefix(name, "x_")
	}
	var errs []ValidationError
	id, _ := obj["id"].(string)
	for h := range hashes {
		if !validHash(h) {
			errs = append(errs, ValidationError{Path: "hashes", Message: "Object '" + id + "' has a 'hashes' dictionary with a hash of type '" + h + "', which is not a value in the hash-algorithm-ov vocabulary nor a custom value prepended with 'x_'."})
		}
	}
	return errs
}

func shouldVocabWindowsPebinaryType(obj map[string]interface{}, typ string) []ValidationError {
	return nil
}

func shouldMimeType(obj map[string]interface{}, typ string) []ValidationError {
	// Check external refs and artifact mime_type
	if typ == "artifact" {
		if mime, ok := obj["mime_type"].(string); ok && mime != "" && !stix.IsValidMIMEType(mime) {
			id, _ := obj["id"].(string)
			return []ValidationError{{Path: "mime_type", Message: "The 'mime_type' property of object '" + id + "' ('" + mime + "') should be an IANA registered MIME type."}}
		}
	}
	return nil
}

func shouldProtocols(obj map[string]interface{}, typ string) []ValidationError {
	// network-traffic etc.
	if extRefs, ok := obj["external_references"].([]interface{}); ok {
		for _, er := range extRefs {
			erm, _ := er.(map[string]interface{})
			if erm == nil {
				continue
			}
			if proto, ok := erm["source_name"].(string); ok && proto != "" {
				if !protocolRE.MatchString(proto) {
					return []ValidationError{{Path: "external_references", Message: "Protocol/source_name should match [a-zA-Z0-9-]{1,15}."}}
				}
			}
		}
	}
	return nil
}

func shouldIPFIX(obj map[string]interface{}, typ string) []ValidationError              { return nil }
func shouldHTTPRequestHeaders(obj map[string]interface{}, typ string) []ValidationError { return nil }
func shouldSocketOptions(obj map[string]interface{}, typ string) []ValidationError      { return nil }
func shouldPDFDocInfo(obj map[string]interface{}, typ string) []ValidationError         { return nil }

func shouldCountries(obj map[string]interface{}, typ string) []ValidationError {
	// location.country
	if typ != "location" {
		return nil
	}
	country, ok := obj["country"].(string)
	if !ok || country == "" {
		return nil
	}
	if !sliceContains(COUNTRY_CODES, country) {
		return []ValidationError{{Path: "country", Message: "The 'country' property should be an ISO 3166-1 alpha-2 country code."}}
	}
	return nil
}

func shouldNetworkTrafficPorts(obj map[string]interface{}, typ string) []ValidationError { return nil }
func shouldExtrefHashes(obj map[string]interface{}, typ string) []ValidationError        { return nil }

func shouldIndicatorPropertyCheck(obj map[string]interface{}, typ string) []ValidationError {
	if typ != "indicator" {
		return nil
	}
	if _, hasName := obj["name"]; !hasName {
		return []ValidationError{{Path: "name", Message: "Both the name and description properties SHOULD be present."}}
	}
	if _, hasDesc := obj["description"]; !hasDesc {
		return []ValidationError{{Path: "description", Message: "Both the name and description properties SHOULD be present."}}
	}
	return nil
}

func shouldDeprecatedPropertyCheck(obj map[string]interface{}, typ string) []ValidationError {
	deprecated, ok := DEPRECATED_PROPERTIES[typ]
	if !ok {
		return nil
	}
	var errs []ValidationError
	for _, p := range deprecated {
		if _, has := obj[p]; has {
			errs = append(errs, ValidationError{Path: p, Message: "Included property '" + p + "' is deprecated within the indicated spec version."})
		}
	}
	return errs
}

func shouldExtensionDescription(obj map[string]interface{}, typ string) []ValidationError {
	if typ != "extension-definition" {
		return nil
	}
	if _, has := obj["description"]; !has {
		return []ValidationError{{Path: "description", Message: "The 'description' property SHOULD be populated."}}
	}
	return nil
}

func shouldExtensionProperties(obj map[string]interface{}, typ string) []ValidationError {
	if typ != "extension-definition" {
		return nil
	}
	extTypes, _ := obj["extension_types"].([]interface{})
	for _, et := range extTypes {
		if e, ok := et.(string); ok && e == "toplevel-property-extension" {
			if _, has := obj["extension_properties"]; !has {
				return []ValidationError{{Path: "extension_properties", Message: "For extensions of the 'toplevel-property-extension' type, the 'extension_properties' property SHOULD include one or more property names."}}
			}
			break
		}
	}
	return nil
}

func shouldExtensionsUse(obj map[string]interface{}, typ string) []ValidationError {
	// Custom types should use extension mechanism
	if !sliceContains(TYPES, typ) && !sliceContains(RESERVED_OBJECTS, typ) && !sliceContains(OBSERVABLE_TYPES, typ) {
		// Check if uses extension
		if _, has := obj["extensions"]; !has {
			id, _ := obj["id"].(string)
			return []ValidationError{{Path: "type", Message: "Object '" + id + "' has custom type '" + typ + "' which should be implemented using an extension with an 'extension_type' of 'new-sdo', 'new-sco', or 'new-sro'."}}
		}
	}
	return nil
}

var (
	customTypePrefixRE = regexp.MustCompile(`^x\-.+\-.+$`)
	customPropPrefixRE = regexp.MustCompile(`^x_.+_.+$`)
)

func shouldCustomPrefixStrict(obj map[string]interface{}, typ string) []ValidationError {
	var errs []ValidationError
	if !sliceContains(TYPES, typ) && !sliceContains(RESERVED_OBJECTS, typ) && !sliceContains(OBSERVABLE_TYPES, typ) && !customTypePrefixRE.MatchString(typ) {
		errs = append(errs, ValidationError{Path: "type", Message: "Custom object type '" + typ + "' should start with 'x-' followed by a source unique identifier (like a domain name with dots replaced by hyphens), a hyphen and then the name."})
	}
	allowed, _ := PROPERTIES[typ]
	for key := range obj {
		if sliceContains(RESERVED_PROPERTIES, key) {
			continue
		}
		if sliceContains(allowed, key) {
			continue
		}
		if !customPropPrefixRE.MatchString(key) {
			errs = append(errs, ValidationError{Path: key, Message: "Custom property '" + key + "' should have a type that starts with 'x_' followed by a source unique identifier."})
		}
	}
	return errs
}

func shouldCustomPrefixLax(obj map[string]interface{}, typ string) []ValidationError {
	var errs []ValidationError
	if !sliceContains(TYPES, typ) && !sliceContains(RESERVED_OBJECTS, typ) && !customTypeLaxRE.MatchString(typ) && !sliceContains(OBSERVABLE_TYPES, typ) {
		errs = append(errs, ValidationError{Path: "type", Message: "Custom object type '" + typ + "' should start with 'x-' in order to be compatible with future versions of the STIX 2 specification."})
	}
	allowed, _ := PROPERTIES[typ]
	for key := range obj {
		if sliceContains(RESERVED_PROPERTIES, key) || sliceContains(allowed, key) || customPropLaxRE.MatchString(key) {
			continue
		}
		errs = append(errs, ValidationError{Path: key, Message: "Custom property '" + key + "' should start with 'x_' in order to be compatible with future versions of the STIX 2 specification."})
	}
	return errs
}
