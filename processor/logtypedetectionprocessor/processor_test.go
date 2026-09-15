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

package logtypedetectionprocessor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/fingerprint"
	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/metadatatest"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"
	"go.uber.org/zap"
)

func logsFromBodies(bodies ...string) plog.Logs {
	ld := plog.NewLogs()
	records := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
	for _, b := range bodies {
		records.AppendEmpty().Body().SetStr(b)
	}
	return ld
}

func TestProcessLogs(t *testing.T) {
	testCases := []struct {
		name         string
		bodies       []string
		expectedRuns int64
		fingerprints int
	}{
		{
			name:         "identical structure detects once",
			bodies:       []string{`{"a":1,"b":"x"}`, `{"a":2,"b":"y"}`},
			expectedRuns: 1,
			fingerprints: 2,
		},
		{
			name:         "distinct structures detect separately",
			bodies:       []string{`{"alpha":1}`, `{"gamma":1}`},
			expectedRuns: 2,
			fingerprints: 2,
		},
		{
			name:         "plain text detects via generic fingerprint",
			bodies:       []string{"plain text line", "other words entirely"},
			expectedRuns: 2,
			fingerprints: 2,
		},
		{
			name:         "unfingerprintable logs are skipped",
			bodies:       []string{"", "x", "nine char"},
			expectedRuns: 0,
			fingerprints: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tel := componenttest.NewTelemetry()
			t.Cleanup(func() { require.NoError(t, tel.Shutdown(context.Background())) })

			tb, err := metadata.NewTelemetryBuilder(metadatatest.NewSettings(tel).TelemetrySettings)
			require.NoError(t, err)

			p, err := newLogTypeDetectionProcessor(createDefaultConfig().(*Config), component.MustNewID("logtypedetection"), zap.NewNop(), tb)
			require.NoError(t, err)

			out, err := p.processLogs(context.Background(), logsFromBodies(tc.bodies...))
			require.NoError(t, err)

			records := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
			require.Equal(t, len(tc.bodies), records.Len())

			withFingerprint := 0
			for i := 0; i < records.Len(); i++ {
				if _, ok := records.At(i).Attributes().Get("fingerprint"); ok {
					withFingerprint++
				}
			}
			require.Equal(t, tc.fingerprints, withFingerprint)

			if tc.expectedRuns == 0 {
				_, err := tel.GetMetric("otelcol_processor_log_type_detection_attempts")
				require.Error(t, err)
				return
			}
			metadatatest.AssertEqualProcessorLogTypeDetectionAttempts(t, tel,
				[]metricdata.DataPoint[int64]{{Value: tc.expectedRuns}},
				metricdatatest.IgnoreTimestamp())
		})
	}
}

func TestProcessLogsCachesAcrossCalls(t *testing.T) {
	tel := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tel.Shutdown(context.Background())) })

	tb, err := metadata.NewTelemetryBuilder(metadatatest.NewSettings(tel).TelemetrySettings)
	require.NoError(t, err)

	p, err := newLogTypeDetectionProcessor(createDefaultConfig().(*Config), component.MustNewID("logtypedetection"), zap.NewNop(), tb)
	require.NoError(t, err)

	for range 3 {
		_, err := p.processLogs(context.Background(), logsFromBodies(`{"alpha":1}`))
		require.NoError(t, err)
	}

	metadatatest.AssertEqualProcessorLogTypeDetectionAttempts(t, tel,
		[]metricdata.DataPoint[int64]{{Value: 1}},
		metricdatatest.IgnoreTimestamp())
}

