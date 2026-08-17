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

	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/metadatatest"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"
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

			p, err := newLogTypeDetectionProcessor(createDefaultConfig().(*Config), tb)
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
				_, err := tel.GetMetric("otelcol_log_type_detection_runs")
				require.Error(t, err)
				return
			}
			metadatatest.AssertEqualLogTypeDetectionRuns(t, tel,
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

	p, err := newLogTypeDetectionProcessor(createDefaultConfig().(*Config), tb)
	require.NoError(t, err)

	for range 3 {
		_, err := p.processLogs(context.Background(), logsFromBodies(`{"alpha":1}`))
		require.NoError(t, err)
	}

	metadatatest.AssertEqualLogTypeDetectionRuns(t, tel,
		[]metricdata.DataPoint[int64]{{Value: 1}},
		metricdatatest.IgnoreTimestamp())
}

func TestProcessLogsConcurrentSameStructure(t *testing.T) {
	tel := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tel.Shutdown(context.Background())) })

	tb, err := metadata.NewTelemetryBuilder(metadatatest.NewSettings(tel).TelemetrySettings)
	require.NoError(t, err)

	p, err := newLogTypeDetectionProcessor(createDefaultConfig().(*Config), tb)
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

	got, err := tel.GetMetric("otelcol_log_type_detection_runs")
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
		{Name: "priority-10", Priority: 10, Value: `test`, Method: MatcherTypeStartsWith},
		{Name: "priority-1", Priority: 1, Value: `test`, Method: MatcherTypeStartsWith},
		{Name: "priority-2", Priority: 2, Value: `test`, Method: MatcherTypeStartsWith},
	}
	p, err := newLogTypeDetectionProcessor(config, tb)
	require.NoError(t, err)

	out, err := p.processLogs(context.Background(), logsFromBodies(`{"a":1,"b":"x"}`))
	require.NoError(t, err)

	records := out.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	require.Len(t, config.Matchers, len(p.matchers))

	for i := 0; i < records.Len(); i++ {
		if record, ok := records.At(i).Attributes().Get(defaultLogTypeField); ok {
			require.Equal(t, "priority-1", record.AsString())
		}
	}
}
