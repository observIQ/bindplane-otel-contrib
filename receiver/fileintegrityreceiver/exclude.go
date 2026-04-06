// Copyright observIQ, Inc.
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

package fileintegrityreceiver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathMatcher reports whether a filepath.Clean'd path should be excluded.
type PathMatcher func(string) bool

// CompileExcludes builds matchers from a list of exclude patterns.
// Glob patterns (*, ?, []) use filepath.Match; plain patterns match
// exactly or as a directory prefix. Returns an error if any glob is malformed.
func CompileExcludes(patterns []string) ([]PathMatcher, error) {
	out := make([]PathMatcher, 0, len(patterns))
	for _, raw := range patterns {
		if raw == "" {
			continue
		}
		pat := filepath.Clean(raw)

		if isGlob(pat) {
			m, err := newGlobMatcher(pat)
			if err != nil {
				return nil, fmt.Errorf("bad exclude pattern %q: %w", raw, err)
			}
			out = append(out, m)
		} else {
			out = append(out, newPrefixMatcher(pat))
		}
	}
	return out, nil
}

// isGlob reports whether a pattern contains glob meta-characters.
func isGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[]")
}

// newGlobMatcher returns a matcher that uses filepath.Match.
// It validates the pattern eagerly so callers fail fast on bad syntax.
func newGlobMatcher(pattern string) (PathMatcher, error) {
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	return func(path string) bool {
		matched, _ := filepath.Match(pattern, path)
		return matched
	}, nil
}

// newPrefixMatcher returns a matcher that matches an exact path
// or anything nested under it as a directory.
func newPrefixMatcher(dir string) PathMatcher {
	prefix := dir + string(os.PathSeparator)
	return func(path string) bool {
		return path == dir || strings.HasPrefix(path, prefix)
	}
}