func TestFingerprintCacheEvictsOldest(t *testing.T) {
	tel := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tel.Shutdown(context.Background())) })

	tb, err := metadata.NewTelemetryBuilder(metadatatest.NewSettings(tel).TelemetrySettings)
	require.NoError(t, err)

	cfg := createDefaultConfig().(*Config)
	cfg.MaxSavedFingerprints = 2
	p, err := newLogTypeDetectionProcessor(cfg, component.MustNewID("logtypedetection"), zap.NewNop(), tb)
	require.NoError(t, err)

	_, err = p.processLogs(context.Background(), logsFromBodies(`{"alpha":1}`, `{"beta":1}`, `{"gamma":1}`))
	require.NoError(t, err)

	require.Equal(t, 2, p.logTypes.Len())
	require.False(t, p.logTypes.Contains(fingerprint.HashLog(`{"alpha":1}`)))
	require.True(t, p.logTypes.Contains(fingerprint.HashLog(`{"beta":1}`)))
	require.True(t, p.logTypes.Contains(fingerprint.HashLog(`{"gamma":1}`)))

	_, err = p.processLogs(context.Background(), logsFromBodies(`{"beta":1}`, `{"delta":1}`))
	require.NoError(t, err)

	require.True(t, p.logTypes.Contains(fingerprint.HashLog(`{"beta":1}`)))
	require.False(t, p.logTypes.Contains(fingerprint.HashLog(`{"gamma":1}`)))
}

func TestProcessLogsConcurrentSameStructure(t *testing.T) {
	tel := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tel.Shutdown(context.Background())) })

	tb, err := metadata.NewTelemetryBuilder(metadatatest.NewSettings(tel).TelemetrySettings)
	require.NoError(t, err)

	p, err := newLogTypeDetectionProcessor(createDefaultConfig().(*Config), component.MustNewID("logtypedetection"), zap.NewNop(), tb)
	require.NoError(t, err)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			_, err := p.processLogs(context.Background(), logsFromBodies(`{"a":1,"b":"x"}`))
			require.NoError(t, err)
		})
	}
	close(start)
	wg.Wait()

	got, err := tel.GetMetric("otelcol_processor_log_type_detection_attempts")
	require.NoError(t, err)
	runs := got.Data.(metricdata.Sum[int64]).DataPoints[0].Value
	require.Equal(t, int64(1), runs, "each fingerprint should only be detected once")
}

func TestPriorityOfMatchers(t *testing.T) {
	tel := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tel.Shutdown(context.Background())) })

	tb, err := metadata.NewTelemetryBuilder(metadatatest.NewSettings(tel).TelemetrySettings)
	require.NoError(t, err)

	config := createDefaultConfig().(*Config)
	config.Matchers = []MatcherConfig{
		{Name: "priority-unset", Value: `{"a"`, Method: MatcherTypeStartsWith},
		{Name: "priority-10", Priority: new(10), Value: `{"a"`, Method: MatcherTypeStartsWith},
		{Name: "priority-1", Priority: new(1), Value: `{"a"`, Method: MatcherTypeStartsWith},
		{Name: "priority-0", Priority: new(0), Value: `{"a"`, Method: MatcherTypeStartsWith},
		{Name: "priority-2", Priority: new(2), Value: `{"a"`, Method: MatcherTypeStartsWith},
	}
	p, err := newLogTypeDetectionProcessor(config, component.MustNewID("logtypedetection"), zap.NewNop(), tb)
	require.NoError(t, err)

	out, err := p.processLogs(context.Background(), logsFromBodies(`{"a":1,"b":"x"}`))
	require.NoError(t, err)

	records := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Len(t, config.Matchers, len(p.matchers))

	require.Equal(t, 1, records.Len())
	logType, ok := records.At(0).Attributes().Get(defaultLogTypeField)
	require.True(t, ok)
	require.Equal(t, "priority-0", logType.AsString())
}

func TestUnknownLogType(t *testing.T) {
	tel := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tel.Shutdown(context.Background())) })

	tb, err := metadata.NewTelemetryBuilder(metadatatest.NewSettings(tel).TelemetrySettings)
	require.NoError(t, err)

	cfg := createDefaultConfig().(*Config)
	cfg.Matchers = []MatcherConfig{{Name: "json_a", Method: MatcherTypeStartsWith, Value: `{"a"`}}
	p, err := newLogTypeDetectionProcessor(cfg, component.MustNewID("logtypedetection"), zap.NewNop(), tb)
	require.NoError(t, err)

	out, err := p.processLogs(context.Background(), logsFromBodies(`{"a":1,"b":2}`, `{"z":1,"y":2}`, "plain text line"))
	require.NoError(t, err)

	records := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	for i, want := range []string{"json_a", unknownLogType, unknownLogType} {
		got, ok := records.At(i).Attributes().Get(defaultLogTypeField)
		require.True(t, ok, "record %d has no log type", i)
		require.Equal(t, want, got.Str())
	}

	metadatatest.AssertEqualProcessorLogTypeDetectionLogsUnclassified(t, tel,
		[]metricdata.DataPoint[int64]{{Value: 2}},
		metricdatatest.IgnoreTimestamp())
	metadatatest.AssertEqualProcessorLogTypeDetectionLogsClassified(t, tel,
		[]metricdata.DataPoint[int64]{{
			Value:      1,
			Attributes: attribute.NewSet(attribute.String("log_type", "json_a")),
		}},
		metricdatatest.IgnoreTimestamp())
}

