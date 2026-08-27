// Copyright  observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logtypedetectionprocessor

import (
	"fmt"
	"regexp"
	"strings"
)

type MatcherType string

const (
	MatcherTypeRegex      MatcherType = "regex"
	MatcherTypeStartsWith MatcherType = "starts_with"
)

// MatcherConfig is the user-facing config, unmarshalled from YAML.
type MatcherConfig struct {
	Name     string      `mapstructure:"name"`
	Priority int         `mapstructure:"priority"`
	Method   MatcherType `mapstructure:"method"`
	Value    string      `mapstructure:"value"`
}

// Matcher is the compiled runtime form.
type Matcher interface {
	Test(s string) bool
	Name() string
}

func (c MatcherConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	if c.Priority < 0 {
		return fmt.Errorf("priority must be >= 0")
	}

	switch c.Method {
	case MatcherTypeRegex:
		if c.Value == "" {
			return fmt.Errorf("regex matcher requires a value")
		}
		if _, err := regexp.Compile(c.Value); err != nil {
			return fmt.Errorf("invalid regex %q: %w", c.Value, err)
		}
		return nil
	case MatcherTypeStartsWith:
		if c.Value == "" {
			return fmt.Errorf("starts with matcher requires a value")
		}
		return nil
	default:
		return fmt.Errorf("unknown matcher method %q", c.Method)
	}
}

// Build compiles the config into a Matcher.
func (c MatcherConfig) Build() (Matcher, error) {
	matcherBase := matcherBase{
		name:     c.Name,
		priority: c.Priority,
	}
	switch c.Method {
	case MatcherTypeRegex:
		return newRegexMatcher(matcherBase, c.Value)
	case MatcherTypeStartsWith:
		return newStartsWithMatcher(matcherBase, c.Value)
	default:
		return nil, fmt.Errorf("unknown matcher method %q", c.Method)
	}
}

type matcherBase struct {
	name     string
	priority int
}

func (m *matcherBase) Name() string {
	return m.name
}

type regexMatcher struct {
	matcherBase
	regex *regexp.Regexp
}

func newRegexMatcher(matcherBase matcherBase, pattern string) (*regexMatcher, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compiling regex %q: %w", pattern, err)
	}
	return &regexMatcher{
		matcherBase: matcherBase,
		regex:       re,
	}, nil
}

func (m *regexMatcher) Test(s string) bool {
	return m.regex.MatchString(s)
}

type startsWithMatcher struct {
	matcherBase
	prefix string
}

func newStartsWithMatcher(matcherBase matcherBase, prefix string) (*startsWithMatcher, error) {
	return &startsWithMatcher{
		matcherBase: matcherBase,
		prefix:      prefix,
	}, nil
}

func (m *startsWithMatcher) Test(s string) bool {
	return strings.HasPrefix(s, m.prefix)
}
