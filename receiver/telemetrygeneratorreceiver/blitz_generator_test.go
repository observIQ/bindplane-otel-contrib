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

package telemetrygeneratorreceiver

import (
	"context"
	"testing"
	"time"

	"github.com/observiq/blitz/embed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// nopBlitzLogConsumer satisfies embed.LogConsumer for tests where the
// dispatcher's return value is the subject under test and downstream
// record flow isn't relevant.
type nopBlitzLogConsumer struct{}

func (nopBlitzLogConsumer) ConsumeLogs(context.Context, []embed.LogRecord) error { return nil }

func TestValidateBlitz_RequiresRecipeOrYAML(t *testing.T) {
	cases := []struct {
		name      string
		extra     map[string]any
		wantErr   bool
		errSubstr string
	}{
		{
			name:      "neither present",
			extra:     map[string]any{},
			wantErr:   true,
			errSubstr: "exactly one of 'recipe' or 'blitz_yaml'",
		},
		{
			name:      "both present",
			extra:     map[string]any{"recipe": "apache", "blitz_yaml": "x"},
			wantErr:   true,
			errSubstr: "exactly one of 'recipe' or 'blitz_yaml'",
		},
		{
			name:    "recipe only — valid name",
			extra:   map[string]any{"recipe": "apache"},
			wantErr: false,
		},
		{
			name:      "recipe only — empty name",
			extra:     map[string]any{"recipe": ""},
			wantErr:   true,
			errSubstr: "non-empty string",
		},
		{
			name:      "recipe only — wrong type",
			extra:     map[string]any{"recipe": 42},
			wantErr:   true,
			errSubstr: "non-empty string",
		},
		{
			name:      "recipe only — unknown name",
			extra:     map[string]any{"recipe": "fictional-recipe"},
			wantErr:   true,
			errSubstr: "unknown recipe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &GeneratorConfig{Type: generatorTypeBlitz, AdditionalConfig: tc.extra}
			err := validateBlitzGeneratorConfig(cfg)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateBlitz_BlitzYAML_Shape(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		wantErr   bool
		errSubstr string
	}{
		{
			name: "minimal valid apache config",
			yaml: `
generator:
  type: apache-common
  apache-common:
    workers: 1
    rate: 1s
output:
  type: nop
logging:
  type: stdout
metrics:
  port: 19000
`,
		},
		{
			name:      "empty string rejected",
			yaml:      "",
			wantErr:   true,
			errSubstr: "non-empty string",
		},
		{
			name:      "syntactically broken YAML",
			yaml:      "this is: : not valid yaml: -",
			wantErr:   true,
			errSubstr: "invalid blitz_yaml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &GeneratorConfig{
				Type:             generatorTypeBlitz,
				AdditionalConfig: map[string]any{"blitz_yaml": tc.yaml},
			}
			err := validateBlitzGeneratorConfig(cfg)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateBlitz_RecipeParams(t *testing.T) {
	cases := []struct {
		name      string
		params    any
		wantErr   bool
		errSubstr string
	}{
		{name: "absent OK", params: nil, wantErr: false},
		{name: "valid workers + rate", params: map[string]any{"workers": 4, "rate": "500ms"}, wantErr: false},
		{name: "workers wrong type", params: map[string]any{"workers": "four"}, wantErr: true, errSubstr: "workers must be an int"},
		{name: "workers zero", params: map[string]any{"workers": 0}, wantErr: true, errSubstr: "at least 1"},
		{name: "rate wrong type", params: map[string]any{"rate": 5}, wantErr: true, errSubstr: "duration string"},
		{name: "rate unparseable", params: map[string]any{"rate": "nope"}, wantErr: true, errSubstr: "rate:"},
		{name: "rate zero", params: map[string]any{"rate": "0s"}, wantErr: true, errSubstr: "must be positive"},
		{name: "wrong outer type", params: "not a map", wantErr: true, errSubstr: "must be a map"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{"recipe": "apache"}
			if tc.params != nil {
				extra["recipe_params"] = tc.params
			}
			cfg := &GeneratorConfig{Type: generatorTypeBlitz, AdditionalConfig: extra}
			err := validateBlitzGeneratorConfig(cfg)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateBlitz_ParseBody(t *testing.T) {
	cases := []struct {
		name      string
		value     any
		wantErr   bool
		errSubstr string
	}{
		{name: "absent OK", value: nil, wantErr: false},
		{name: "true OK", value: true, wantErr: false},
		{name: "false OK", value: false, wantErr: false},
		{name: "string rejected", value: "true", wantErr: true, errSubstr: "must be a boolean"},
		{name: "int rejected", value: 1, wantErr: true, errSubstr: "must be a boolean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{"recipe": "apache"}
			if tc.value != nil {
				extra["parse_body"] = tc.value
			}
			cfg := &GeneratorConfig{Type: generatorTypeBlitz, AdditionalConfig: extra}
			err := validateBlitzGeneratorConfig(cfg)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateBlitz_AttributesAndResourceAttributes(t *testing.T) {
	// Receiver-config Attributes and ResourceAttributes go through
	// pcommon.NewMap().FromRaw() to validate they're representable as
	// pcommon.Map. Invalid raw shapes (e.g., a map with a chan value)
	// should be rejected; valid raw shapes pass through.
	cfg := &GeneratorConfig{
		Type:               generatorTypeBlitz,
		ResourceAttributes: map[string]any{"service.name": "blitz-test"},
		Attributes:         map[string]any{"log.source": "apache"},
		AdditionalConfig:   map[string]any{"recipe": "apache"},
	}
	assert.NoError(t, validateBlitzGeneratorConfig(cfg))
}

func TestParseRecipeParams_AcceptsRecipeDefaults(t *testing.T) {
	// Absent recipe_params → zero Params, which downstream recipes
	// translate to their internal defaults.
	got, err := parseRecipeParams(nil)
	require.NoError(t, err)
	assert.Equal(t, 0, got.Workers)
	assert.Equal(t, time.Duration(0), got.Rate)
}

func TestBuildBlitzModules_RecipePath(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := GeneratorConfig{
		Type: generatorTypeBlitz,
		AdditionalConfig: map[string]any{
			"recipe":        "apache",
			"recipe_params": map[string]any{"workers": 2, "rate": "100ms"},
		},
	}
	mods, err := buildBlitzModules(logger, cfg, nopBlitzLogConsumer{})
	require.NoError(t, err)
	require.Len(t, mods, 1, "apache recipe should yield exactly one module")
}

func TestBuildBlitzModules_BlitzYAMLPath(t *testing.T) {
	logger := zaptest.NewLogger(t)
	yaml := `
generator:
  type: apache-common
  apache-common:
    workers: 1
    rate: 1s
output:
  type: nop
logging:
  type: stdout
metrics:
  port: 19000
`
	cfg := GeneratorConfig{
		Type:             generatorTypeBlitz,
		AdditionalConfig: map[string]any{"blitz_yaml": yaml},
	}
	mods, err := buildBlitzModules(logger, cfg, nopBlitzLogConsumer{})
	require.NoError(t, err)
	require.Len(t, mods, 1)
}

func TestBuildBlitzModules_BlitzYAMLRejectsNonLogModule(t *testing.T) {
	// hostmetrics is metric-producing and not yet migrated to the embed
	// contract; blitz's LoadModules must surface the rejection to the
	// receiver caller verbatim.
	logger := zaptest.NewLogger(t)
	yaml := `
generator:
  type: hostmetrics
  hostmetrics:
    workers: 1
    rate: 1s
output:
  type: nop
logging:
  type: stdout
metrics:
  port: 19000
`
	cfg := GeneratorConfig{
		Type:             generatorTypeBlitz,
		AdditionalConfig: map[string]any{"blitz_yaml": yaml},
	}
	_, err := buildBlitzModules(logger, cfg, nopBlitzLogConsumer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostmetrics", "rejection error should mention the rejected module type")
}

func TestBuildBlitzModules_MissingShape(t *testing.T) {
	logger := zaptest.NewLogger(t)
	cfg := GeneratorConfig{
		Type:             generatorTypeBlitz,
		AdditionalConfig: map[string]any{},
	}
	_, err := buildBlitzModules(logger, cfg, nopBlitzLogConsumer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither a recipe nor blitz_yaml")
}
