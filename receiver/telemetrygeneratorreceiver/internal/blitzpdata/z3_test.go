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

package blitzpdata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseZ3_NilInput_ReturnsEmpty(t *testing.T) {
	cfg, err := ParseZ3(nil, "resource_attributes")
	require.NoError(t, err)
	assert.True(t, cfg.IsEmpty())
	assert.Nil(t, cfg.Base)
	assert.Nil(t, cfg.Locked)
}

func TestParseZ3_EmptyInput_ReturnsEmpty(t *testing.T) {
	cfg, err := ParseZ3(map[string]any{}, "resource_attributes")
	require.NoError(t, err)
	assert.True(t, cfg.IsEmpty())
}

func TestParseZ3_SimpleForm_Scalars(t *testing.T) {
	in := map[string]any{
		"host.name":    "my-host",
		"port":         8080,
		"enabled":      true,
		"sample.ratio": 0.25,
	}
	cfg, err := ParseZ3(in, "resource_attributes")
	require.NoError(t, err)
	assert.Equal(t, "my-host", cfg.Base["host.name"])
	assert.Equal(t, 8080, cfg.Base["port"])
	assert.Equal(t, true, cfg.Base["enabled"])
	assert.Equal(t, 0.25, cfg.Base["sample.ratio"])
	assert.Empty(t, cfg.Locked, "scalars are unlocked")
}

func TestParseZ3_StructuredForm_Locked(t *testing.T) {
	in := map[string]any{
		"deployment.environment": map[string]any{
			"value": "staging",
			"lock":  true,
		},
	}
	cfg, err := ParseZ3(in, "resource_attributes")
	require.NoError(t, err)
	assert.Equal(t, "staging", cfg.Base["deployment.environment"])
	_, locked := cfg.Locked["deployment.environment"]
	assert.True(t, locked)
}

func TestParseZ3_StructuredForm_ExplicitUnlocked(t *testing.T) {
	in := map[string]any{
		"region": map[string]any{
			"value": "us-east",
			"lock":  false,
		},
	}
	cfg, err := ParseZ3(in, "resource_attributes")
	require.NoError(t, err)
	assert.Equal(t, "us-east", cfg.Base["region"])
	_, locked := cfg.Locked["region"]
	assert.False(t, locked, "lock: false stays unlocked")
}

func TestParseZ3_StructuredForm_OmittedLockDefaultsFalse(t *testing.T) {
	in := map[string]any{
		"region": map[string]any{
			"value": "us-east",
		},
	}
	cfg, err := ParseZ3(in, "resource_attributes")
	require.NoError(t, err)
	assert.Equal(t, "us-east", cfg.Base["region"])
	_, locked := cfg.Locked["region"]
	assert.False(t, locked, "omitted lock defaults to false")
}

func TestParseZ3_MixedSimpleAndStructured(t *testing.T) {
	in := map[string]any{
		"host.name":              "my-host",
		"cluster.name":           "gargantua",
		"deployment.environment": map[string]any{"value": "staging", "lock": true},
		"region": map[string]any{
			"value": "us-east",
			"lock":  false,
		},
	}
	cfg, err := ParseZ3(in, "resource_attributes")
	require.NoError(t, err)
	assert.Equal(t, "my-host", cfg.Base["host.name"])
	assert.Equal(t, "gargantua", cfg.Base["cluster.name"])
	assert.Equal(t, "staging", cfg.Base["deployment.environment"])
	assert.Equal(t, "us-east", cfg.Base["region"])
	assert.Contains(t, cfg.Locked, "deployment.environment")
	assert.NotContains(t, cfg.Locked, "region")
	assert.NotContains(t, cfg.Locked, "host.name")
}

func TestParseZ3_StructuredForm_MapValueAsValueField(t *testing.T) {
	// The `value` field can itself be any type — including a nested
	// map. This is the simple-form-vs-structured-form disambiguation
	// path: the structured form is identified by presence of the
	// `value` sub-key, not by the value type.
	in := map[string]any{
		"nested.attr": map[string]any{
			"value": map[string]any{"a": "b", "c": 1},
			"lock":  true,
		},
	}
	cfg, err := ParseZ3(in, "attributes")
	require.NoError(t, err)
	nested, ok := cfg.Base["nested.attr"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "b", nested["a"])
	assert.Equal(t, 1, nested["c"])
	assert.Contains(t, cfg.Locked, "nested.attr")
}

func TestParseZ3_MapWithoutValueKey_Rejected(t *testing.T) {
	in := map[string]any{
		"deployment.environment": map[string]any{
			"locked": true, // typo for "lock", no value field
		},
	}
	_, err := ParseZ3(in, "resource_attributes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource_attributes.deployment.environment")
	assert.Contains(t, err.Error(), "no `value` sub-key")
}

