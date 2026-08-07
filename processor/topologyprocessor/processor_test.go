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

package topologyprocessor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/plogtest"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/pmetrictest"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/ptracetest"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

func TestProcessor_Logs(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "1")

	tmp, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
	}, processorID)
	require.NoError(t, err)

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	ctx := client.NewContext(context.Background(), client.Info{
		Metadata: client.NewMetadata(map[string][]string{
			accountIDHeader:      {"myAccountID1"},
			organizationIDHeader: {"myOrgID1"},
			configurationHeader:  {"myConfigName1"},
			resourceNameHeader:   {"myResourceName1"},
		}),
	})
	processedLogs, err := tmp.processLogs(ctx, logs)
	require.NoError(t, err)

	// Output logs should be the same as input logs (passthrough check)
	require.NoError(t, plogtest.CompareLogs(logs, processedLogs))

	// validate that upsert route was performed
	require.True(t, tmp.topology.GatewaySource.AccountID == "myAccountID")
	require.True(t, tmp.topology.GatewaySource.OrganizationID == "myOrgID")
	require.True(t, tmp.topology.GatewaySource.Configuration == "myConfigName")
	ci := GatewayInfo{
		Configuration:  "myConfigName1",
		AccountID:      "myAccountID1",
		OrganizationID: "myOrgID1",
		GatewayID:      "myResourceName1",
	}
	_, ok := tmp.topology.RouteTable[ci]
	require.True(t, ok)
}

func TestProcessor_Metrics(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "1")

	tmp, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
	}, processorID)
	require.NoError(t, err)

	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	ctx := client.NewContext(context.Background(), client.Info{
		Metadata: client.NewMetadata(map[string][]string{
			accountIDHeader:      {"myAccountID1"},
			organizationIDHeader: {"myOrgID1"},
			configurationHeader:  {"myConfigName1"},
			resourceNameHeader:   {"myResourceName1"},
		}),
	})

	processedMetrics, err := tmp.processMetrics(ctx, metrics)
	require.NoError(t, err)

	// Output metrics should be the same as input logs (passthrough check)
	require.NoError(t, pmetrictest.CompareMetrics(metrics, processedMetrics))

	// validate that upsert route was performed
	require.True(t, tmp.topology.GatewaySource.AccountID == "myAccountID")
	require.True(t, tmp.topology.GatewaySource.OrganizationID == "myOrgID")
	require.True(t, tmp.topology.GatewaySource.Configuration == "myConfigName")
	ci := GatewayInfo{
		Configuration:  "myConfigName1",
		AccountID:      "myAccountID1",
		OrganizationID: "myOrgID1",
		GatewayID:      "myResourceName1",
	}
	_, ok := tmp.topology.RouteTable[ci]
	require.True(t, ok)
}

func TestProcessor_Traces(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "1")

	tmp, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
	}, processorID)
	require.NoError(t, err)

	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	ctx := client.NewContext(context.Background(), client.Info{
		Metadata: client.NewMetadata(map[string][]string{
			accountIDHeader:      {"myAccountID1"},
			organizationIDHeader: {"myOrgID1"},
			configurationHeader:  {"myConfigName1"},
			resourceNameHeader:   {"myResourceName1"},
		}),
	})

	processedTraces, err := tmp.processTraces(ctx, traces)
	require.NoError(t, err)

	// Output traces should be the same as input logs (passthrough check)
	require.NoError(t, ptracetest.CompareTraces(traces, processedTraces))

	// validate that upsert route was performed
	require.True(t, tmp.topology.GatewaySource.AccountID == "myAccountID")
	require.True(t, tmp.topology.GatewaySource.OrganizationID == "myOrgID")
	require.True(t, tmp.topology.GatewaySource.Configuration == "myConfigName")
	ci := GatewayInfo{
		Configuration:  "myConfigName1",
		AccountID:      "myAccountID1",
		OrganizationID: "myOrgID1",
		GatewayID:      "myResourceName1",
	}
	_, ok := tmp.topology.RouteTable[ci]
	require.True(t, ok)
}

func TestProcessor_MissingHeader(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "1")

	tmp, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
	}, processorID)
	require.NoError(t, err)

	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	ctx := client.NewContext(context.Background(), client.Info{
		Metadata: client.NewMetadata(map[string][]string{
			organizationIDHeader: {"myOrgID1"},
			configurationHeader:  {"myConfigName1"},
			resourceNameHeader:   {"myResourceName1"},
		}),
	})

	processedTraces, err := tmp.processTraces(ctx, traces)
	require.NoError(t, err)

	// Output traces should be the same as input logs (passthrough check)
	require.NoError(t, ptracetest.CompareTraces(traces, processedTraces))

	// validate that upsert route was not performed
	require.Equal(t, 0, len(tmp.topology.RouteTable))
}

