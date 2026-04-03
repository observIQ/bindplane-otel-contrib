// Copyright  observIQ, Inc.
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

package bytebatcherprocessor

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"

	"github.com/observiq/bindplane-otel-contrib/processor/bytebatcherprocessor/internal/metadata"
)

var componentType = component.MustNewType("bytebatcher")

const (
	logsStability    = metadata.LogsStability
	metricsStability = metadata.MetricsStability
	tracesStability  = metadata.TracesStability
)

var (
	consumerCapabilities = consumer.Capabilities{MutatesData: true}
	errInvalidConfigType = errors.New("config is not of type bytebatcher")
)

func NewFactory() processor.Factory {
	return processor.NewFactory(
		componentType,
		createDefaultConfig,
		processor.WithTraces(createTracesProcessor, tracesStability),
		processor.WithLogs(createLogsProcessor, logsStability),
		processor.WithMetrics(createMetricsProcessor, metricsStability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		FlushInterval: 1 * time.Second,
		Bytes:         1024,
	}
}

func createTracesProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	nextConsumer consumer.Traces,
) (processor.Traces, error) {
	bytebatcherCfg, ok := cfg.(*Config)
	if !ok {
		return nil, errInvalidConfigType
	}

	telemetry, err := metadata.NewTelemetryBuilder(set.TelemetrySettings)
	if err != nil {
		return nil, err
	}

	proc := newTracesProcessor(bytebatcherCfg, set.Logger, telemetry, func() batch[ptrace.Traces] {
		return newBatchTraces(nextConsumer, telemetry)
	})
	return processorhelper.NewTraces(ctx, set, cfg, nextConsumer, proc.processTraces,
		processorhelper.WithCapabilities(consumerCapabilities),
		processorhelper.WithStart(proc.Start),
		processorhelper.WithShutdown(proc.Shutdown),
	)
}

func createLogsProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	nextConsumer consumer.Logs,
) (processor.Logs, error) {
	bytebatcherCfg, ok := cfg.(*Config)
	if !ok {
		return nil, errInvalidConfigType
	}

	telemetry, err := metadata.NewTelemetryBuilder(set.TelemetrySettings)
	if err != nil {
		return nil, err
	}

	proc := newLogsProcessor(bytebatcherCfg, set.Logger, telemetry, func() batch[plog.Logs] {
		return newBatchLogs(nextConsumer, telemetry)
	})
	return processorhelper.NewLogs(ctx, set, cfg, nextConsumer, proc.processLogs,
		processorhelper.WithCapabilities(consumerCapabilities),
		processorhelper.WithStart(proc.Start),
		processorhelper.WithShutdown(proc.Shutdown),
	)
}

func createMetricsProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	nextConsumer consumer.Metrics,
) (processor.Metrics, error) {
	bytebatcherCfg, ok := cfg.(*Config)
	if !ok {
		return nil, errInvalidConfigType
	}

	telemetry, err := metadata.NewTelemetryBuilder(set.TelemetrySettings)
	if err != nil {
		return nil, err
	}

	proc := newMetricsProcessor(bytebatcherCfg, set.Logger, telemetry, func() batch[pmetric.Metrics] {
		return newBatchMetrics(nextConsumer, telemetry)
	})

	return processorhelper.NewMetrics(ctx, set, cfg, nextConsumer, proc.processMetrics,
		processorhelper.WithCapabilities(consumerCapabilities),
		processorhelper.WithStart(proc.Start),
		processorhelper.WithShutdown(proc.Shutdown),
	)
}
