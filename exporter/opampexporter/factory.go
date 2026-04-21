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
	"context"
	"errors"
	"sync"

	"github.com/observiq/bindplane-otel-contrib/exporter/opampexporter/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

// NewFactory creates a new OpAMP exporter factory.
func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		metadata.Type,
		createDefaultConfig,
		exporter.WithLogs(createLogsExporter, metadata.LogsStability),
		exporter.WithMetrics(createMetricsExporter, metadata.MetricsStability),
		exporter.WithTraces(createTracesExporter, metadata.TracesStability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		OpAMP: defaultOpAMPExtensionID,
	}
}

var consumerCapabilities = consumer.Capabilities{MutatesData: false}

func createLogsExporter(ctx context.Context, params exporter.Settings, config component.Config) (exporter.Logs, error) {
	cfg, ok := config.(*Config)
	if !ok {
		return nil, errors.New("invalid configuration: expected *Config for opamp exporter")
	}

	e := createOrGetExporter(params, cfg)

	return exporterhelper.NewLogs(
		ctx,
		params,
		cfg,
		e.consumeLogs,
		exporterhelper.WithCapabilities(consumerCapabilities),
		exporterhelper.WithStart(e.start),
		exporterhelper.WithShutdown(e.shutdown),
	)
}

func createMetricsExporter(ctx context.Context, params exporter.Settings, config component.Config) (exporter.Metrics, error) {
	cfg, ok := config.(*Config)
	if !ok {
		return nil, errors.New("invalid configuration: expected *Config for opamp exporter")
	}

	e := createOrGetExporter(params, cfg)

	return exporterhelper.NewMetrics(
		ctx,
		params,
		cfg,
		e.consumeMetrics,
		exporterhelper.WithCapabilities(consumerCapabilities),
		exporterhelper.WithStart(e.start),
		exporterhelper.WithShutdown(e.shutdown),
	)
}

func createTracesExporter(ctx context.Context, params exporter.Settings, config component.Config) (exporter.Traces, error) {
	cfg, ok := config.(*Config)
	if !ok {
		return nil, errors.New("invalid configuration: expected *Config for opamp exporter")
	}

	e := createOrGetExporter(params, cfg)

	return exporterhelper.NewTraces(
		ctx,
		params,
		cfg,
		e.consumeTraces,
		exporterhelper.WithCapabilities(consumerCapabilities),
		exporterhelper.WithStart(e.start),
		exporterhelper.WithShutdown(e.shutdown),
	)
}

// createOrGetExporter returns a single instance of the exporter for a given
// component.ID, so that the same opamp capability registration is shared
// across logs, metrics, and traces pipelines that reference the same exporter.
func createOrGetExporter(params exporter.Settings, cfg *Config) *opampExporter {
	exportersMux.Lock()
	defer exportersMux.Unlock()

	if e, ok := exporters[params.ID]; ok {
		return e
	}
	e := newOpAMPExporter(params.Logger, cfg, params.ID)
	exporters[params.ID] = e
	return e
}

func unregisterExporter(id component.ID) {
	exportersMux.Lock()
	defer exportersMux.Unlock()
	delete(exporters, id)
}

var (
	exporters    = map[component.ID]*opampExporter{}
	exportersMux = sync.Mutex{}
)
