// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package version reports the version of the opampgateway extension module and
// builds the User-Agent value derived from it.
package version

import (
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"go.opentelemetry.io/collector/component"
)

const (
	// modulePath is the Go module path of the opampgateway extension. The module
	// version is tagged as extension/opampgateway/vX.Y.Z, so the version
	// recorded in build info is the extension's own version.
	modulePath = "github.com/observiq/bindplane-otel-contrib/extension/opampgateway"

	// product is the User-Agent product token for the gateway.
	product = "opamp-gateway"

	// unknown is reported when the module version cannot be determined, which is
	// the case for builds that replace the module with a local directory.
	unknown = "unknown"

	// tchars are the punctuation characters RFC 9110 allows in a token, in
	// addition to letters and digits.
	tchars = "!#$%&'*+-.^_`|~"
)

var moduleVersion = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknown
	}
	return versionOf(info, modulePath)
})

// UserAgent returns the value the gateway sends in the User-Agent header of its
// upstream connections. It follows the RFC 9110 product list convention, naming
// the gateway, then the collector distribution hosting it, then the platform:
//
//	opamp-gateway/v1.9.0 bindplane-otel-collector/v2.0.1-beta.3 (linux/amd64)
//
// The collector product is omitted when the build info does not name it.
func UserAgent(buildInfo component.BuildInfo) string {
	agent := product + "/" + moduleVersion()
	if collector := collectorProduct(buildInfo); collector != "" {
		agent += " " + collector
	}
	return agent + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// collectorProduct renders the collector distribution as a product token.
// BuildInfo.Command is a path in some distributions, so only its base name is
// used. BuildInfo.Description is deliberately not used: it is prose, so it
// cannot be a token.
func collectorProduct(buildInfo component.BuildInfo) string {
	name := token(strings.TrimSuffix(baseName(buildInfo.Command), ".exe"))
	version := token(buildInfo.Version)
	if name == "" || version == "" {
		return ""
	}
	return name + "/" + version
}

// baseName returns the last element of a path. Unlike filepath.Base it treats
// both separators on every platform, because the build info of a binary built
// for one platform can be read on another.
func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// token renders s as an RFC 9110 token, replacing the characters that are not
// allowed in one with a hyphen so that a value carrying spaces or path
// separators cannot split the User-Agent into extra products. It returns an
// empty string for a value that holds nothing usable.
func token(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune(tchars, r):
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	// filepath.Base returns "." or a separator for a path with no base name,
	// both of which trim away to nothing.
	return strings.Trim(b.String(), "-.")
}

// versionOf finds the version recorded for path in info, looking at both the
// main module and its dependencies.
func versionOf(info *debug.BuildInfo, path string) string {
	if info.Main.Path == path {
		return normalize(info.Main.Version)
	}
	for _, dep := range info.Deps {
		if dep == nil || dep.Path != path {
			continue
		}
		// A replaced module records its version on the replacement, which is
		// empty when the replacement is a local directory.
		if dep.Replace != nil {
			return normalize(dep.Replace.Version)
		}
		return normalize(dep.Version)
	}
	return unknown
}

// normalize maps the version strings that carry no useful information onto
// "unknown".
func normalize(version string) string {
	if version == "" || version == "(devel)" {
		return unknown
	}
	return version
}
