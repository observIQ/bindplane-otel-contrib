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

//go:build linux

package awsneuronreceiver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/observiq/bindplane-otel-contrib/receiver/awsneuronreceiver/internal/metadata"
)

func loadFixture(t *testing.T) *nmReport {
	t.Helper()
	b, err := os.ReadFile("testdata/under_load.json")
	require.NoError(t, err)
	var rep nmReport
	require.NoError(t, json.Unmarshal(b, &rep))
	return &rep
}

type numberPoint struct {
	dbl    float64
	intVal int64
	attrs  map[string]string
}

func collect(t *testing.T, md pmetric.Metrics) (map[string][]numberPoint, pcommon.Map) {
	t.Helper()
	require.Equal(t, 1, md.ResourceMetrics().Len())
	rm := md.ResourceMetrics().At(0)
	out := map[string][]numberPoint{}
	sms := rm.ScopeMetrics()
	for i := 0; i < sms.Len(); i++ {
		ms := sms.At(i).Metrics()
		for j := 0; j < ms.Len(); j++ {
			m := ms.At(j)
			var dps pmetric.NumberDataPointSlice
			switch m.Type() {
			case pmetric.MetricTypeGauge:
				dps = m.Gauge().DataPoints()
			case pmetric.MetricTypeSum:
				dps = m.Sum().DataPoints()
			default:
				continue
			}
			for k := 0; k < dps.Len(); k++ {
				dp := dps.At(k)
				np := numberPoint{attrs: map[string]string{}}
				if dp.ValueType() == pmetric.NumberDataPointValueTypeDouble {
					np.dbl = dp.DoubleValue()
				} else {
					np.intVal = dp.IntValue()
				}
				dp.Attributes().Range(func(key string, v pcommon.Value) bool {
					np.attrs[key] = v.AsString()
					return true
				})
				out[m.Name()] = append(out[m.Name()], np)
			}
		}
	}
	return out, rm.Resource().Attributes()
}

func findByAttrs(pts []numberPoint, want map[string]string) (numberPoint, bool) {
	for _, p := range pts {
		match := true
		for k, v := range want {
			if p.attrs[k] != v {
				match = false
				break
			}
		}
		if match {
			return p, true
		}
	}
	return numberPoint{}, false
}

func TestRecordMonitorFromFixture(t *testing.T) {
	rep := loadFixture(t)
	s := &neuronScraper{
		settings: receivertest.NewNopSettings(metadata.Type),
		mb:       metadata.NewMetricsBuilder(metadata.DefaultMetricsBuilderConfig(), receivertest.NewNopSettings(metadata.Type)),
	}
	now := pcommon.NewTimestampFromTime(time.Now())
	rb := s.mb.NewResourceBuilder()
	s.recordMonitor(now, rep)
	setResourceFromReport(rb, rep)
	md := s.mb.Emit(metadata.WithResource(rb.Emit()))

	metrics, res := collect(t, md)

	v, ok := res.Get("host.id")
	require.True(t, ok)
	assert.Equal(t, "i-0049639f2f6d4f7b5", v.AsString())

	util, ok := findByAttrs(metrics["aws.neuron.neuroncore.utilization"], map[string]string{"aws.neuron.neuroncore.id": "0"})
	require.True(t, ok)
	assert.InDelta(t, 0.2504577, util.dbl, 1e-6) // percent -> fraction

	flops, ok := findByAttrs(metrics["aws.neuron.neuroncore.flops"], map[string]string{"aws.neuron.neuroncore.id": "0"})
	require.True(t, ok)
	assert.InDelta(t, 346336403642, flops.dbl, 1)

	lat, ok := findByAttrs(metrics["aws.neuron.execution.latency"], map[string]string{"aws.neuron.latency.type": "total", "aws.neuron.latency.quantile": "p50"})
	require.True(t, ok)
	assert.InDelta(t, 0.0000629425, lat.dbl, 1e-9)

	comp, ok := findByAttrs(metrics["aws.neuron.execution.count"], map[string]string{"aws.neuron.execution.status": "completed"})
	require.True(t, ok)
	assert.Equal(t, int64(34408), comp.intVal)

	mem, ok := findByAttrs(metrics["aws.neuron.neuroncore.memory.usage"], map[string]string{"aws.neuron.neuroncore.id": "0", "aws.neuron.memory.category": "model_code"})
	require.True(t, ok)
	assert.Equal(t, int64(102616000), mem.intVal)

	// default-off metric must NOT appear with default config.
	assert.Empty(t, metrics["aws.neuron.system.memory.usage"])
}

