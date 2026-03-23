package googlesecopsexporter

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

// NewFactory creates a new Chronicle exporter factory.
func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		metadata.Type,
		createDefaultConfig,
		exporter.WithLogs(createLogsExporter, metadata.LogsStability))
}

const (
	defaultHost                  = "chronicle.googleapis.com"
	defaultBatchRequestSizeLimit = 4000000
	defaultMetricsInterval       = 1 * time.Minute
)

var defaultCollectorID = uuid.MustParse("aaaa1111-aaaa-1111-aaaa-1111aaaa1111")

// createDefaultConfig creates the default configuration for the google secops exporter.
func createDefaultConfig() component.Config {
	return &Config{
		API:                   chronicleAPI,
		Hostname:              defaultHost,
		OverrideLogType:       true,
		CollectAgentMetrics:   true,
		MetricsInterval:       defaultMetricsInterval,
		LogErroredPayloads:    false,
		ValidateLogTypes:      false,
		Compression:           noCompression,
		BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
		TimeoutConfig:         exporterhelper.NewDefaultTimeoutConfig(),
		QueueBatchConfig:      configoptional.Some(exporterhelper.NewDefaultQueueConfig()),
		BackOffConfig:         configretry.NewDefaultBackOffConfig(),
		CollectorID:           defaultCollectorID[:],
	}
}

// createLogsExporter creates a new log exporter based on this config.
func createLogsExporter(
	ctx context.Context,
	params exporter.Settings,
	cfg component.Config,
) (exp exporter.Logs, err error) {
	t, err := metadata.NewTelemetryBuilder(params.TelemetrySettings)
	if err != nil {
		return nil, fmt.Errorf("create telemetry builder: %w", err)
	}

	c := cfg.(*Config)
	if c.API == chronicleAPI {
		exp, err = newHTTPExporter(c, params, t)
	} else {
		exp, err = newGRPCExporter(c, params, t)
	}
	if err != nil {
		return nil, err
	}
	return exporterhelper.NewLogs(
		ctx,
		params,
		c,
		exp.ConsumeLogs,
		exporterhelper.WithCapabilities(exp.Capabilities()),
		exporterhelper.WithTimeout(c.TimeoutConfig),
		exporterhelper.WithQueue(c.QueueBatchConfig),
		exporterhelper.WithRetry(c.BackOffConfig),
		exporterhelper.WithStart(exp.Start),
		exporterhelper.WithShutdown(exp.Shutdown),
	)
}
