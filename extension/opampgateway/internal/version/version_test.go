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

package version

import (
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
)

func TestUserAgent(t *testing.T) {
	// under test the module is the main module, so no version is recorded
	require.Equal(t, unknown, moduleVersion())

	platform := " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"

	testCases := []struct {
		name      string
		buildInfo component.BuildInfo
		expected  string
	}{
		{
			name:      "no build info",
			buildInfo: component.BuildInfo{},
			expected:  "opamp-gateway/unknown" + platform,
		},
		{
			name: "v2 style build info",
			buildInfo: component.BuildInfo{
				Command:     "bindplane-otel-collector",
				Description: "Bindplane's custom distro for the OpenTelemetry Collector",
				Version:     "v2.0.1-beta.3",
			},
			expected: "opamp-gateway/unknown bindplane-otel-collector/v2.0.1-beta.3" + platform,
		},
		{
			name: "v1 style build info with a path for the command",
			buildInfo: component.BuildInfo{
				Command:     "/opt/observiq-otel-collector/observiq-otel-collector",
				Description: "observIQ's opentelemetry-collector distribution",
				Version:     "v1.85.0",
			},
			expected: "opamp-gateway/unknown observiq-otel-collector/v1.85.0" + platform,
		},
		{
			name: "windows command",
			buildInfo: component.BuildInfo{
				Command: `C:\Program Files\Bindplane\collector.exe`,
				Version: "v1.85.0",
			},
			expected: "opamp-gateway/unknown collector/v1.85.0" + platform,
		},
		{
			name:      "command without a version",
			buildInfo: component.BuildInfo{Command: "bindplane-otel-collector"},
			expected:  "opamp-gateway/unknown" + platform,
		},
		{
			name:      "version without a command",
			buildInfo: component.BuildInfo{Version: "v2.0.1"},
			expected:  "opamp-gateway/unknown" + platform,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, UserAgent(tc.buildInfo))
		})
	}
}

func TestBaseName(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "empty", input: "", expected: ""},
		{name: "no separator", input: "bindplane-otel-collector", expected: "bindplane-otel-collector"},
		{name: "unix path", input: "/opt/observiq-otel-collector/observiq-otel-collector", expected: "observiq-otel-collector"},
		{name: "windows path", input: `C:\Program Files\Bindplane\collector.exe`, expected: "collector.exe"},
		{name: "relative path", input: "./build/collector", expected: "collector"},
		{name: "trailing separator", input: "/opt/collector/", expected: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, baseName(tc.input))
		})
	}
}

func TestToken(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "empty", input: "", expected: ""},
		{name: "already a token", input: "bindplane-otel-collector", expected: "bindplane-otel-collector"},
		{name: "underscores are allowed", input: "collector_darwin_arm64", expected: "collector_darwin_arm64"},
		{name: "semver with a prerelease", input: "v2.0.1-beta.3", expected: "v2.0.1-beta.3"},
		{name: "spaces are replaced", input: "OpenTelemetry Collector", expected: "OpenTelemetry-Collector"},
		{name: "separators are replaced", input: "a/b", expected: "a-b"},
		{name: "parens cannot escape the comment", input: "bad(v1)", expected: "bad-v1"},
		{name: "a lone dot trims away", input: ".", expected: ""},
		{name: "a lone separator trims away", input: "/", expected: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, token(tc.input))
		})
	}
}

func TestVersionOf(t *testing.T) {
	testCases := []struct {
		name     string
		info     *debug.BuildInfo
		expected string
	}{
		{
			name:     "not found",
			info:     &debug.BuildInfo{Main: debug.Module{Path: "example.com/other"}},
			expected: unknown,
		},
		{
			name: "main module",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: modulePath, Version: "v1.2.3"},
			},
			expected: "v1.2.3",
		},
		{
			name: "main module devel",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: modulePath, Version: "(devel)"},
			},
			expected: unknown,
		},
		{
			name: "dependency",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/collector"},
				Deps: []*debug.Module{
					{Path: "example.com/unrelated", Version: "v9.9.9"},
					{Path: modulePath, Version: "v1.2.3"},
				},
			},
			expected: "v1.2.3",
		},
		{
			name: "dependency replaced by another version",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/collector"},
				Deps: []*debug.Module{
					{
						Path:    modulePath,
						Version: "v1.2.3",
						Replace: &debug.Module{Path: modulePath, Version: "v1.2.4"},
					},
				},
			},
			expected: "v1.2.4",
		},
		{
			name: "dependency replaced by local directory",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/collector"},
				Deps: []*debug.Module{
					{
						Path:    modulePath,
						Version: "v1.2.3",
						Replace: &debug.Module{Path: "../opampgateway"},
					},
				},
			},
			expected: unknown,
		},
		{
			name: "nil dependency",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/collector"},
				Deps: []*debug.Module{nil, {Path: modulePath, Version: "v1.2.3"}},
			},
			expected: "v1.2.3",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, versionOf(tc.info, modulePath))
		})
	}
}