func TestTwoLayerConfigResolution(t *testing.T) {
	off := false
	conf := confmap.NewFromStringMap(map[string]any{
		"metric_groups": map[string]any{"neuroncore": off, "system": true},
		"metrics": map[string]any{
			// explicit per-metric override beats the (false) neuroncore group toggle.
			"aws.neuron.neuroncore.utilization": map[string]any{"enabled": true},
		},
	})
	cfg := createDefaultConfig().(*Config)
	require.NoError(t, conf.Unmarshal(cfg))

	m := cfg.Metrics
	// neuroncore group off -> flops disabled (was on by default)
	assert.False(t, m.AwsNeuronNeuroncoreFlops.Enabled)
	// explicit per-metric override keeps utilization on
	assert.True(t, m.AwsNeuronNeuroncoreUtilization.Enabled)
	// system group on -> a normally-off system metric becomes enabled
	assert.True(t, m.AwsNeuronSystemMemoryUsage.Enabled)
	// untouched group keeps metadata default (execution.latency default-on)
	assert.True(t, m.AwsNeuronExecutionLatency.Enabled)
}

func TestSysfsDegradesWhenRootMissing(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	r := newSysfsReader("/nonexistent-neuron-sysfs-root", zap.New(core))
	mb := metadata.NewMetricsBuilder(metadata.DefaultMetricsBuilderConfig(), receivertest.NewNopSettings(metadata.Type))
	now := pcommon.NewTimestampFromTime(time.Now())

	// Multiple scrapes against an unreadable sysfs root must not crash and must
	// log exactly once (single error, no per-scrape spam).
	for i := 0; i < 3; i++ {
		r.record(mb, now, mb.NewResourceBuilder(), true)
	}
	assert.Equal(t, 1, logs.FilterLevelExact(zapcore.ErrorLevel).Len())
}

func TestSysfsECCEmission(t *testing.T) {
	dir := t.TempDir()
	for name, val := range map[string]string{
		"mem_ecc_repairable_uncorrected": "3",
		"mem_ecc_uncorrected":            "1",
		"sram_ecc_uncorrected":           "2",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(val), 0o600))
	}
	r := newSysfsReader("/unused", zap.NewNop())
	now := pcommon.NewTimestampFromTime(time.Now())

	// Monitor active: only the sysfs-unique repairable series is emitted.
	mb := metadata.NewMetricsBuilder(metadata.DefaultMetricsBuilderConfig(), receivertest.NewNopSettings(metadata.Type))
	r.recordSysfsECC(mb, now, "0", dir, true)
	active, _ := collect(t, mb.Emit())
	require.Len(t, active["aws.neuron.errors"], 1)
	_, ok := findByAttrs(active["aws.neuron.errors"], map[string]string{"error.type": "repairable", "aws.neuron.memory.type": "dram", "hw.id": "0"})
	assert.True(t, ok, "repairable must be emitted while monitor is active")

	// Monitor inactive: repairable + the two uncorrected fallback series.
	mb2 := metadata.NewMetricsBuilder(metadata.DefaultMetricsBuilderConfig(), receivertest.NewNopSettings(metadata.Type))
	r.recordSysfsECC(mb2, now, "0", dir, false)
	inactive, _ := collect(t, mb2.Emit())
	assert.Len(t, inactive["aws.neuron.errors"], 3, "repairable + dram/sram uncorrected when monitor absent")
}

// The sysfs power node reports "<status>,<epoch>,<min>,<max>,<avg>" as percentages.
// The receiver must emit all three statistics as separate points under one metric
// name, distinguished by the statistic attribute (hostmetrics state-attribute
// pattern), each as a fraction of max power (percent/100, to match unit "1").
func TestSysfsPowerUtilizationStatistics(t *testing.T) {
	dir := t.TempDir()
	powerDir := filepath.Join(dir, "neuron0", "stats", "power")
	require.NoError(t, os.MkdirAll(powerDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(powerDir, "utilization"),
		[]byte("POWER_STATUS_VALID,1781370720,1.50,3.50,2.00"), 0o600))

	cfg := metadata.DefaultMetricsBuilderConfig()
	cfg.Metrics.AwsNeuronDevicePowerUtilization.Enabled = true // default-off; opt in for the test
	mb := metadata.NewMetricsBuilder(cfg, receivertest.NewNopSettings(metadata.Type))
	rb := mb.NewResourceBuilder()
	now := pcommon.NewTimestampFromTime(time.Now())

	newSysfsReader(dir, zap.NewNop()).record(mb, now, rb, true)

	pts, _ := collect(t, mb.Emit())
	power := pts["aws.neuron.device.power.utilization"]
	require.Len(t, power, 3, "min, max, and avg must each be a separate data point")
	mn, ok := findByAttrs(power, map[string]string{"aws.neuron.power.statistic": "min"})
	require.True(t, ok, "min point present")
	assert.InDelta(t, 0.015, mn.dbl, 1e-9)
	mx, ok := findByAttrs(power, map[string]string{"aws.neuron.power.statistic": "max"})
	require.True(t, ok, "max point present")
	assert.InDelta(t, 0.035, mx.dbl, 1e-9)
	av, ok := findByAttrs(power, map[string]string{"aws.neuron.power.statistic": "avg"})
	require.True(t, ok, "avg point present")
	assert.InDelta(t, 0.020, av.dbl, 1e-9)
}

