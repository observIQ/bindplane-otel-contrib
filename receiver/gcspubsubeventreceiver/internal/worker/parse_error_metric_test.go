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

package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"

	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/metadatatest"
)

// TestRecordParseError_IncrementsTheMetric asserts the callback blobstream holds
// reports into this receiver's own counter.
func TestRecordParseError_IncrementsTheMetric(t *testing.T) {
	tt := componenttest.NewTelemetry()
	defer func() { require.NoError(t, tt.Shutdown(context.Background())) }()

	builder, err := metadata.NewTelemetryBuilder(metadatatest.NewSettings(tt).TelemetrySettings)
	require.NoError(t, err)

	w := &Worker{metrics: builder}
	w.recordParseError(context.Background())
	w.recordParseError(context.Background())

	metadatatest.AssertEqualGcseventParseErrors(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 2}},
		metricdatatest.IgnoreTimestamp())
}

// TestRecordParseError_ToleratesMissingMetrics asserts a worker built without a
// telemetry builder does not panic.
func TestRecordParseError_ToleratesMissingMetrics(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		(&Worker{}).recordParseError(context.Background())
	})
}
