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

package telemetrygeneratorreceiver //import "github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver"

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/observiq/blitz/config"
	"github.com/observiq/blitz/embed"
	"github.com/observiq/blitz/generator/filegen/embeddedlibrary"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/internal/recipes"
)

// blitz AdditionalConfig keys. Centralized so the validator and the
// dispatcher use the same names.
const (
	blitzKeyRecipe       = "recipe"
	blitzKeyRecipeParams = "recipe_params"
	blitzKeyBlitzYAML    = "blitz_yaml"
	// blitzKeyParseBody is an optional bool. When true, the LogAdapter
	// invokes embed.LogRecord.ParseFunc (when blitz populates it) to
	// emit a structured pcommon.Map body instead of the raw Message
	// string. Default false — telemetry-generator users typically want
	// raw log lines through the pipeline and let downstream processors
	// handle parsing.
	blitzKeyParseBody = "parse_body"
)

// assertEmbeddedLibraryAvailable verifies the blitz filegen data
// library snapshot was compiled into the binary. The binary MUST be
// built with `-tags embed_library`; without the tag,
// embeddedlibrary.FS() returns an empty stub that cannot be walked, and
// any filegen reference in a blitz_yaml config that uses `package:`
// names will fail at runtime with a confusing error. Catching the
// missing tag at Start time turns that into a clear actionable message.
func assertEmbeddedLibraryAvailable(lib fs.FS) error {
	entries, err := fs.ReadDir(lib, ".")
	if err != nil || len(entries) == 0 {
		return errors.New("blitz embedded data library is empty — this receiver requires the binary be built with `-tags embed_library`; see the receiver README for details")
	}
	return nil
}

// validateBlitzGeneratorConfig checks the shape of a Type: "blitz"
// generator entry without constructing any modules. The full module
// construction (which catches non-log generator-type rejections from
// blitz's LoadModules) is deferred to receiver Start time — running it
// at validate time would either need a real downstream consumer or a
// throwaway noop consumer plus duplicate module construction. Surfacing
// the error at Start is the same UX from the operator's perspective
// (collector startup still fails with a clear message) and avoids the
// duplication.
func validateBlitzGeneratorConfig(g *GeneratorConfig) error {
	if err := pcommon.NewMap().FromRaw(g.Attributes); err != nil {
		return fmt.Errorf("error in attributes config: %s", err)
	}
	if err := pcommon.NewMap().FromRaw(g.ResourceAttributes); err != nil {
		return fmt.Errorf("error in resource_attributes config: %s", err)
	}

	_, hasRecipe := g.AdditionalConfig[blitzKeyRecipe]
	_, hasYAML := g.AdditionalConfig[blitzKeyBlitzYAML]
	if hasRecipe == hasYAML {
		return errors.New("blitz generator requires exactly one of 'recipe' or 'blitz_yaml' in additional_config")
	}

	if hasRecipe {
		name, ok := g.AdditionalConfig[blitzKeyRecipe].(string)
		if !ok || name == "" {
			return errors.New("'recipe' must be a non-empty string")
		}
		if _, found := recipes.Get(name); !found {
			return fmt.Errorf("unknown recipe %q (available: %s)", name, strings.Join(recipes.Names(), ", "))
		}
		if _, err := parseRecipeParams(g.AdditionalConfig[blitzKeyRecipeParams]); err != nil {
			return fmt.Errorf("invalid recipe_params: %w", err)
		}
	}

	if hasYAML {
		raw, ok := g.AdditionalConfig[blitzKeyBlitzYAML].(string)
		if !ok || raw == "" {
			return errors.New("'blitz_yaml' must be a non-empty string")
		}
		if _, err := config.Load([]byte(raw), config.LoadOpts{}); err != nil {
			return fmt.Errorf("invalid blitz_yaml: %w", err)
		}
	}

	if v, present := g.AdditionalConfig[blitzKeyParseBody]; present {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("'parse_body' must be a boolean, got %T", v)
		}
	}

	return nil
}

// parseRecipeParams extracts the optional Params struct from
// AdditionalConfig.recipe_params. A nil/missing value yields a zero
// Params (recipe defaults apply for every field).
func parseRecipeParams(raw any) (recipes.Params, error) {
	if raw == nil {
		return recipes.Params{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return recipes.Params{}, fmt.Errorf("recipe_params must be a map, got %T", raw)
	}
	out := recipes.Params{}
	if v, present := m["workers"]; present {
		w, ok := v.(int)
		if !ok {
			return recipes.Params{}, fmt.Errorf("recipe_params.workers must be an int, got %T", v)
		}
		if w < 1 {
			return recipes.Params{}, fmt.Errorf("recipe_params.workers must be at least 1, got %d", w)
		}
		out.Workers = w
	}
	if v, present := m["rate"]; present {
		s, ok := v.(string)
		if !ok {
			return recipes.Params{}, fmt.Errorf("recipe_params.rate must be a duration string, got %T", v)
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return recipes.Params{}, fmt.Errorf("recipe_params.rate: %w", err)
		}
		if d <= 0 {
			return recipes.Params{}, fmt.Errorf("recipe_params.rate must be positive, got %s", d)
		}
		out.Rate = d
	}
	return out, nil
}

// buildBlitzModules resolves one blitz generator entry into a slice of
// embed.ProducerModule instances wired to the supplied consumer.
//
// Called by the logs receiver at Start time after the LogAdapter for
// the entry has been constructed. Returns an error if the entry's
// configured recipe or blitz_yaml fails to construct modules — in the
// blitz_yaml case, this is where non-log generator-type rejections
// (hostmetrics, traces, winevt) from blitz's LoadModules first reach
// the receiver. The receiver surfaces the error verbatim so the
// operator sees blitz's pointer to the v0.17.0 follow-up.
func buildBlitzModules(logger *zap.Logger, g GeneratorConfig, consumer embed.LogConsumer) ([]embed.ProducerModule, error) {
	if name, ok := g.AdditionalConfig[blitzKeyRecipe].(string); ok && name != "" {
		fn, found := recipes.Get(name)
		if !found {
			return nil, fmt.Errorf("unknown recipe %q", name)
		}
		params, err := parseRecipeParams(g.AdditionalConfig[blitzKeyRecipeParams])
		if err != nil {
			return nil, err
		}
		return fn(logger, consumer, params)
	}

	raw, ok := g.AdditionalConfig[blitzKeyBlitzYAML].(string)
	if !ok || raw == "" {
		return nil, errors.New("blitz generator entry has neither a recipe nor blitz_yaml")
	}
	return config.LoadModules([]byte(raw), config.EmbedOpts{
		Logger:         logger,
		LogConsumer:    consumer,
		FileGenLibrary: embeddedlibrary.FS(),
		// EnvOverrides intentionally nil: the receiver does not forward
		// process env. Users who want env-driven values inside their
		// blitz_yaml use the OTel collector's native env-substitution
		// syntax at collector-config time (the substitution expands
		// before the YAML string reaches the receiver).
		EnvOverrides: nil,
	})
}
