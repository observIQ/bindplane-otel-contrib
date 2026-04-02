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

package oktareceiver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/okta/okta-sdk-golang/v6/okta"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/plogtest"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap"
)

func newTestClient(t *testing.T, handler http.Handler) *okta.APIClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfg, err := okta.NewConfiguration(
		okta.WithOrgUrl(server.URL),
		okta.WithToken("test-token"),
		okta.WithTestingDisableHttpsCheck(true),
		okta.WithCache(false),
	)
	require.NoError(t, err)
	// WithOrgUrl strips the port via url.Hostname(); restore full host:port for test server
	cfg.Host = server.Listener.Addr().String()
	return okta.NewAPIClient(cfg)
}

func TestStartShutdown(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))

	cfg := createDefaultConfig().(*Config)
	recv := newOktaLogsReceiver(cfg, zap.NewNop(), consumertest.NewNop(), client)

	err := recv.Start(context.Background(), componenttest.NewNopHost())
	require.NoError(t, err)

	err = recv.Shutdown(context.Background())
	require.NoError(t, err)
}

func TestShutdownNoServer(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))

	cfg := createDefaultConfig().(*Config)
	recv := newOktaLogsReceiver(cfg, zap.NewNop(), consumertest.NewNop(), client)
	require.NoError(t, recv.Shutdown(context.Background()))
}

func TestPollBasic(t *testing.T) {
	responseBody, err := os.ReadFile("testdata/oktaResponseBasic.json")
	require.NoError(t, err)

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.String(), "since=")
		require.Contains(t, r.URL.String(), "until=")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))

	cfg := createDefaultConfig().(*Config)
	cfg.Domain = "observiq.okta.com"

	sink := &consumertest.LogsSink{}
	recv := newOktaLogsReceiver(cfg, zap.NewNop(), sink, client)

	err = recv.poll(context.Background())
	require.NoError(t, err)

	logs := sink.AllLogs()
	log := logs[0]

	// golden.WriteLogs(t, "testdata/plog.yaml", log)

	oktaDomain, exist := log.ResourceLogs().At(0).Resource().Attributes().Get("okta.domain")
	require.True(t, exist)
	require.Equal(t, "observiq.okta.com", oktaDomain.Str())

	expected, err := golden.ReadLogs("testdata/plog.yaml")
	require.NoError(t, err)
	require.NoError(t, plogtest.CompareLogs(expected, log, plogtest.IgnoreObservedTimestamp()))

	require.Equal(t, 2, log.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().Len())
}

func TestPollError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{}`))
	}))

	cfg := createDefaultConfig().(*Config)
	cfg.Domain = "observiq.okta.com"

	sink := &consumertest.LogsSink{}
	recv := newOktaLogsReceiver(cfg, zap.NewNop(), sink, client)

	err := recv.poll(context.Background())
	require.Error(t, err)
	require.Empty(t, sink.AllLogs())
}

func TestPollEmpty(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))

	cfg := createDefaultConfig().(*Config)
	cfg.Domain = "observiq.okta.com"

	sink := &consumertest.LogsSink{}
	recv := newOktaLogsReceiver(cfg, zap.NewNop(), sink, client)

	err := recv.poll(context.Background())
	require.NoError(t, err)
	require.Empty(t, sink.AllLogs())
}

func TestPollLargeResponse(t *testing.T) {
	responseBody, err := os.ReadFile("testdata/oktaResponse1000Logs.json")
	require.NoError(t, err)

	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))

	cfg := createDefaultConfig().(*Config)
	cfg.Domain = "observiq.okta.com"

	sink := &consumertest.LogsSink{}
	recv := newOktaLogsReceiver(cfg, zap.NewNop(), sink, client)

	err = recv.poll(context.Background())
	require.NoError(t, err)

	logs := sink.AllLogs()
	require.Equal(t, 1000, logs[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().Len())
}