type testHost struct {
	component.Host
	components map[component.ID]component.Component
}

func (t *testHost) GetExtensions() map[component.ID]component.Component {
	return t.components
}

func TestFingerprintMapPersistence(t *testing.T) {
	ctx := context.Background()

	factory := filestorage.NewFactory()
	storageCfg := factory.CreateDefaultConfig().(*filestorage.Config)
	storageCfg.Directory = t.TempDir()

	fingerprintStorageID := component.NewIDWithName(component.MustNewType("file_storage"), "test")
	ext, err := factory.Create(ctx, extension.Settings{ID: fingerprintStorageID, TelemetrySettings: componenttest.NewNopTelemetrySettings()}, storageCfg)
	require.NoError(t, err)
	require.NoError(t, ext.Start(ctx, componenttest.NewNopHost()))
	defer func() { require.NoError(t, ext.Shutdown(ctx)) }()

	host := &testHost{components: map[component.ID]component.Component{fingerprintStorageID: ext}}

	cfg := createDefaultConfig().(*Config)
	cfg.FingerprintStorageID = &fingerprintStorageID
	cfg.Matchers = []MatcherConfig{{Name: "nginx", Method: MatcherTypeStartsWith, Value: "GET "}}

	newProcessor := func() *logTypeDetectionProcessor {
		tb, err := metadata.NewTelemetryBuilder(componenttest.NewNopTelemetrySettings())
		require.NoError(t, err)
		p, err := newLogTypeDetectionProcessor(cfg, component.MustNewID("logtypedetection"), zap.NewNop(), tb)
		require.NoError(t, err)
		return p
	}

	first := newProcessor()
	require.NoError(t, first.start(ctx, host))
	_, err = first.processLogs(ctx, logsFromBodies("GET /index.html 200", "something else entirely"))
	require.NoError(t, err)
	require.NoError(t, first.stop(ctx))

	second := newProcessor()
	require.NoError(t, second.start(ctx, host))
	defer func() { require.NoError(t, second.stop(ctx)) }()

	want := map[uint64]string{}
	for _, logFingerprint := range first.logTypes.Keys() {
		want[logFingerprint], _ = first.logTypes.Peek(logFingerprint)
	}
	require.Len(t, want, 2)
	require.Equal(t, 2, second.logTypes.Len())
	for logFingerprint, logType := range want {
		got, ok := second.logTypes.Peek(logFingerprint)
		require.True(t, ok)
		require.Equal(t, logType, got)
	}
}

func TestFingerprintMapPeriodicPersist(t *testing.T) {
	ctx := context.Background()

	factory := filestorage.NewFactory()
	storageCfg := factory.CreateDefaultConfig().(*filestorage.Config)
	storageCfg.Directory = t.TempDir()

	fingerprintStorageID := component.NewIDWithName(component.MustNewType("file_storage"), "test")
	ext, err := factory.Create(ctx, extension.Settings{ID: fingerprintStorageID, TelemetrySettings: componenttest.NewNopTelemetrySettings()}, storageCfg)
	require.NoError(t, err)
	require.NoError(t, ext.Start(ctx, componenttest.NewNopHost()))
	defer func() { require.NoError(t, ext.Shutdown(ctx)) }()

	host := &testHost{components: map[component.ID]component.Component{fingerprintStorageID: ext}}

	cfg := createDefaultConfig().(*Config)
	cfg.FingerprintStorageID = &fingerprintStorageID
	cfg.FingerprintPersistInterval = 10 * time.Millisecond
	cfg.Matchers = []MatcherConfig{{Name: "nginx", Method: MatcherTypeStartsWith, Value: "GET "}}

	tb, err := metadata.NewTelemetryBuilder(componenttest.NewNopTelemetrySettings())
	require.NoError(t, err)
	p, err := newLogTypeDetectionProcessor(cfg, component.MustNewID("logtypedetection"), zap.NewNop(), tb)
	require.NoError(t, err)
	require.NoError(t, p.start(ctx, host))

	_, err = p.processLogs(ctx, logsFromBodies("GET /index.html 200"))
	require.NoError(t, err)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		saved := persistedFingerprints{}
		assert.NoError(c, p.fingerprintStorageClient.LoadStorageData(ctx, fingerprintStorageKey, &saved))
		assert.Len(c, saved.LogTypes, 1)
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, p.stop(ctx))
	require.NoError(t, p.stop(ctx))
}

