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

// Package recipes ships curated blitz embed configurations as Go
// constructors. Each recipe builds and returns a set of
// embed.ProducerModule instances tuned for a common scenario; the
// receiver invokes a recipe by name from its config and hands the
// modules to embed.New.
//
// Recipes are an alternative to the receiver's paste-YAML
// configuration shape (blitz_yaml) — they trade flexibility for a
// stable, version-pinned starting point that doesn't require the user
// to author blitz YAML.
package recipes // import "github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/internal/recipes"

import (
	"fmt"
	"sort"
	"time"

	"github.com/observiq/blitz/embed"
	"go.uber.org/zap"
)

// Params captures the user-supplied knobs every recipe accepts. Recipes
// MAY override their internal defaults with these values when set; zero
// values fall back to recipe-specific defaults.
//
// Recipes containing more than one generator apply Workers and Rate to
// each generator they construct. That keeps the schema small and the
// behavior predictable; users wanting per-generator control should use
// the blitz_yaml shape instead.
type Params struct {
	// Workers is the per-generator worker count. Zero means recipe default.
	Workers int

	// Rate is the per-generator emission interval. Zero means recipe default.
	Rate time.Duration
}

// resolve returns the effective workers + rate for a recipe, taking
// either user-supplied values when set or the supplied recipe defaults.
func (p Params) resolve(defaultWorkers int, defaultRate time.Duration) (int, time.Duration) {
	workers := p.Workers
	if workers == 0 {
		workers = defaultWorkers
	}
	rate := p.Rate
	if rate == 0 {
		rate = defaultRate
	}
	return workers, rate
}

// Func is the contract every recipe implements. Construct and return
// ProducerModules wired to the supplied consumer; the runner takes
// over lifecycle from there.
type Func func(*zap.Logger, embed.LogConsumer, Params) ([]embed.ProducerModule, error)

// registry holds every recipe shipped with the receiver. Keys are the
// names users reference in config (AdditionalConfig.recipe). The map is
// populated by init() in each recipe-specific file in this package.
var registry = map[string]Func{}

// register is the package-private add-to-registry helper used by each
// recipe's init(). Panics on duplicate registration — recipe names must
// be unique and conflicts indicate a programming error, not a runtime
// condition.
func register(name string, fn Func) {
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("recipes: duplicate registration for %q", name))
	}
	registry[name] = fn
}

// Get returns the recipe with the given name plus a found flag. The
// dispatcher uses this both at config-validate time (existence check)
// and at receiver Start time (actual invocation).
func Get(name string) (Func, bool) {
	fn, ok := registry[name]
	return fn, ok
}

// Names returns every registered recipe name in sorted order. Used to
// build helpful error messages when an unknown recipe is referenced.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