// The receiver must not override scraperhelper's default collection interval; the
// default should be the upstream 60s, matching Bindplane's source default.
func TestDefaultCollectionIntervalInheritsScraperhelper(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	assert.Equal(t, time.Minute, cfg.CollectionInterval,
		"default collection_interval must inherit scraperhelper's 60s, not a local override")
}

// neuron-monitor's default output is single-line JSON, but a pretty-printed
// config produces multi-line reports. The streaming decoder must parse those
// (the old line scanner would split on internal newlines and lose every report).
func TestConsumeParsesPrettyPrintedReports(t *testing.T) {
	stream := `{
  "instance_info": { "instance_id": "i-first" }
}
{
  "instance_info": { "instance_id": "i-second" }
}
`
	r := newRunner("neuron-monitor", 10*time.Second, zap.NewNop())
	r.consume(context.Background(), strings.NewReader(stream))

	rep := r.latestReport()
	require.NotNil(t, rep, "multi-line reports must parse")
	assert.Equal(t, "i-second", rep.InstanceInfo.InstanceID, "last report wins")
}

// A non-numeric NeuronCore key must be skipped, not collapsed onto core 0 where
// it would mask the real core-0 data.
func TestRecordMonitorSkipsNonNumericCoreKey(t *testing.T) {
	rep := &nmReport{NeuronRuntimeData: make([]nmRuntimeData, 1)}
	cc := &rep.NeuronRuntimeData[0].Report.NeuroncoreCounters
	cc.NeuroncoresInUse = map[string]struct {
		NeuroncoreUtilization float64 `json:"neuroncore_utilization"`
		EffectiveFlops        float64 `json:"effective_flops"`
	}{
		"0":     {NeuroncoreUtilization: 50, EffectiveFlops: 100},
		"bogus": {NeuroncoreUtilization: 99, EffectiveFlops: 999},
	}
	s := &neuronScraper{
		settings: receivertest.NewNopSettings(metadata.Type),
		mb:       metadata.NewMetricsBuilder(metadata.DefaultMetricsBuilderConfig(), receivertest.NewNopSettings(metadata.Type)),
	}
	now := pcommon.NewTimestampFromTime(time.Now())
	s.recordMonitor(now, rep)
	metrics, _ := collect(t, s.mb.Emit())

	util := metrics["aws.neuron.neuroncore.utilization"]
	require.Len(t, util, 1, "only the numeric core key is recorded")
	_, ok := findByAttrs(util, map[string]string{"aws.neuron.neuroncore.id": "0"})
	assert.True(t, ok, "core 0 must be the surviving entry, not the bogus key")
}

// The receiver is the sole source of truth for the neuron-monitor config: it
// generates the config itself (no external input) and sets the period from
// collection_interval, requesting the metrics it maps (runtime + system, incl. ECC).
func TestRunnerGeneratesOwnConfig(t *testing.T) {
	r := newRunner("neuron-monitor", 30*time.Second, zap.NewNop())
	path, cleanup, err := r.writeEffectiveConfig()
	require.NoError(t, err)
	defer cleanup()

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "30s", got["period"])
	assert.NotNil(t, got["neuron_runtimes"], "default config must request runtime metrics")
	assert.NotNil(t, got["system_metrics"], "default config must request system/ECC metrics")
}

// A non-zero exit from neuron-monitor must be logged once at Error with a tail
// of stderr, so a permissions failure or refusing-to-run binary is debuggable.
func TestRunnerLogsStderrOnNonZeroExit(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	r := newRunner("/bin/sh", 10*time.Second, zap.New(core))
	r.commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "echo neuron-monitor-boom >&2; exit 3")
	}
	r.start(context.Background())
	defer r.stop()

	assert.Eventually(t, func() bool {
		return logs.FilterLevelExact(zapcore.ErrorLevel).Len() == 1
	}, 2*time.Second, 10*time.Millisecond)
	entry := logs.FilterLevelExact(zapcore.ErrorLevel).All()[0]
	assert.Contains(t, entry.ContextMap()["stderr"], "neuron-monitor-boom")
	assert.Nil(t, r.latestReport())
}

func TestRunnerDegradesWhenBinaryMissing(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	r := newRunner("definitely-not-a-real-neuron-monitor-xyz", 10*time.Second, zap.New(core))
	r.start(context.Background())
	defer r.stop()

	// neuron-monitor is the primary path, so an absent binary is an error, logged once.
	assert.Eventually(t, func() bool {
		return logs.FilterLevelExact(zapcore.ErrorLevel).Len() == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Nil(t, r.latestReport())
}
