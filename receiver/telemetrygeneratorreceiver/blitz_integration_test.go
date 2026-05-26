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

//go:build embed_library

// The integration tests in this file exercise the full receiver
// lifecycle against blitz embed sources. They require the receiver be
// built with `-tags embed_library` because:
//
//  1. The receiver asserts the embeddedlibrary FS is non-empty at
//     Start time when any Type: "blitz" entry is configured.
//  2. Filegen-based tests need the data_library snapshot baked into
//     the binary.
//
// The build tag matches the deployment reality (every binary linking
// this receiver MUST be built with the tag — see the receiver README).

package telemetrygeneratorreceiver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

// blitzRecipeTestCfg builds a Config that runs a single recipe under
// fast params suitable for integration tests.
func blitzRecipeTestCfg(recipe string, resourceAttrs, attrs map[string]any) *Config {
	return &Config{
		PayloadsPerSecond: 1,
		Generators: []GeneratorConfig{{
			Type:               generatorTypeBlitz,
			ResourceAttributes: resourceAttrs,
			Attributes:         attrs,
			AdditionalConfig: map[string]any{
				"recipe": recipe,
				"recipe_params": map[string]any{
					"workers": 1,
					"rate":    "50ms",
				},
			},
		}},
	}
}

// TestBlitz_EveryRecipe_DeliversRecordsEndToEnd boots the receiver via
// its factory for each v1 recipe, runs briefly, and asserts records
// flow into a consumertest.LogsSink. Acceptance criterion: "All six
// curated recipes are shipped and exercised in a table-driven
// integration test."
func TestBlitz_EveryRecipe_DeliversRecordsEndToEnd(t *testing.T) {
	recipes := []string{
		"apache",
		"apache-combined",
		"apache-error",
		"kubernetes-cluster",
		"nginx",
		"pii-stress",
	}
	for _, name := range recipes {
		t.Run(name, func(t *testing.T) {
			sink := &consumertest.LogsSink{}
			cfg := blitzRecipeTestCfg(name, map[string]any{"service.name": "blitz-it"}, map[string]any{"log.source": name})
			r := startReceiver(t, cfg, sink)
			t.Cleanup(func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = r.Shutdown(stopCtx)
			})

			require.Eventually(t,
				func() bool { return sink.LogRecordCount() >= 3 },
				5*time.Second, 50*time.Millisecond,
				"recipe %q produced %d records (want >=3)", name, sink.LogRecordCount(),
			)
			assertResourceAttr(t, sink.AllLogs(), "service.name", "blitz-it")
		})
	}
}

// TestBlitz_BlitzYAML_MultiModule exercises the paste-YAML path with a
// non-trivial multi-module config (apache + json + kubernetes
// concurrent) parsed via blitz's LoadModules. Acceptance criterion:
// "Custom-YAML integration test exercises a multi-module log config
// (apache + json + kubernetes concurrent) parsed via LoadModules."
func TestBlitz_BlitzYAML_MultiModule(t *testing.T) {
	yaml := `
generators:
  - type: apache-common
    apache-common: {workers: 1, rate: 50ms}
  - type: json
    json: {workers: 1, rate: 50ms, type: default}
  - type: kubernetes
    kubernetes: {workers: 1, rate: 50ms}
output:
  type: nop
logging:
  type: stdout
metrics:
  port: 19000
`
	cfg := &Config{
		PayloadsPerSecond: 1,
		Generators: []GeneratorConfig{{
			Type: generatorTypeBlitz,
			AdditionalConfig: map[string]any{
				"blitz_yaml": yaml,
			},
		}},
	}
	sink := &consumertest.LogsSink{}
	r := startReceiver(t, cfg, sink)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(stopCtx)
	})
	require.Eventually(t,
		func() bool { return sink.LogRecordCount() >= 9 },
		5*time.Second, 50*time.Millisecond,
		"multi-module blitz_yaml produced %d records (want >=9 — 3 generators × 3 records each)", sink.LogRecordCount(),
	)
}

// TestBlitz_BlitzYAML_Filegen_EmbeddedLibrary exercises a filegen
// generator that references a data_library entry by name. With the
// embed_library build tag set, embeddedlibrary.FS() is non-empty and
// filegen's `package:` references resolve against the bundled snapshot
// without any on-disk data_library/. Acceptance criterion: "Custom-YAML
// integration test exercises a filegen-based config that references
// the embedded data library; no on-disk data_library/ required for the
// test to pass."
func TestBlitz_BlitzYAML_Filegen_EmbeddedLibrary(t *testing.T) {
	yaml := `
generator:
  type: filegen
  filegen:
    workers: 1
    rate: 50ms
    source: package:syslog_generic
output:
  type: nop
logging:
  type: stdout
metrics:
  port: 19000
`
	cfg := &Config{
		PayloadsPerSecond: 1,
		Generators: []GeneratorConfig{{
			Type: generatorTypeBlitz,
			AdditionalConfig: map[string]any{
				"blitz_yaml": yaml,
			},
		}},
	}
	sink := &consumertest.LogsSink{}
	r := startReceiver(t, cfg, sink)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(stopCtx)
	})
	require.Eventually(t,
		func() bool { return sink.LogRecordCount() >= 3 },
		5*time.Second, 50*time.Millisecond,
		"filegen-via-embedded-library produced %d records (want >=3)", sink.LogRecordCount(),
	)
}

// TestBlitz_BlitzYAML_RejectsNonLogModule_AtFactoryCreation verifies
// that a blitz_yaml referencing hostmetrics (metric-producing, not yet
// embed-eligible) surfaces blitz's LoadModules rejection error when the
// receiver is constructed. Acceptance criterion: "blitz_yaml configs
// that name a non-log module surface blitz's LoadModules rejection
// error to the receiver user."
func TestBlitz_BlitzYAML_RejectsNonLogModule_AtFactoryCreation(t *testing.T) {
	yaml := `
generator:
  type: hostmetrics
  hostmetrics: {workers: 1, rate: 1s}
output:
  type: nop
logging:
  type: stdout
metrics:
  port: 19000
`
	cfg := &Config{
		PayloadsPerSecond: 1,
		Generators: []GeneratorConfig{{
			Type:             generatorTypeBlitz,
			AdditionalConfig: map[string]any{"blitz_yaml": yaml},
		}},
	}
	factory := NewFactory()
	_, err := factory.CreateLogs(context.Background(), receivertest.NewNopSettings(factory.Type()), cfg, consumertest.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostmetrics", "rejection error should name the offending module")
}

// startReceiver builds a logs receiver through the factory and Starts
// it. The returned receiver is the caller's responsibility to Shutdown
// via t.Cleanup.
func startReceiver(t *testing.T, cfg *Config, sink *consumertest.LogsSink) receiver.Logs {
	t.Helper()
	factory := NewFactory()
	r, err := factory.CreateLogs(context.Background(), receivertest.NewNopSettings(factory.Type()), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, r.Start(context.Background(), componenttest.NewNopHost()))
	return r
}

// assertResourceAttr scans every ResourceLogs in logs for an attribute
// matching key=want. Fails the test if no resource has it.
func assertResourceAttr(t *testing.T, logs []plog.Logs, key, want string) {
	t.Helper()
	for _, batch := range logs {
		for i := 0; i < batch.ResourceLogs().Len(); i++ {
			rl := batch.ResourceLogs().At(i)
			if got, ok := rl.Resource().Attributes().Get(key); ok && got.AsString() == want {
				return
			}
		}
	}
	t.Fatalf("no ResourceLogs found with %s=%q", key, want)
}
