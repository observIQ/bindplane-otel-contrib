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
	"errors"
	"fmt"

	"github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

// errImproperCfgType error for when an invalid config type is passed to receiver creation funcs
var errImproperCfgType = errors.New("improper config type")

// NewFactory creates a new receiver factory
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithMetrics(createMetricsReceiver, metadata.MetricsStability),
		receiver.WithLogs(createLogsReceiver, metadata.LogsStability),
		receiver.WithTraces(createTracesReceiver, metadata.TracesStability),
	)
}

// createDefaultConfig creates a default configuration
func createDefaultConfig() component.Config {
	return &Config{
		PayloadsPerSecond: 1,
	}
}

// createMetricsReceiver creates a metrics receiver
func createMetricsReceiver(ctx context.Context, params receiver.Settings, conf component.Config, nextConsumer consumer.Metrics) (receiver.Metrics, error) {
	cfg, ok := conf.(*Config)
	if !ok {
		return nil, errImproperCfgType
	}
	if err := rejectBlitzOnNonLogsSignal(cfg, "metrics"); err != nil {
		return nil, err
	}

	return newMetricsReceiver(ctx, params.Logger, cfg, nextConsumer)
}

// createLogsReceiver creates a logs receiver
func createLogsReceiver(ctx context.Context, params receiver.Settings, conf component.Config, nextConsumer consumer.Logs) (receiver.Logs, error) {
	cfg, ok := conf.(*Config)
	if !ok {
		return nil, errImproperCfgType
	}

	return newLogsReceiver(ctx, params.Logger, cfg, nextConsumer)
}

// createTracesReceiver creates a traces receiver
func createTracesReceiver(ctx context.Context, params receiver.Settings, conf component.Config, nextConsumer consumer.Traces) (receiver.Traces, error) {
	cfg, ok := conf.(*Config)
	if !ok {
		return nil, errImproperCfgType
	}
	if err := rejectBlitzOnNonLogsSignal(cfg, "traces"); err != nil {
		return nil, err
	}

	return newTracesReceiver(ctx, params.Logger, cfg, nextConsumer)
}

// rejectBlitzOnNonLogsSignal fails fast if a Type: "blitz" generator
// entry is configured on a non-logs receiver instance. v1 of the blitz
// embed integration is logs-only (PIPE-1017); metric and trace adapters
// land in a follow-up once blitz v0.17.0 migrates hostmetrics and
// traces to the embed contract.
func rejectBlitzOnNonLogsSignal(cfg *Config, signal string) error {
	for _, g := range cfg.Generators {
		if g.Type == generatorTypeBlitz {
			return fmt.Errorf("generator type %q is logs-only in v1 of the telemetrygeneratorreceiver blitz integration (PIPE-1017); remove it from the %s pipeline or use one of the receiver's existing generator types", generatorTypeBlitz, signal)
		}
	}
	return nil
}
