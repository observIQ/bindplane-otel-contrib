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

package recipes

import (
	"context"
	"testing"
	"time"

	"github.com/observiq/blitz/embed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// nopLogConsumer satisfies embed.LogConsumer; used in tests because
// recipes hand the consumer to constructed generators but never invoke
// them in unit tests (we only verify construction).
type nopLogConsumer struct{}

func (nopLogConsumer) ConsumeLogs(context.Context, []embed.LogRecord) error { return nil }

func TestNames_ReturnsAllSixV1Recipes(t *testing.T) {
	got := Names()
	want := []string{
		"apache",
		"apache-combined",
		"apache-error",
		"kubernetes-cluster",
		"nginx",
		"pii-stress",
	}
	assert.Equal(t, want, got, "Names should return every v1 recipe in sorted order")
}

func TestGet_UnknownRecipe(t *testing.T) {
	_, ok := Get("nonexistent")
	assert.False(t, ok)
}

func TestEveryRegisteredRecipe_BuildsAtLeastOneModule(t *testing.T) {
	logger := zaptest.NewLogger(t)
	consumer := nopLogConsumer{}
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			fn, ok := Get(name)
			require.True(t, ok)
			mods, err := fn(logger, consumer, Params{})
			require.NoError(t, err)
			require.NotEmpty(t, mods, "recipe %q returned zero modules", name)
			for _, m := range mods {
				assert.NotEmpty(t, m.Name(), "module from recipe %q has empty Name()", name)
			}
		})
	}
}

func TestKubernetesCluster_BuildsThreeModules(t *testing.T) {
	logger := zaptest.NewLogger(t)
	fn, ok := Get("kubernetes-cluster")
	require.True(t, ok)
	mods, err := fn(logger, nopLogConsumer{}, Params{})
	require.NoError(t, err)
	assert.Len(t, mods, 3, "kubernetes-cluster should bundle k8s + apache + json")
}

func TestParams_ResolveOverrides(t *testing.T) {
	defaults := struct {
		workers int
		rate    time.Duration
	}{workers: 1, rate: time.Second}

	// Zero params → defaults applied.
	w, r := Params{}.resolve(defaults.workers, defaults.rate)
	assert.Equal(t, defaults.workers, w)
	assert.Equal(t, defaults.rate, r)

	// Both overridden.
	w, r = Params{Workers: 4, Rate: 500 * time.Millisecond}.resolve(defaults.workers, defaults.rate)
	assert.Equal(t, 4, w)
	assert.Equal(t, 500*time.Millisecond, r)

	// One field overridden; the other falls back to default.
	w, r = Params{Workers: 8}.resolve(defaults.workers, defaults.rate)
	assert.Equal(t, 8, w)
	assert.Equal(t, defaults.rate, r)
}

func TestRecipe_RespectsParamOverrides(t *testing.T) {
	// Smoke-check that param overrides reach the underlying generator
	// constructors without an error. We can't easily verify the rate
	// internally, but a successful construction with overridden values
	// proves the recipe accepts the params.
	logger := zaptest.NewLogger(t)
	fn, ok := Get("apache")
	require.True(t, ok)
	mods, err := fn(logger, nopLogConsumer{}, Params{Workers: 5, Rate: 50 * time.Millisecond})
	require.NoError(t, err)
	require.Len(t, mods, 1)
}