// Test 2 instances with the same processor ID
func TestProcessor_Logs_TwoInstancesSameID(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "1")

	tmp1, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
	}, processorID)
	require.NoError(t, err)

	tmp2, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID2",
		AccountID:      "myAccountID2",
		Configuration:  "myConfigName2",
	}, processorID)
	require.NoError(t, err)

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	_, err = tmp1.processLogs(context.Background(), logs)
	require.NoError(t, err)

	_, err = tmp2.processLogs(context.Background(), logs)
	require.NoError(t, err)
}

func TestProcessor_Logs_TwoInstancesDifferentID(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "1")
	processorID2 := component.MustNewIDWithName("topology", "2")

	tmp1, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
	}, processorID)
	require.NoError(t, err)

	tmp2, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID2",
		AccountID:      "myAccountID2",
		Configuration:  "myConfigName2",
	}, processorID2)
	require.NoError(t, err)

	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	_, err = tmp1.processLogs(context.Background(), logs)
	require.NoError(t, err)

	_, err = tmp2.processLogs(context.Background(), logs)
	require.NoError(t, err)
}

func TestProcessor_ReportsTopologyOverOpAMP(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "1")
	opampID := component.MustNewID("opamp")

	tp, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
		OpAMP:          opampID,
		Global:         &GlobalConfig{Interval: 100 * time.Millisecond},
	}, processorID)
	require.NoError(t, err)

	mockOpamp := &mockOpAMPExtension{msgChan: make(chan *protobufs.CustomMessage, 1)}
	mh := mockHost{
		extMap: map[component.ID]component.Component{
			opampID: mockOpamp,
		},
	}

	// Ingest before starting so the report loop's first tick has routes to send.
	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	ctx := client.NewContext(context.Background(), client.Info{
		Metadata: client.NewMetadata(map[string][]string{
			accountIDHeader:      {"myAccountID1"},
			organizationIDHeader: {"myOrgID1"},
			configurationHeader:  {"myConfigName1"},
			resourceNameHeader:   {"myResourceName1"},
		}),
	})
	_, err = tp.processLogs(ctx, logs)
	require.NoError(t, err)

	require.NoError(t, tp.start(context.Background(), mh))
	require.Equal(t, ReportTopologyCapability, mockOpamp.capability)

	require.Eventually(t, func() bool {
		return mockOpamp.GotMessage()
	}, 5*time.Second, 10*time.Millisecond)

	require.Equal(t, ReportTopologyType, mockOpamp.sentMessageType)

	decoded, err := snappy.Decode(nil, mockOpamp.sentMessage)
	require.NoError(t, err)

	var infos []TopoInfo
	require.NoError(t, json.Unmarshal(decoded, &infos))
	require.Len(t, infos, 1)

	require.Equal(t, GatewayInfo{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
		GatewayID:      "1",
	}, infos[0].GatewaySource)
	require.Len(t, infos[0].GatewayDestinations, 1)
	require.Equal(t, GatewayInfo{
		OrganizationID: "myOrgID1",
		AccountID:      "myAccountID1",
		Configuration:  "myConfigName1",
		GatewayID:      "myResourceName1",
	}, infos[0].GatewayDestinations[0].Gateway)

	require.NoError(t, tp.shutdown(context.Background()))

	// The last processor to shut down tears the shared reporter down.
	reporterMux.Lock()
	require.Nil(t, reporter)
	reporterMux.Unlock()
}

// Test that a zero interval disables reporting, like the bindplane extension.
func TestProcessor_OpAMPZeroIntervalDisablesReporting(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "disabled")
	opampID := component.MustNewID("opamp")

	tp, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
		OpAMP:          opampID,
		Global:         &GlobalConfig{Interval: 0},
	}, processorID)
	require.NoError(t, err)

	mockOpamp := &mockOpAMPExtension{msgChan: make(chan *protobufs.CustomMessage, 1)}
	mh := mockHost{
		extMap: map[component.ID]component.Component{
			opampID: mockOpamp,
		},
	}

	require.NoError(t, tp.start(context.Background(), mh))

	// No capability is registered and the reporter stays dormant.
	require.Equal(t, 0, mockOpamp.RegisterCount())
	reporterMux.Lock()
	require.NotNil(t, reporter)
	require.Nil(t, reporter.handler)
	reporterMux.Unlock()

	// The opamp extension must still exist, even with reporting disabled.
	tp2, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
		OpAMP:          opampID,
		Global:         &GlobalConfig{Interval: 0},
	}, component.MustNewIDWithName("topology", "disabled2"))
	require.NoError(t, err)
	require.Error(t, tp2.start(context.Background(), mockHost{}))

	require.NoError(t, tp.shutdown(context.Background()))
	require.NoError(t, tp2.shutdown(context.Background()))
}

