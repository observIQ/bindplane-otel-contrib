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

package logtypedetectionprocessor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/metadata"
)

var opampID = component.MustNewID("opamp")

// mockOpAMPExtension stands in for an opamp server.
type mockOpAMPExtension struct {
	component.Component

	msgChan chan *protobufs.CustomMessage

	reply        []MatcherConfig
	replyVersion string
	replyFor     component.ID
	autoReply    bool
	upToDate     bool
	unregister   bool

	mux      sync.Mutex
	requests []matchersMessage
}

func (m *mockOpAMPExtension) Start(_ context.Context, _ component.Host) error { return nil }
func (m *mockOpAMPExtension) Shutdown(_ context.Context) error                { return nil }

func (m *mockOpAMPExtension) Register(_ string, _ ...opampcustommessages.CustomCapabilityRegisterOption) (opampcustommessages.CustomCapabilityHandler, error) {
	return m, nil
}

func (m *mockOpAMPExtension) Message() <-chan *protobufs.CustomMessage { return m.msgChan }

func (m *mockOpAMPExtension) SendMessage(messageType string, payload []byte) (chan struct{}, error) {
	var request matchersMessage
	if err := yaml.Unmarshal(payload, &request); err != nil {
		panic(err)
	}

	m.mux.Lock()
	m.requests = append(m.requests, request)
	m.mux.Unlock()

	if !m.autoReply || messageType != requestMatchersType {
		return nil, nil
	}

	if m.upToDate {
		m.send(matchersUpToDateType, matchersMessage{Processor: m.replyFor, Version: m.replyVersion})
		return nil, nil
	}

	m.sendMatchers(m.replyFor, m.replyVersion, m.reply)
	return nil, nil
}

func (m *mockOpAMPExtension) sendMatchers(processor component.ID, version string, matchers []MatcherConfig) {
	m.send(updateMatchersType, matchersMessage{Processor: processor, Version: version, Matchers: matchers})
}

func (m *mockOpAMPExtension) send(messageType string, msg matchersMessage) {
	data, err := yaml.Marshal(msg)
	if err != nil {
		panic(err)
	}
	m.msgChan <- &protobufs.CustomMessage{Type: messageType, Data: data}
}

func (m *mockOpAMPExtension) Unregister() { m.unregister = true }

func (m *mockOpAMPExtension) sentRequests() []matchersMessage {
	m.mux.Lock()
	defer m.mux.Unlock()
	return append([]matchersMessage(nil), m.requests...)
}

func newOpAMPProcessor(t *testing.T, cfg *Config, id component.ID) *logTypeDetectionProcessor {
	t.Helper()
	tb, err := metadata.NewTelemetryBuilder(componenttest.NewNopTelemetrySettings())
	require.NoError(t, err)
	p, err := newLogTypeDetectionProcessor(cfg, id, zap.NewNop(), tb)
	require.NoError(t, err)
	return p
}

func opampConfig(matchers []MatcherConfig) *Config {
	cfg := createDefaultConfig().(*Config)
	cfg.OpAMP = &OpAMPConfig{Extension: opampID, RequestTimeout: defaultOpAMPRequestTimeout}
	cfg.Matchers = matchers
	return cfg
}

// Matchers are requested in the background so startup never waits on the server.
func TestOpAMPMatchersRequestedOnStart(t *testing.T) {
	ctx := context.Background()
	id := component.MustNewID("logtypedetection")

	mock := &mockOpAMPExtension{
		msgChan:      make(chan *protobufs.CustomMessage, 1),
		autoReply:    true,
		replyFor:     id,
		replyVersion: "1.0.0",
		reply:        []MatcherConfig{{Name: "k8s_audit", Method: MatcherTypeStartsWith, Value: `{"kind"`}},
	}
	host := &testHost{components: map[component.ID]component.Component{opampID: mock}}

	p := newOpAMPProcessor(t, opampConfig(nil), id)
	require.NoError(t, p.start(ctx, host))
	defer func() { require.NoError(t, p.stop(ctx)) }()

	waitForMatchers(t, p)
	requests := mock.sentRequests()
	require.Len(t, requests, 1)
	require.Equal(t, id, requests[0].Processor)
	require.Empty(t, requests[0].Version, "a first run has no version to report")
	require.Empty(t, requests[0].MaxVersion, "no ceiling unless opamp::matchers_version is set")

	out, err := p.processLogs(ctx, logsFromBodies(`{"kind":"Event"}`))
	require.NoError(t, err)
	logType, ok := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().Get("log_type")
	require.True(t, ok)
	require.Equal(t, "k8s_audit", logType.Str())
}

