// Package ocsf provides OCSF-to-STIX mapping and a builder that converts OCSF events
// into STIX 2.1 observed-data bundles for use with the matcher.
package ocsf

import (
	"strings"
)

// STIXMapping describes how an OCSF observable field or type maps to a STIX SCO.
type STIXMapping struct {
	Type     string // STIX SCO type, e.g. "file", "ipv4-addr"
	Property string // STIX property path, e.g. "name", "value", "hashes.MD5"
}

// PatternPath returns the matcher pattern path: Type + ":" + Property (e.g. "file:name").
func (m STIXMapping) PatternPath() string {
	return m.Type + ":" + m.Property
}

// DefaultMapping is used when no specific mapping exists so no observable data is lost.
// Unmapped observables become artifact SCOs with extensions.ocsf metadata.
var DefaultMapping = STIXMapping{
	Type:     "artifact",
	Property: "extensions.ocsf.value",
}

// Curated field-name overrides: strong STIX mappings for well-known OCSF paths.
// Generator output is merged in init(); these take precedence.
var curatedFieldOverrides = map[string]STIXMapping{
	"file.name":            {Type: "file", Property: "name"},
	"file.path":            {Type: "file", Property: "name"},
	"file.size":            {Type: "file", Property: "size"},
	"file.hashes":          {Type: "file", Property: "hashes.MD5"},
	"file.mime_type":       {Type: "file", Property: "mime_type"},
	"device.name":          {Type: "software", Property: "name"},
	"device.hostname":      {Type: "domain-name", Property: "value"},
	"device.ip":            {Type: "ipv4-addr", Property: "value"},
	"device.mac":           {Type: "mac-addr", Property: "value"},
	"src_ip":               {Type: "ipv4-addr", Property: "value"},
	"dst_ip":               {Type: "ipv4-addr", Property: "value"},
	"src_endpoint.ip":      {Type: "ipv4-addr", Property: "value"},
	"dst_endpoint.ip":      {Type: "ipv4-addr", Property: "value"},
	"hostname":             {Type: "domain-name", Property: "value"},
	"domain":               {Type: "domain-name", Property: "value"},
	"url":                  {Type: "url", Property: "value"},
	"url.url":              {Type: "url", Property: "value"},
	"process.name":         {Type: "process", Property: "name"},
	"process.pid":          {Type: "process", Property: "pid"},
	"process.command_line": {Type: "process", Property: "command_line"},
	"process.executable":   {Type: "process", Property: "name"},
	"actor.user.name":      {Type: "user-account", Property: "account_login"},
	"actor.user.uid":       {Type: "user-account", Property: "user_id"},
	"actor.email":          {Type: "email-addr", Property: "value"},
	"user.name":            {Type: "user-account", Property: "user_id"},
	"user.uid":             {Type: "user-account", Property: "user_id"},
	"email.from":           {Type: "email-addr", Property: "value"},
	"email.to":             {Type: "email-addr", Property: "value"},
	"registry_key":         {Type: "windows-registry-key", Property: "key"},
	"registry_value":       {Type: "windows-registry-key", Property: "values.data"},
	"resource.name":        {Type: "software", Property: "name"},
	"geo.location":         {Type: "location", Property: "region"},
	"country":              {Type: "location", Property: "country"},
}

// ocsfTypeIDToSTIX maps OCSF observable type_id to STIX mapping (22 mapped; 0,10,17,18,20,27,99 omitted).
var ocsfTypeIDToSTIX = map[int]STIXMapping{
	1:  {Type: "domain-name", Property: "value"},
	2:  {Type: "ipv4-addr", Property: "value"},
	3:  {Type: "mac-addr", Property: "value"},
	4:  {Type: "user-account", Property: "user_id"},
	5:  {Type: "email-addr", Property: "value"},
	6:  {Type: "url", Property: "value"},
	7:  {Type: "file", Property: "name"},
	8:  {Type: "file", Property: "hashes.MD5"},
	9:  {Type: "process", Property: "name"},
	11: {Type: "network-traffic", Property: "dst_port"},
	12: {Type: "ipv4-addr", Property: "value"},
	13: {Type: "process", Property: "command_line"},
	14: {Type: "location", Property: "country"},
	15: {Type: "process", Property: "pid"},
	16: {Type: "network-traffic", Property: "extensions.http-request-ext.request_header.User-Agent"},
	19: {Type: "user-account", Property: "user_id"},
	21: {Type: "user-account", Property: "user_id"},
	22: {Type: "email-addr", Property: "value"},
	23: {Type: "url", Property: "value"},
	24: {Type: "file", Property: "name"},
	25: {Type: "process", Property: "name"},
	26: {Type: "location", Property: "latitude"},
	28: {Type: "windows-registry-key", Property: "key"},
	29: {Type: "windows-registry-key", Property: "values.data"},
	30: {Type: "x509-certificate", Property: "hashes.SHA-256"},
}

