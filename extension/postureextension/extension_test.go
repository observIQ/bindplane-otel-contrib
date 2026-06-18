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

package postureextension

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension/extensiontest"

	"github.com/observiq/bindplane-otel-contrib/internal/posture"
)

func TestNewFactory(t *testing.T) {
	f := NewFactory()
	assert.Equal(t, componentType, f.Type())

	cfg := f.CreateDefaultConfig().(*Config)
	require.NoError(t, cfg.Validate())
	assert.Equal(t, posture.DefaultLevels, cfg.Levels)
}

func TestCreateExtensionLifecycle(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig().(*Config)
	cfg.Default = "full"

	ext, err := f.Create(context.Background(), extensiontest.NewNopSettings(componentType), cfg)
	require.NoError(t, err)

	prov, ok := ext.(posture.Provider)
	require.True(t, ok, "extension must expose posture.Provider")

	require.NoError(t, ext.Start(context.Background(), componenttestHost{}))
	full, _ := posture.NewLevelSet(posture.DefaultLevels)
	assert.Equal(t, full.Max(), prov.Current())
	require.NoError(t, ext.Shutdown(context.Background()))
}

type componenttestHost struct{}

func (componenttestHost) GetExtensions() map[component.ID]component.Component { return nil }