// Test that multiple processors report through a single shared reporter as one
// aggregated message, like the bindplane extension does.
func TestProcessor_AggregatesTopologyOverOpAMP(t *testing.T) {
	opampID := component.MustNewID("opamp")

	processorID1 := component.MustNewIDWithName("topology", "agg1")
	processorID2 := component.MustNewIDWithName("topology", "agg2")

	// Only the first processor carries the `global` block; the second one's
	// topology state must still feed the shared reporter.
	tp1, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
		OpAMP:          opampID,
		Global:         &GlobalConfig{Interval: 100 * time.Millisecond},
	}, processorID1)
	require.NoError(t, err)
	tp2, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
		OpAMP:          opampID,
	}, processorID2)
	require.NoError(t, err)

	// A processor without `opamp` does not feed the reporter and must not
	// appear in the aggregated message.
	tp3, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
	}, component.MustNewIDWithName("topology", "agg3"))
	require.NoError(t, err)

	mockOpamp := &mockOpAMPExtension{msgChan: make(chan *protobufs.CustomMessage, 1)}
	mh := mockHost{
		extMap: map[component.ID]component.Component{
			opampID: mockOpamp,
		},
	}

	// Ingest before starting so the report loop's first tick has both
	// processors' routes to send.
	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	ctx := client.NewContext(context.Background(), client.Info{
		Metadata: client.NewMetadata(map[string][]string{
			accountIDHeader:      {"myAccountID1"},
			organizationIDHeader: {"myOrgID1"},
			configurationHeader:  {"myConfigName1"},
			resourceNameHeader:   {"myResourceName1"},
		}),
	})
	_, err = tp1.processLogs(ctx, logs)
	require.NoError(t, err)
	_, err = tp2.processLogs(ctx, logs)
	require.NoError(t, err)
	_, err = tp3.processLogs(ctx, logs)
	require.NoError(t, err)

	require.NoError(t, tp1.start(context.Background(), mh))
	require.NoError(t, tp2.start(context.Background(), mh))
	require.NoError(t, tp3.start(context.Background(), mh))

	// Both opamp-configured processors share one reporter: the capability is
	// registered once.
	require.Equal(t, 1, mockOpamp.RegisterCount())

	require.Eventually(t, func() bool {
		return mockOpamp.GotMessage()
	}, 5*time.Second, 10*time.Millisecond)

	decoded, err := snappy.Decode(nil, mockOpamp.sentMessage)
	require.NoError(t, err)

	// One message contains the topology of both processors.
	var infos []TopoInfo
	require.NoError(t, json.Unmarshal(decoded, &infos))
	require.Len(t, infos, 2)

	seenSources := map[string]struct{}{}
	for _, info := range infos {
		seenSources[info.GatewaySource.GatewayID] = struct{}{}
		require.Len(t, info.GatewayDestinations, 1)
	}
	require.Contains(t, seenSources, "agg1")
	require.Contains(t, seenSources, "agg2")
	require.NotContains(t, seenSources, "agg3")

	// The reporter survives until the last opamp-configured processor shuts
	// down.
	require.NoError(t, tp3.shutdown(context.Background()))
	require.NoError(t, tp1.shutdown(context.Background()))
	reporterMux.Lock()
	require.NotNil(t, reporter)
	reporterMux.Unlock()

	require.NoError(t, tp2.shutdown(context.Background()))
	reporterMux.Lock()
	require.Nil(t, reporter)
	reporterMux.Unlock()
}

// Test that when more than one processor carries a global block, the last one
// to start reconfigures the reporter.
func TestProcessor_GlobalLastOneWins(t *testing.T) {
	opampID := component.MustNewID("opamp")

	tp1, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
		OpAMP:          opampID,
		Global:         &GlobalConfig{Interval: 100 * time.Millisecond},
	}, component.MustNewIDWithName("topology", "lastwins1"))
	require.NoError(t, err)
	tp2, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
		OpAMP:          opampID,
		Global:         &GlobalConfig{Interval: 100 * time.Millisecond},
	}, component.MustNewIDWithName("topology", "lastwins2"))
	require.NoError(t, err)

	mockOpamp := &mockOpAMPExtension{msgChan: make(chan *protobufs.CustomMessage, 1)}
	mh := mockHost{
		extMap: map[component.ID]component.Component{
			opampID: mockOpamp,
		},
	}

	require.NoError(t, tp1.start(context.Background(), mh))
	require.NoError(t, tp2.start(context.Background(), mh))

	// The second global block reconfigured the reporter: the capability was
	// registered twice (once per configuration).
	require.Equal(t, 2, mockOpamp.RegisterCount())

	require.NoError(t, tp1.shutdown(context.Background()))
	require.NoError(t, tp2.shutdown(context.Background()))
}

