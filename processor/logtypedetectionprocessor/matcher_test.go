// Copyright  observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logtypedetectionprocessor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegexMatcher(t *testing.T) {
	testCases := []struct {
		name        string
		matcherType MatcherType
		value       string
		log         string
		expected    bool
	}{
		{
			name:        "regex matcher matches",
			matcherType: MatcherTypeRegex,
			value:       `^2026-08-13T10:00:00Z task completed in 42ms$`,
			log:         "2026-08-13T10:00:00Z task completed in 42ms",
			expected:    true,
		},
		{
			name:        "regex matcher does not match",
			matcherType: MatcherTypeRegex,
			value:       `^2026-08-13T10:00:00Z task completed in 42ms$`,
			log:         "2026-08-13T10:00:00Z task completed in 42ms, but not this one",
			expected:    false,
		},
		{
			name:        "starts with matcher matches",
			matcherType: MatcherTypeStartsWith,
			value:       "2026-08-13T10:00:00Z task completed in 42ms",
			log:         "2026-08-13T10:00:00Z task completed in 42ms",
			expected:    true,
		},
		{
			name:        "starts with matcher does not match",
			matcherType: MatcherTypeStartsWith,
			value:       "2026-08-13T10:00:00Z task completed in 43ms",
			log:         "2026-08-13T10:00:00Z task completed in 42ms, but not this one",
			expected:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var matcher Matcher
			switch tc.matcherType {
			case MatcherTypeRegex:
				regexMatcher, err := newRegexMatcher(matcherBase{name: "test"}, tc.value)
				require.NoError(t, err)
				matcher = regexMatcher
			case MatcherTypeStartsWith:
				startsWithMatcher := newStartsWithMatcher(matcherBase{name: "test"}, tc.value)
				matcher = startsWithMatcher
			}
			require.Equal(t, tc.expected, matcher.Test(tc.log))
		})
	}
}
