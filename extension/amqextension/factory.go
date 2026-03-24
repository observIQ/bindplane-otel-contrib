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

	"github.com/observiq/bindplane-otel-contrib/extension/amqextension/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

// NewFactory creates a new factory for the AMQ filter extension
func NewFactory() extension.Factory {
	return extension.NewFactory(
		metadata.Type,
		defaultConfig,
		createAMQExtension,
		metadata.ExtensionStability,
	)
}

func defaultConfig() component.Config {
	return &Config{
		Filters: []FilterConfig{{
			Name:              "default",
			Kind:              "bloom",
			EstimatedCount:    10000,
			FalsePositiveRate: 0.01,
		}},
	}
}

func createAMQExtension(_ context.Context, cs extension.Settings, cfg component.Config) (extension.Extension, error) {
	oCfg := cfg.(*Config)

	return newAMQExtension(cs.Logger, oCfg), nil
}
