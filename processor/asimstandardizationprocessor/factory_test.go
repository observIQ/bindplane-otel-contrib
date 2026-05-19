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

package asimstandardizationprocessor

import (
	"context"
	"testing"

	"github.com/observiq/bindplane-otel-contrib/processor/asimstandardizationprocessor/internal/metadata"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/processor/processortest"
)

func TestNewFactory(t *testing.T) {
	factory := NewFactory()
	require.Equal(t, metadata.Type, factory.Type())

	cfg, ok := factory.CreateDefaultConfig().(*Config)
	require.True(t, ok)
	require.Equal(t, &Config{}, cfg)
}

func TestCreateLogsProcessor(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig().(*Config)
	cfg.EventMappings = []EventMapping{
		{
			TargetTable: TargetTableAuthentication,
			FieldMappings: []FieldMapping{
				{From: "body.user", To: "TargetUsername"},
			},
		},
	}

	p, err := factory.CreateLogs(
		context.Background(),
		processortest.NewNopSettings(metadata.Type),
		cfg,
		consumertest.NewNop(),
	)
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestCreateLogsProcessor_InvalidConfigType(t *testing.T) {
	type otherConfig struct{ component.Config }

	_, err := createLogsProcessor(
		context.Background(),
		processortest.NewNopSettings(metadata.Type),
		otherConfig{},
		consumertest.NewNop(),
	)
	require.ErrorIs(t, err, errInvalidConfigType)
}

func TestCreateLogsProcessor_InvalidExpression(t *testing.T) {
	factory := NewFactory()
	cfg := factory.CreateDefaultConfig().(*Config)
	cfg.EventMappings = []EventMapping{
		{
			TargetTable: TargetTableAuthentication,
			FieldMappings: []FieldMapping{
				{From: "|||invalid|||", To: "TargetUsername"},
			},
		},
	}

	_, err := factory.CreateLogs(
		context.Background(),
		processortest.NewNopSettings(metadata.Type),
		cfg,
		consumertest.NewNop(),
	)
	require.Error(t, err)
}