// Server matchers merge with configured ones and the combined set is priority ordered.
func TestOpAMPMatchersMergeWithConfig(t *testing.T) {
	ctx := context.Background()
	id := component.MustNewID("logtypedetection")

	mock := &mockOpAMPExtension{
		msgChan:      make(chan *protobufs.CustomMessage, 1),
		autoReply:    true,
		replyFor:     id,
		replyVersion: "1.0.0",
		reply:        []MatcherConfig{{Name: "from_server", Priority: new(1), Method: MatcherTypeStartsWith, Value: `{`}},
	}
	host := &testHost{components: map[component.ID]component.Component{opampID: mock}}

	cfg := opampConfig([]MatcherConfig{
		{Name: "from_config", Priority: new(0), Method: MatcherTypeStartsWith, Value: `{"a"`},
		{Name: "from_config_last", Priority: new(2), Method: MatcherTypeStartsWith, Value: `{`},
	})

	p := newOpAMPProcessor(t, cfg, id)
	require.NoError(t, p.start(ctx, host))
	defer func() { require.NoError(t, p.stop(ctx)) }()

	waitForMatchers(t, p)
	require.Equal(t, []string{"from_config", "from_server", "from_config_last"}, matcherNames(p))

	out, err := p.processLogs(ctx, logsFromBodies(`{"beta":1}`))
	require.NoError(t, err)
	logType, ok := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().Get("log_type")
	require.True(t, ok)
	require.Equal(t, "from_server", logType.Str())
}

// A late push replaces the matcher set and invalidates log types detected under the old one.
func TestOpAMPMatchersUpdateInvalidatesCache(t *testing.T) {
	ctx := context.Background()
	id := component.MustNewID("logtypedetection")

	mock := &mockOpAMPExtension{
		msgChan:      make(chan *protobufs.CustomMessage, 1),
		autoReply:    true,
		replyFor:     id,
		replyVersion: "1.0.0",
		reply:        []MatcherConfig{{Name: "nginx", Method: MatcherTypeStartsWith, Value: "GET "}},
	}
	host := &testHost{components: map[component.ID]component.Component{opampID: mock}}

	p := newOpAMPProcessor(t, opampConfig(nil), id)
	require.NoError(t, p.start(ctx, host))
	defer func() { require.NoError(t, p.stop(ctx)) }()

	waitForMatchers(t, p)
	_, err := p.processLogs(ctx, logsFromBodies("GET /index.html 200"))
	require.NoError(t, err)
	require.Equal(t, 1, p.logTypes.Len())

	mock.sendMatchers(id, "1.1.0", []MatcherConfig{{Name: "nginx_access", Method: MatcherTypeStartsWith, Value: "GET "}})
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, []string{"nginx_access"}, matcherNames(p))
	}, time.Second, 5*time.Millisecond)
	require.Equal(t, 0, p.logTypes.Len())

	out, err := p.processLogs(ctx, logsFromBodies("GET /index.html 200"))
	require.NoError(t, err)
	logType, ok := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().Get("log_type")
	require.True(t, ok)
	require.Equal(t, "nginx_access", logType.Str())
}

// A message naming a different processor is ignored.
func TestOpAMPMatchersForOtherProcessorIgnored(t *testing.T) {
	ctx := context.Background()
	id := component.MustNewID("logtypedetection")

	mock := &mockOpAMPExtension{
		msgChan:      make(chan *protobufs.CustomMessage, 1),
		autoReply:    true,
		replyFor:     component.MustNewIDWithName("logtypedetection", "other"),
		replyVersion: "1.0.0",
		reply:        []MatcherConfig{{Name: "not_mine", Method: MatcherTypeStartsWith, Value: "GET "}},
	}
	host := &testHost{components: map[component.ID]component.Component{opampID: mock}}

	cfg := opampConfig([]MatcherConfig{{Name: "mine", Method: MatcherTypeStartsWith, Value: "GET "}})
	cfg.OpAMP.RequestTimeout = 100 * time.Millisecond

	p := newOpAMPProcessor(t, cfg, id)
	require.NoError(t, p.start(ctx, host))
	defer func() { require.NoError(t, p.stop(ctx)) }()

	waitForMatchers(t, p)
	require.Equal(t, []string{"mine"}, matcherNames(p))
}

