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
	"os"
	"path/filepath"
	"strings"
)

// compileExcludes builds matchers for exclude patterns: glob patterns use filepath.Match;
// non-glob patterns match the path exactly or as a directory prefix.
// Matchers must be passed a path already normalized with filepath.Clean.
func compileExcludes(patterns []string) []func(string) bool {
	out := make([]func(string) bool, 0, len(patterns))
	for _, raw := range patterns {
		if raw == "" {
			continue
		}
		pat := filepath.Clean(raw)
		isGlob := strings.ContainsAny(pat, "*?[]")
		if isGlob {
			p := pat
			out = append(out, func(cleanPath string) bool {
				m, err := filepath.Match(p, cleanPath)
				return err == nil && m
			})
			continue
		}
		p := pat
		out = append(out, func(cleanPath string) bool {
			if cleanPath == p {
				return true
			}
			sep := string(os.PathSeparator)
			prefix := p
			if !strings.HasSuffix(prefix, sep) {
				prefix += sep
			}
			return strings.HasPrefix(cleanPath, prefix)
		})
	}
	return out
}
