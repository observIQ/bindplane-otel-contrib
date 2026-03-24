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

package amqextension

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/extension/extensiontest"

	"github.com/observiq/bindplane-otel-contrib/extension/amqextension/internal/metadata"
)

func TestExtensionFrom_OK(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig().(*Config)
	cfg.Filters[0].Name = "testfilter"

	ext, err := factory.Create(context.Background(), extensiontest.NewNopSettings(metadata.Type), cfg)
	require.NoError(t, err)
	require.NoError(t, ext.Start(context.Background(), componenttest.NewNopHost()))
	t.Cleanup(func() { _ = ext.Shutdown(context.Background()) })

	m, err := ExtensionFrom(ext)
	require.NoError(t, err)
	m.AddString("testfilter", "hello")
	require.True(t, m.MayContainString("testfilter", "hello"))
}

func TestExtensionFrom_Nil(t *testing.T) {
	_, err := ExtensionFrom(nil)
	require.Error(t, err)
}

func TestExtensionFrom_WrongType(t *testing.T) {
	_, err := ExtensionFrom(noopComponent{})
	require.Error(t, err)
}

type noopComponent struct{}

func (noopComponent) Start(context.Context, component.Host) error { return nil }
func (noopComponent) Shutdown(context.Context) error               { return nil }
