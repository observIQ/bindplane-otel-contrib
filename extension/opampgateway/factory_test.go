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

package opampgateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/confignet"
	"go.opentelemetry.io/collector/config/configtls"
	"go.opentelemetry.io/collector/extension/extensiontest"
)

// TestCreateOpAMPGatewayInvalidUpstreamTLSConfig verifies that an invalid
// server.tls configuration (e.g. a ca_file that cannot be read) fails
// extension creation instead of silently being ignored.
func TestCreateOpAMPGatewayInvalidUpstreamTLSConfig(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{
			Endpoint:    "wss://localhost:4320/v1/opamp",
			Connections: 1,
			TLS: configtls.ClientConfig{
				Config: configtls.Config{
					CAFile: "testdata/does-not-exist.pem",
				},
			},
		},
		Listener: confighttp.ServerConfig{
			NetAddr: confignet.AddrConfig{Endpoint: "127.0.0.1:0"},
		},
	}

	factory := NewFactory()
	_, err := factory.Create(context.Background(), extensiontest.NewNopSettings(typ), cfg)
	require.ErrorContains(t, err, "load upstream TLS config")
}