func TestStopAfterFailedStart(t *testing.T) {
	ctx := context.Background()

	missingStorageID := component.MustNewIDWithName("file_storage", "missing")
	cfg := createDefaultConfig().(*Config)
	cfg.FingerprintStorageID = &missingStorageID

	tb, err := metadata.NewTelemetryBuilder(componenttest.NewNopTelemetrySettings())
	require.NoError(t, err)
	p, err := newLogTypeDetectionProcessor(cfg, component.MustNewID("logtypedetection"), zap.NewNop(), tb)
	require.NoError(t, err)

	require.Error(t, p.start(ctx, &testHost{components: map[component.ID]component.Component{}}))
	require.NoError(t, p.stop(ctx))
}

func TestPersistedLogTypesDiscardedWhenMatchersChange(t *testing.T) {
	ctx := context.Background()

	factory := filestorage.NewFactory()
	storageCfg := factory.CreateDefaultConfig().(*filestorage.Config)
	storageCfg.Directory = t.TempDir()

	fingerprintStorageID := component.NewIDWithName(component.MustNewType("file_storage"), "test")
	ext, err := factory.Create(ctx, extension.Settings{ID: fingerprintStorageID, TelemetrySettings: componenttest.NewNopTelemetrySettings()}, storageCfg)
	require.NoError(t, err)
	require.NoError(t, ext.Start(ctx, componenttest.NewNopHost()))
	defer func() { require.NoError(t, ext.Shutdown(ctx)) }()

	host := &testHost{components: map[component.ID]component.Component{fingerprintStorageID: ext}}

	newProcessor := func(matchers []MatcherConfig) *logTypeDetectionProcessor {
		cfg := createDefaultConfig().(*Config)
		cfg.FingerprintStorageID = &fingerprintStorageID
		cfg.Matchers = matchers
		tb, err := metadata.NewTelemetryBuilder(componenttest.NewNopTelemetrySettings())
		require.NoError(t, err)
		p, err := newLogTypeDetectionProcessor(cfg, component.MustNewID("logtypedetection"), zap.NewNop(), tb)
		require.NoError(t, err)
		return p
	}

	original := newProcessor([]MatcherConfig{{Name: "nginx", Method: MatcherTypeStartsWith, Value: "GET "}})
	require.NoError(t, original.start(ctx, host))
	_, err = original.processLogs(ctx, logsFromBodies("GET /index.html 200"))
	require.NoError(t, err)
	require.NoError(t, original.stop(ctx))

	unchanged := newProcessor([]MatcherConfig{{Name: "nginx", Method: MatcherTypeStartsWith, Value: "GET "}})
	require.NoError(t, unchanged.start(ctx, host))
	require.Equal(t, 1, unchanged.logTypes.Len())
	require.NoError(t, unchanged.stop(ctx))

	renamed := newProcessor([]MatcherConfig{{Name: "nginx_access", Method: MatcherTypeStartsWith, Value: "GET "}})
	require.NoError(t, renamed.start(ctx, host))
	defer func() { require.NoError(t, renamed.stop(ctx)) }()
	require.Equal(t, 0, renamed.logTypes.Len())

	out, err := renamed.processLogs(ctx, logsFromBodies("GET /index.html 200"))
	require.NoError(t, err)
	logType, ok := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().Get("log_type")
	require.True(t, ok)
	require.Equal(t, "nginx_access", logType.Str())
}