// An invalid matcher from the server is rejected without disturbing the current set.
func TestOpAMPInvalidMatchersRejected(t *testing.T) {
	ctx := context.Background()
	id := component.MustNewID("logtypedetection")

	mock := &mockOpAMPExtension{
		msgChan:      make(chan *protobufs.CustomMessage, 1),
		autoReply:    true,
		replyFor:     id,
		replyVersion: "1.0.0",
		reply:        []MatcherConfig{{Name: "bad", Method: MatcherTypeRegex, Value: "("}},
	}
	host := &testHost{components: map[component.ID]component.Component{opampID: mock}}

	cfg := opampConfig([]MatcherConfig{{Name: "mine", Method: MatcherTypeStartsWith, Value: "GET "}})
	cfg.OpAMP.RequestTimeout = 100 * time.Millisecond

	p := newOpAMPProcessor(t, cfg, id)
	require.NoError(t, p.start(ctx, host))
	defer func() { require.NoError(t, p.stop(ctx)) }()

	waitForMatchers(t, p)
	require.Equal(t, []string{"mine"}, matcherNames(p))
}

func TestOpAMPMissingExtension(t *testing.T) {
	p := newOpAMPProcessor(t, opampConfig(nil), component.MustNewID("logtypedetection"))
	err := p.start(context.Background(), &testHost{components: map[component.ID]component.Component{}})
	require.ErrorContains(t, err, `opamp extension "opamp" does not exist`)
	require.NoError(t, p.stop(context.Background()))
}

func waitForMatchers(t *testing.T, p *logTypeDetectionProcessor) {
	t.Helper()
	select {
	case <-p.matchersReady:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the matcher exchange to finish")
	}
}

func matcherNames(p *logTypeDetectionProcessor) []string {
	p.matcherMux.RLock()
	defer p.matcherMux.RUnlock()

	names := make([]string, 0, len(p.matchers))
	for _, m := range p.matchers {
		names = append(names, m.Name())
	}
	return names
}

// The persisted fingerprint map is restored alongside the stored matchers on restart.
func TestOpAMPRestartKeepsPersistedLogTypes(t *testing.T) {
	ctx := context.Background()
	id := component.MustNewID("logtypedetection")

	factory := filestorage.NewFactory()
	storageCfg := factory.CreateDefaultConfig().(*filestorage.Config)
	storageCfg.Directory = t.TempDir()
	storageID := component.NewIDWithName(component.MustNewType("file_storage"), "test")
	ext, err := factory.Create(ctx, extension.Settings{ID: storageID, TelemetrySettings: componenttest.NewNopTelemetrySettings()}, storageCfg)
	require.NoError(t, err)
	require.NoError(t, ext.Start(ctx, componenttest.NewNopHost()))
	defer func() { require.NoError(t, ext.Shutdown(ctx)) }()

	serverMatchers := []MatcherConfig{{Name: "nginx", Method: MatcherTypeStartsWith, Value: "GET "}}
	newMock := func() *mockOpAMPExtension {
		return &mockOpAMPExtension{
			msgChan:      make(chan *protobufs.CustomMessage, 1),
			autoReply:    true,
			replyFor:     id,
			replyVersion: "1.0.0",
			reply:        serverMatchers,
		}
	}

	newRun := func(mock *mockOpAMPExtension) (*logTypeDetectionProcessor, component.Host) {
		cfg := opampConfig(nil)
		cfg.StorageID = &storageID
		host := &testHost{components: map[component.ID]component.Component{
			storageID: ext,
			opampID:   mock,
		}}
		return newOpAMPProcessor(t, cfg, id), host
	}

	first, host := newRun(newMock())
	require.NoError(t, first.start(ctx, host))
	waitForMatchers(t, first)
	_, err = first.processLogs(ctx, logsFromBodies("GET /index.html 200"))
	require.NoError(t, err)
	require.Equal(t, 1, first.logTypes.Len())
	require.NoError(t, first.stop(ctx))

	second, host := newRun(newMock())
	require.NoError(t, second.start(ctx, host))
	defer func() { require.NoError(t, second.stop(ctx)) }()

	waitForMatchers(t, second)
	require.Equal(t, []string{"nginx"}, matcherNames(second))
	require.Equal(t, 1, second.logTypes.Len(), "persisted log types should be restored once the server's matchers are in place")
}

