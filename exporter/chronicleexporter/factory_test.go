package googlesecopsexporter

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

func Test_createDefaultConfig(t *testing.T) {
	expectedCfg := &Config{
		TimeoutConfig:         exporterhelper.NewDefaultTimeoutConfig(),
		QueueBatchConfig:      configoptional.Some(exporterhelper.NewDefaultQueueConfig()),
		BackOffConfig:         configretry.NewDefaultBackOffConfig(),
		OverrideLogType:       true,
		BaseURL:               "malachiteingestion-pa.googleapis.com",
		Compression:           "none",
		CollectAgentMetrics:   true,
		API:                   chronicleAPI,
		BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
	}

	actual := createDefaultConfig()
	require.Equal(t, expectedCfg, actual)
}
