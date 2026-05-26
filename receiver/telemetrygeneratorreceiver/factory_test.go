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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

func Test_createDefaultConfig(t *testing.T) {
	expectedCfg := &Config{
		PayloadsPerSecond: 1,
	}

	componentCfg := createDefaultConfig()
	actualCfg, ok := componentCfg.(*Config)
	require.True(t, ok)
	require.Equal(t, expectedCfg, actualCfg)
}

func Test_createMetricsReceiver_RejectsBlitzEntries(t *testing.T) {
	factory := NewFactory()
	cfg := &Config{
		PayloadsPerSecond: 1,
		Generators: []GeneratorConfig{{
			Type:             generatorTypeBlitz,
			AdditionalConfig: map[string]any{"recipe": "apache"},
		}},
	}
	_, err := factory.CreateMetrics(context.Background(), receivertest.NewNopSettings(factory.Type()), cfg, consumertest.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logs-only in v1")
	assert.Contains(t, err.Error(), "metrics pipeline")
}

func Test_createTracesReceiver_RejectsBlitzEntries(t *testing.T) {
	factory := NewFactory()
	cfg := &Config{
		PayloadsPerSecond: 1,
		Generators: []GeneratorConfig{{
			Type:             generatorTypeBlitz,
			AdditionalConfig: map[string]any{"recipe": "apache"},
		}},
	}
	_, err := factory.CreateTraces(context.Background(), receivertest.NewNopSettings(factory.Type()), cfg, consumertest.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logs-only in v1")
	assert.Contains(t, err.Error(), "traces pipeline")
}

func Test_createMetricsReceiver_StillAcceptsNonBlitzEntries(t *testing.T) {
	// Regression check: factory rejection logic must only trigger on
	// Type: "blitz" — existing receiver types still work on metrics.
	factory := NewFactory()
	cfg := &Config{
		PayloadsPerSecond: 1,
		Generators: []GeneratorConfig{{
			Type: generatorTypeHostMetrics,
			AdditionalConfig: map[string]any{
				"host_name": "test.example.com",
			},
		}},
	}
	r, err := factory.CreateMetrics(context.Background(), receivertest.NewNopSettings(factory.Type()), cfg, consumertest.NewNop())
	require.NoError(t, err)
	require.NotNil(t, r)
}