// Log types detected under other matchers are discarded on restart.
func TestOpAMPTimeoutResolvesPendingLogTypes(t *testing.T) {
	ctx := context.Background()
	id := component.MustNewID("logtypedetection")

	factory := filestorage.NewFactory()
	storageCfg := factory.CreateDefaultConfig().(*filestorage.Config)
	storageCfg.Directory = t.TempDir()
	storageID := component.NewIDWithName(component.MustNewType("file_storage"), "test")
	ext, err := factory.Create(ctx, extension.Settings{ID: storageID, TelemetrySettings: componenttest.NewNopTelemetrySettings()}, storageCfg)
	require.NoError(t, err)
	require.NoError(t, ext.Start(ctx, componenttest.NewNopHost()))
	defer func() { require.NoError(t, ext.Shutdown(ctx)) }()

	matchers := []MatcherConfig{{Name: "nginx", Method: MatcherTypeStartsWith, Value: "GET "}}
	newRun := func(cfg *Config) (*logTypeDetectionProcessor, component.Host) {
		cfg.StorageID = &storageID
		host := &testHost{components: map[component.ID]component.Component{
			storageID: ext,
			opampID:   &mockOpAMPExtension{msgChan: make(chan *protobufs.CustomMessage, 1)},
		}}
		return newOpAMPProcessor(t, cfg, id), host
	}

	first, host := newRun(createDefaultConfig().(*Config))
	require.NoError(t, first.start(ctx, host))
	_, err = first.processLogs(ctx, logsFromBodies("GET /index.html 200"))
	require.NoError(t, err)
	require.Equal(t, 1, first.logTypes.Len())
	require.NoError(t, first.stop(ctx))

	cfg := opampConfig(matchers)
	cfg.OpAMP.RequestTimeout = 50 * time.Millisecond
	second, host := newRun(cfg)
	require.NoError(t, second.start(ctx, host))
	defer func() { require.NoError(t, second.stop(ctx)) }()

	waitForMatchers(t, second)
	require.Equal(t, 0, second.logTypes.Len(), "detected under different matchers, so discarded")
}

// matcherStorageHost sets up a storage extension plus a mock opamp server.
func matcherStorageHost(t *testing.T, mock *mockOpAMPExtension) (component.Host, *component.ID) {
	t.Helper()
	ctx := context.Background()

	factory := filestorage.NewFactory()
	storageCfg := factory.CreateDefaultConfig().(*filestorage.Config)
	storageCfg.Directory = t.TempDir()
	storageID := component.NewIDWithName(component.MustNewType("file_storage"), "matchers")
	ext, err := factory.Create(ctx, extension.Settings{ID: storageID, TelemetrySettings: componenttest.NewNopTelemetrySettings()}, storageCfg)
	require.NoError(t, err)
	require.NoError(t, ext.Start(ctx, componenttest.NewNopHost()))
	t.Cleanup(func() { require.NoError(t, ext.Shutdown(ctx)) })

	return &testHost{components: map[component.ID]component.Component{
		storageID: ext,
		opampID:   mock,
	}}, &storageID
}

func versionedMock(id component.ID, version string, matchers []MatcherConfig) *mockOpAMPExtension {
	return &mockOpAMPExtension{
		msgChan:      make(chan *protobufs.CustomMessage, 1),
		autoReply:    true,
		replyFor:     id,
		replyVersion: version,
		reply:        matchers,
	}
}