type mockOpAMPExtension struct {
	msgChan chan *protobufs.CustomMessage

	capability    string
	registerCount int

	gotMessageMux   sync.Mutex
	gotMessage      bool
	sentMessageType string
	sentMessage     []byte
}

func (m *mockOpAMPExtension) Start(_ context.Context, _ component.Host) error { return nil }

func (m *mockOpAMPExtension) Shutdown(_ context.Context) error { return nil }

func (m *mockOpAMPExtension) Register(capability string, _ ...opampcustommessages.CustomCapabilityRegisterOption) (handler opampcustommessages.CustomCapabilityHandler, err error) {
	m.gotMessageMux.Lock()
	defer m.gotMessageMux.Unlock()

	m.capability = capability
	m.registerCount++
	return m, nil
}

func (m *mockOpAMPExtension) RegisterCount() int {
	m.gotMessageMux.Lock()
	defer m.gotMessageMux.Unlock()

	return m.registerCount
}

func (m *mockOpAMPExtension) Message() <-chan *protobufs.CustomMessage {
	return m.msgChan
}

func (m *mockOpAMPExtension) SendMessage(messageType string, message []byte) (messageSendingChannel chan struct{}, err error) {
	m.gotMessageMux.Lock()
	defer m.gotMessageMux.Unlock()

	if m.gotMessage {
		return
	}
	m.gotMessage = true

	m.sentMessageType = messageType
	m.sentMessage = message
	return
}

func (m *mockOpAMPExtension) GotMessage() bool {
	m.gotMessageMux.Lock()
	defer m.gotMessageMux.Unlock()

	return m.gotMessage
}

func (m *mockOpAMPExtension) Unregister() {}

func TestProcessor_RegistersWithBindplaneExtension(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "bindplane_ext_fallback")
	bindplaneID := component.MustNewID("bindplane")

	tp, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID:     "myOrgID",
		AccountID:          "myAccountID",
		Configuration:      "myConfigName",
		BindplaneExtension: &bindplaneID,
	}, processorID)
	require.NoError(t, err)

	reg := NewResettableTopologyRegistry()
	mh := mockHost{
		extMap: map[component.ID]component.Component{
			bindplaneID: mockTopologyRegistry{reg},
		},
	}

	require.NoError(t, tp.start(context.Background(), mh))

	// Registering the same processor ID again through the extension errors,
	// proving the first registration landed in the extension's registry.
	require.Error(t, reg.RegisterTopologyState(processorID.String(), tp.topology))

	require.NoError(t, tp.shutdown(context.Background()))
}

func TestProcessor_RegistersWithAgentRegistry(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "v1_fallback")

	tp, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
	}, processorID)
	require.NoError(t, err)

	// Neither opamp nor bindplane_extension set: registers with the package-level
	// registry read by the v1 bindplane agent.
	require.NoError(t, tp.start(context.Background(), mockHost{}))
	require.Error(t, BindplaneAgentTopologyRegistry.RegisterTopologyState(processorID.String(), tp.topology))

	// A second processor with the same ID (e.g. after a config reload without a
	// registry reset) must not fail startup.
	tp2, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID: "myOrgID",
		AccountID:      "myAccountID",
		Configuration:  "myConfigName",
	}, processorID)
	require.NoError(t, err)
	require.NoError(t, tp2.start(context.Background(), mockHost{}))

	require.NoError(t, tp.shutdown(context.Background()))
	require.NoError(t, tp2.shutdown(context.Background()))
}

type mockTopologyRegistry struct {
	*ResettableTopologyRegistry
}

func (mockTopologyRegistry) Start(_ context.Context, _ component.Host) error { return nil }
func (mockTopologyRegistry) Shutdown(_ context.Context) error                { return nil }

func TestProcessor_BindplaneExtensionMissing_FallsBackToAgentRegistry(t *testing.T) {
	processorID := component.MustNewIDWithName("topology", "missing_ext_fallback")
	bindplaneID := component.MustNewID("bindplane")

	tp, err := newTopologyProcessor(zap.NewNop(), &Config{
		OrganizationID:     "myOrgID",
		AccountID:          "myAccountID",
		Configuration:      "myConfigName",
		BindplaneExtension: &bindplaneID,
	}, processorID)
	require.NoError(t, err)

	// Old Bindplane servers render bindplane_extension without instantiating the
	// extension; startup must succeed and fall back to the v1 agent registry.
	require.NoError(t, tp.start(context.Background(), mockHost{}))
	require.Error(t, BindplaneAgentTopologyRegistry.RegisterTopologyState(processorID.String(), tp.topology))

	require.NoError(t, tp.shutdown(context.Background()))
}