func TestParseZ3_StructuredForm_UnknownSubKey_Rejected(t *testing.T) {
	in := map[string]any{
		"deployment.environment": map[string]any{
			"value":  "staging",
			"sticky": true, // unknown sub-key
		},
	}
	_, err := ParseZ3(in, "resource_attributes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource_attributes.deployment.environment")
	assert.Contains(t, err.Error(), `unknown sub-key "sticky"`)
}

func TestParseZ3_StructuredForm_NonBooleanLock_Rejected(t *testing.T) {
	in := map[string]any{
		"deployment.environment": map[string]any{
			"value": "staging",
			"lock":  "yes", // should be boolean
		},
	}
	_, err := ParseZ3(in, "resource_attributes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource_attributes.deployment.environment.lock")
	assert.Contains(t, err.Error(), "must be a boolean")
}

func TestParseZ3_DeterministicErrorKey(t *testing.T) {
	// Two errors in one map — parser should fail on the
	// alphabetically-first offending key so the user sees a stable
	// error across runs.
	in := map[string]any{
		"zeta": map[string]any{"locked": true}, // no value
		"alpha": map[string]any{
			"value": "v",
			"lock":  "yes", // non-bool
		},
	}
	_, err := ParseZ3(in, "attributes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "attributes.alpha", "alpha sorts before zeta")
}

func TestMergeWithStringOverlay_Empty(t *testing.T) {
	cfg := Z3Config{}
	merged := cfg.MergeWithStringOverlay(nil)
	assert.Empty(t, merged)
}

func TestMergeWithStringOverlay_BaseOnly(t *testing.T) {
	cfg := Z3Config{Base: map[string]any{"host.name": "h1", "os.type": "linux"}}
	merged := cfg.MergeWithStringOverlay(nil)
	assert.Equal(t, "h1", merged["host.name"])
	assert.Equal(t, "linux", merged["os.type"])
}

func TestMergeWithStringOverlay_OverlayWins_Unlocked(t *testing.T) {
	cfg := Z3Config{Base: map[string]any{"host.name": "from-config"}}
	merged := cfg.MergeWithStringOverlay(map[string]string{"host.name": "from-blitz"})
	assert.Equal(t, "from-blitz", merged["host.name"], "unlocked: blitz overlay wins")
}

func TestMergeWithStringOverlay_LockedStays(t *testing.T) {
	cfg := Z3Config{
		Base:   map[string]any{"host.name": "from-config"},
		Locked: map[string]struct{}{"host.name": {}},
	}
	merged := cfg.MergeWithStringOverlay(map[string]string{"host.name": "from-blitz"})
	assert.Equal(t, "from-config", merged["host.name"], "locked: receiver-config wins")
}

func TestMergeWithStringOverlay_OverlayKeyNotInBase_Added(t *testing.T) {
	cfg := Z3Config{Base: map[string]any{"host.name": "h"}}
	merged := cfg.MergeWithStringOverlay(map[string]string{"telemetry.source": "nginx"})
	assert.Equal(t, "h", merged["host.name"])
	assert.Equal(t, "nginx", merged["telemetry.source"])
}

func TestMergeWithAnyOverlay_LockedStays_AnyValueType(t *testing.T) {
	cfg := Z3Config{
		Base:   map[string]any{"svc.weight": 100},
		Locked: map[string]struct{}{"svc.weight": {}},
	}
	merged := cfg.MergeWithAnyOverlay(map[string]any{"svc.weight": 0})
	assert.Equal(t, 100, merged["svc.weight"])
}

func TestFingerprintMap_Empty(t *testing.T) {
	assert.Equal(t, "", FingerprintMap(nil))
	assert.Equal(t, "", FingerprintMap(map[string]any{}))
}

func TestFingerprintMap_StableUnderKeyOrder(t *testing.T) {
	a := FingerprintMap(map[string]any{"b": 1, "a": 2})
	b := FingerprintMap(map[string]any{"a": 2, "b": 1})
	assert.Equal(t, a, b, "fingerprint should be insertion-order-independent")
}

func TestFingerprintMap_DifferentValuesYieldDifferentFingerprints(t *testing.T) {
	a := FingerprintMap(map[string]any{"host.name": "h1"})
	b := FingerprintMap(map[string]any{"host.name": "h2"})
	assert.NotEqual(t, a, b)
}

func TestFingerprintMap_SeparatorAvoidsCollision(t *testing.T) {
	// Without a separator, both these maps could produce the same
	// flat string "abc". The unit-separator byte prevents that.
	a := FingerprintMap(map[string]any{"a": "bc"})
	b := FingerprintMap(map[string]any{"ab": "c"})
	assert.NotEqual(t, a, b)
}