// Stored matchers are used on restart and their version is reported to the server.
func TestOpAMPStoredMatchersReusedAcrossRestart(t *testing.T) {
	ctx := context.Background()
	id := component.MustNewID("logtypedetection")
	matchers := []MatcherConfig{{Name: "nginx", Method: MatcherTypeStartsWith, Value: "GET "}}

	first := versionedMock(id, "1.2.3", matchers)
	host, storageID := matcherStorageHost(t, first)

	cfg := opampConfig(nil)
	cfg.StorageID = storageID

	p := newOpAMPProcessor(t, cfg, id)
	require.NoError(t, p.start(ctx, host))
	waitForMatchers(t, p)
	require.Equal(t, []string{"nginx"}, matcherNames(p))
	require.Equal(t, "1.2.3", p.currentVersion())
	require.NoError(t, p.stop(ctx))

	// Restart against a server that reports nothing new.
	second := versionedMock(id, "1.2.3", nil)
	second.upToDate = true
	host2 := &testHost{components: map[component.ID]component.Component{
		*storageID: host.GetExtensions()[*storageID],
		opampID:    second,
	}}

	restarted := newOpAMPProcessor(t, cfg, id)
	require.NoError(t, restarted.start(ctx, host2))
	defer func() { require.NoError(t, restarted.stop(ctx)) }()

	require.Equal(t, []string{"nginx"}, matcherNames(restarted))
	require.Equal(t, "1.2.3", restarted.currentVersion())

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		requests := second.sentRequests()
		if !assert.Len(c, requests, 1) {
			return
		}
		assert.Equal(c, "1.2.3", requests[0].Version, "the held version must be reported to the server")
	}, time.Second, 5*time.Millisecond)
}

func TestOpAMPVersionAcceptance(t *testing.T) {
	id := component.MustNewID("logtypedetection")
	matchers := []MatcherConfig{{Name: "from_server", Method: MatcherTypeStartsWith, Value: "GET "}}

	testCases := []struct {
		name        string
		held        string
		maxVersion  string
		offered     string
		wantApplied bool
		wantErr     string
	}{
		{name: "at max version", held: "1.2.3", maxVersion: "1.5.0", offered: "1.5.0", wantApplied: true},
		{name: "above max version refused", held: "1.2.3", maxVersion: "1.5.0", offered: "1.6.0", wantErr: "above matchers_version"},
		{name: "above max version refused on first run", maxVersion: "1.5.0", offered: "1.6.0", wantErr: "above matchers_version"},
		{name: "patch bump", held: "1.2.3", offered: "1.2.4", wantApplied: true},
		{name: "minor bump", held: "1.2.3", offered: "1.3.0", wantApplied: true},
		{name: "same version", held: "1.2.3", offered: "1.2.3"},
		{name: "older version", held: "1.2.3", offered: "1.2.2"},
		{name: "major bump refused", held: "1.2.3", offered: "2.0.0", wantErr: "breaking change"},
		{name: "major downgrade refused", held: "2.0.0", offered: "1.9.9", wantErr: "breaking change"},
		{name: "unparseable version", held: "1.2.3", offered: "latest", wantErr: `parse version "latest"`},
		{name: "first version", offered: "3.1.0", wantApplied: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := opampConfig(nil)
			cfg.OpAMP.MatchersVersion = tc.maxVersion
			p := newOpAMPProcessor(t, cfg, id)
			if tc.held != "" {
				applied, err := p.applyMatchers(tc.held, matchers)
				require.NoError(t, err)
				require.True(t, applied)
			}

			applied, err := p.applyMatchers(tc.offered, matchers)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.False(t, applied)
				require.Equal(t, tc.held, p.currentVersion(), "a refused version must not be recorded")
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantApplied, applied)
			if tc.wantApplied {
				require.Equal(t, tc.offered, p.currentVersion())
			} else {
				require.Equal(t, tc.held, p.currentVersion())
			}
		})
	}
}

// The configured ceiling is sent with the request and enforced on the reply.
func TestOpAMPMaxVersionSentWithRequest(t *testing.T) {
	ctx := context.Background()
	id := component.MustNewID("logtypedetection")

	mock := &mockOpAMPExtension{
		msgChan:      make(chan *protobufs.CustomMessage, 1),
		autoReply:    true,
		replyFor:     id,
		replyVersion: "1.6.0",
		reply:        []MatcherConfig{{Name: "too_new", Method: MatcherTypeStartsWith, Value: "GET "}},
	}
	host := &testHost{components: map[component.ID]component.Component{opampID: mock}}

	cfg := opampConfig(nil)
	cfg.OpAMP.MatchersVersion = "1.5.0"
	p := newOpAMPProcessor(t, cfg, id)
	require.NoError(t, p.start(ctx, host))
	defer func() { require.NoError(t, p.stop(ctx)) }()

	waitForMatchers(t, p)
	requests := mock.sentRequests()
	require.Len(t, requests, 1)
	require.Equal(t, "1.5.0", requests[0].MaxVersion)
	require.Empty(t, p.currentVersion(), "a reply above the ceiling is refused")
}
