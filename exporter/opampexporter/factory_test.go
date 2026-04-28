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

package opampexporter

import (
	"testing"

	"github.com/observiq/bindplane-otel-contrib/exporter/opampexporter/internal/metadata"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter/exportertest"
)

func TestNewFactory(t *testing.T) {
	factory := NewFactory()
	require.Equal(t, metadata.Type, factory.Type())

	expectedCfg := &Config{
		OpAMP: defaultOpAMPExtensionID,
		CustomMessage: CustomMessageConfig{
			Capability: defaultCapability,
			Type:       defaultMessageType,
		},
		MaxQueuedMessages: defaultMaxQueuedMessages,
	}

	cfg, ok := factory.CreateDefaultConfig().(*Config)
	require.True(t, ok)
	require.Equal(t, expectedCfg, cfg)
}

func TestCreateOrGetExporter(t *testing.T) {
	t.Cleanup(func() {
		unregisterExporter(component.NewIDWithName(metadata.Type, "exp1"))
		unregisterExporter(component.NewIDWithName(metadata.Type, "exp2"))
	})

	s1 := exportertest.NewNopSettings(metadata.Type)
	s1.ID = component.NewIDWithName(metadata.Type, "exp1")

	e1 := createOrGetExporter(s1, createDefaultConfig().(*Config))
	e1Again := createOrGetExporter(s1, createDefaultConfig().(*Config))

	// Same ID should return the same instance so logs/metrics/traces share
	// a single custom capability registration.
	require.Same(t, e1, e1Again)

	s2 := exportertest.NewNopSettings(metadata.Type)
	s2.ID = component.NewIDWithName(metadata.Type, "exp2")

	e2 := createOrGetExporter(s2, createDefaultConfig().(*Config))
	require.NotSame(t, e1, e2)
}