// ocsfTypeNameToSTIX maps OCSF observable type name (string) to STIX mapping.
var ocsfTypeNameToSTIX = map[string]STIXMapping{
	"Hostname":                 {Type: "domain-name", Property: "value"},
	"IP Address":               {Type: "ipv4-addr", Property: "value"},
	"MAC Address":              {Type: "mac-addr", Property: "value"},
	"User Name":                {Type: "user-account", Property: "user_id"},
	"Email Address":            {Type: "email-addr", Property: "value"},
	"URL String":               {Type: "url", Property: "value"},
	"File Name":                {Type: "file", Property: "name"},
	"Hash":                     {Type: "file", Property: "hashes.MD5"},
	"Process Name":             {Type: "process", Property: "name"},
	"Port":                     {Type: "network-traffic", Property: "dst_port"},
	"Subnet":                   {Type: "ipv4-addr", Property: "value"},
	"Command Line":             {Type: "process", Property: "command_line"},
	"Country":                  {Type: "location", Property: "country"},
	"Process ID":               {Type: "process", Property: "pid"},
	"HTTP User-Agent":          {Type: "network-traffic", Property: "extensions.http-request-ext.request_header.User-Agent"},
	"User Credential ID":       {Type: "user-account", Property: "user_id"},
	"User":                     {Type: "user-account", Property: "user_id"},
	"Email":                    {Type: "email-addr", Property: "value"},
	"Uniform Resource Locator": {Type: "url", Property: "value"},
	"File":                     {Type: "file", Property: "name"},
	"Process":                  {Type: "process", Property: "name"},
	"Geo Location":             {Type: "location", Property: "latitude"},
	"Registry Key":             {Type: "windows-registry-key", Property: "key"},
	"Registry Value":           {Type: "windows-registry-key", Property: "values.data"},
	"Fingerprint":              {Type: "x509-certificate", Property: "hashes.SHA-256"},
}

// ocsfFieldToSTIX is the merged map (generated + curated overrides). Populated in init().
var ocsfFieldToSTIX map[string]STIXMapping

func init() {
	ocsfFieldToSTIX = make(map[string]STIXMapping)
	for k, v := range ocsfFieldToSTIXGenerated {
		ocsfFieldToSTIX[k] = v
	}
	for k, v := range curatedFieldOverrides {
		ocsfFieldToSTIX[k] = v
	}
}

// ByFieldName returns the STIX mapping for an OCSF observable name (e.g. "file.name", "device.ip").
// If the name is not in the map, returns DefaultMapping and true so callers never drop data.
func ByFieldName(name string) (STIXMapping, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return STIXMapping{}, false
	}
	if m, ok := ocsfFieldToSTIX[name]; ok {
		return m, true
	}
	return DefaultMapping, true
}

// ByObservableTypeID returns the STIX mapping for an OCSF observable type_id (1-30, etc.).
// Returns (zero, false) for type_ids with no typed mapping (0, 10, 17, 18, 20, 27, 99);
// caller should use DefaultMapping.
func ByObservableTypeID(typeID int) (STIXMapping, bool) {
	m, ok := ocsfTypeIDToSTIX[typeID]
	return m, ok
}

// ByObservableTypeName returns the STIX mapping for an OCSF observable type string (e.g. "Hostname", "IP Address").
func ByObservableTypeName(typeName string) (STIXMapping, bool) {
	typeName = strings.TrimSpace(typeName)
	if typeName == "" {
		return STIXMapping{}, false
	}
	// Normalize to title case for schema alignment
	key := typeName
	if m, ok := ocsfTypeNameToSTIX[key]; ok {
		return m, true
	}
	return STIXMapping{}, false
}
