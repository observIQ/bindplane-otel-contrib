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

package measurements

import (
	"path/filepath"
	"testing"

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"github.com/stretchr/testify/require"
)

func TestMeasureLogs(t *testing.T) {
	logs, err := golden.ReadLogs(filepath.Join("testdata", "logs", "w3c-logs.yaml"))
	require.NoError(t, err)

	t.Run("without raw bytes", func(t *testing.T) {
		m := MeasureLogs(logs, false)
		require.Equal(t, Measurement{Size: 3974, Count: 16}, m)
	})

	t.Run("with raw bytes", func(t *testing.T) {
		m := MeasureLogs(logs, true)
		require.Equal(t, Measurement{Size: 3974, Count: 16, RawBytes: 2373, HasRawBytes: true}, m)
	})

	// This subtest mutates logs, so it must run last.
	t.Run("raw bytes prefer log.record.original", func(t *testing.T) {
		rls := logs.ResourceLogs()
		for i := 0; i < rls.Len(); i++ {
			sls := rls.At(i).ScopeLogs()
			for j := 0; j < sls.Len(); j++ {
				lrs := sls.At(j).LogRecords()
				for k := 0; k < lrs.Len(); k++ {
					lrs.At(k).Attributes().PutStr("log.record.original", "12345678901234567890")
				}
			}
		}
		m := MeasureLogs(logs, true)
		require.Equal(t, int64(16*20), m.RawBytes)
		require.True(t, m.HasRawBytes)
	})
}

func TestMeasureMetrics(t *testing.T) {
	metrics, err := golden.ReadMetrics(filepath.Join("testdata", "metrics", "host-metrics.yaml"))
	require.NoError(t, err)

	require.Equal(t, Measurement{Size: 5675, Count: 37}, MeasureMetrics(metrics))
}

func TestMeasureTraces(t *testing.T) {
	traces, err := golden.ReadTraces(filepath.Join("testdata", "traces", "bindplane-traces.yaml"))
	require.NoError(t, err)

	require.Equal(t, Measurement{Size: 16767, Count: 178}, MeasureTraces(traces))
}
