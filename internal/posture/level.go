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

package posture

import (
	"errors"
	"fmt"
	"strings"
)

// Level is an ordered posture level. It is an index into an ordered set of
// level names; a higher value is more permissive (more telemetry may egress).
type Level int

// LevelSet maps between ordered posture level names and their indices.
type LevelSet struct {
	names []string
	index map[string]Level
}

// NewLevelSet builds a LevelSet from an ordered list of level names (lowest
// first). Names must be non-empty and unique.
func NewLevelSet(names []string) (LevelSet, error) {
	if len(names) == 0 {
		return LevelSet{}, errors.New("at least one posture level is required")
	}
	index := make(map[string]Level, len(names))
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			return LevelSet{}, errors.New("posture level names must not be empty")
		}
		if _, ok := index[n]; ok {
			return LevelSet{}, fmt.Errorf("duplicate posture level %q", n)
		}
		index[n] = Level(i)
	}
	return LevelSet{names: names, index: index}, nil
}

// Parse returns the Level for the given name.
func (ls LevelSet) Parse(name string) (Level, error) {
	l, ok := ls.index[strings.TrimSpace(name)]
	if !ok {
		return 0, fmt.Errorf("unknown posture level %q (valid: %s)", name, strings.Join(ls.names, ", "))
	}
	return l, nil
}

// Name returns the name for the given Level, or its numeric form if out of range.
func (ls LevelSet) Name(l Level) string {
	if l < 0 || int(l) >= len(ls.names) {
		return fmt.Sprintf("level(%d)", int(l))
	}
	return ls.names[l]
}

// Min returns the lowest (most restrictive) level.
func (ls LevelSet) Min() Level { return 0 }

// Max returns the highest (most permissive) level.
func (ls LevelSet) Max() Level { return Level(len(ls.names) - 1) }

// Clamp constrains l to the valid range.
func (ls LevelSet) Clamp(l Level) Level {
	if l < ls.Min() {
		return ls.Min()
	}
	if l > ls.Max() {
		return ls.Max()
	}
	return l
}
