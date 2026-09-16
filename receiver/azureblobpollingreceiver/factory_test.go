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

package azureblobpollingreceiver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

func TestFactory_createReceivers(t *testing.T) {
	validCfg := func() *Config {
		cfg := createDefaultConfig().(*Config)
		cfg.ConnectionString = "DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=+idLkHYcL0MUWIKYHm2j4Q==;EndpointSuffix=core.windows.net"
		cfg.Container = "c"
		cfg.PollInterval = time.Minute
		return cfg
	}
	params := receivertest.NewNopSettings(receivertest.NopType)

	t.Run("logs receiver builds with telemetry wired", func(t *testing.T) {
		r, err := createLogsReceiver(context.Background(), params, validCfg(), consumertest.NewNop())
		require.NoError(t, err)
		require.NotNil(t, r.(*pollingReceiver).telemetryBuilder)
	})
	t.Run("metrics receiver builds with telemetry wired", func(t *testing.T) {
		r, err := createMetricsReceiver(context.Background(), params, validCfg(), consumertest.NewNop())
		require.NoError(t, err)
		require.NotNil(t, r.(*pollingReceiver).telemetryBuilder)
	})
	t.Run("traces receiver builds with telemetry wired", func(t *testing.T) {
		r, err := createTracesReceiver(context.Background(), params, validCfg(), consumertest.NewNop())
		require.NoError(t, err)
		require.NotNil(t, r.(*pollingReceiver).telemetryBuilder)
	})

	t.Run("a construction error is propagated before telemetry wiring", func(t *testing.T) {
		badCfg := func() *Config {
			cfg := validCfg()
			cfg.FilenamePattern = "(" // invalid regex -> newPollingReceiver fails
			return cfg
		}
		_, err := createLogsReceiver(context.Background(), params, badCfg(), consumertest.NewNop())
		require.ErrorContains(t, err, "filename pattern")
		_, err = createMetricsReceiver(context.Background(), params, badCfg(), consumertest.NewNop())
		require.ErrorContains(t, err, "filename pattern")
		_, err = createTracesReceiver(context.Background(), params, badCfg(), consumertest.NewNop())
		require.ErrorContains(t, err, "filename pattern")
	})
}
