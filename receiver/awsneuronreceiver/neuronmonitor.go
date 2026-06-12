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

package awsneuronreceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/awsneuronreceiver"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// stderrTailBytes bounds how much of neuron-monitor's stderr we retain so a
// non-zero exit can be reported with a diagnostic tail without growing without
// bound for a long-lived process that chatters on stderr.
const stderrTailBytes = 4 * 1024

// tailBuffer is an io.Writer that retains only the last stderrTailBytes written,
// so cmd.Stderr can capture a bounded tail of neuron-monitor's stderr.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrTailBytes {
		t.buf = t.buf[len(t.buf)-stderrTailBytes:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// nmReport is one neuron-monitor JSON report (emitted once per period to stdout).
type nmReport struct {
	NeuronRuntimeData  []nmRuntimeData `json:"neuron_runtime_data"`
	SystemData         nmSystemData    `json:"system_data"`
	InstanceInfo       nmInstanceInfo  `json:"instance_info"`
	NeuronHardwareInfo nmHardwareInfo  `json:"neuron_hardware_info"`
}

type nmRuntimeData struct {
	PID    int    `json:"pid"`
	Error  string `json:"error"`
	Report struct {
		ExecutionStats     nmExecutionStats     `json:"execution_stats"`
		MemoryUsed         nmMemoryUsed         `json:"memory_used"`
		NeuroncoreCounters nmNeuroncoreCounters `json:"neuroncore_counters"`
		RuntimeVcpuUsage   struct {
			VcpuUsage map[string]float64 `json:"vcpu_usage"`
		} `json:"neuron_runtime_vcpu_usage"`
	} `json:"report"`
}

type nmExecutionStats struct {
	ErrorSummary     map[string]int64 `json:"error_summary"`
	ExecutionSummary map[string]int64 `json:"execution_summary"`
	LatencyStats     struct {
		TotalLatency  map[string]float64 `json:"total_latency"`
		DeviceLatency map[string]float64 `json:"device_latency"`
	} `json:"latency_stats"`
}

type nmMemoryUsed struct {
	NeuronRuntimeUsedBytes struct {
		Host           int64 `json:"host"`
		NeuronDevice   int64 `json:"neuron_device"`
		UsageBreakdown struct {
			// per-NeuronCore index -> category -> bytes
			NeuroncoreMemoryUsage map[string]map[string]int64 `json:"neuroncore_memory_usage"`
		} `json:"usage_breakdown"`
	} `json:"neuron_runtime_used_bytes"`
}

type nmNeuroncoreCounters struct {
	// per-NeuronCore index -> counters
	NeuroncoresInUse map[string]struct {
		NeuroncoreUtilization float64 `json:"neuroncore_utilization"`
		EffectiveFlops        float64 `json:"effective_flops"`
	} `json:"neuroncores_in_use"`
}

type nmSystemData struct {
	NeuronHwCounters nmHwCounters `json:"neuron_hw_counters"`
	MemoryInfo       struct {
		MemoryTotalBytes int64 `json:"memory_total_bytes"`
		MemoryUsedBytes  int64 `json:"memory_used_bytes"`
		SwapTotalBytes   int64 `json:"swap_total_bytes"`
		SwapUsedBytes    int64 `json:"swap_used_bytes"`
	} `json:"memory_info"`
	VcpuUsage struct {
		AverageUsage map[string]float64 `json:"average_usage"`
	} `json:"vcpu_usage"`
}

type nmHwCounters struct {
	NeuronDevices []struct {
		NeuronDeviceIndex  int   `json:"neuron_device_index"`
		MemEccCorrected    int64 `json:"mem_ecc_corrected"`
		MemEccUncorrected  int64 `json:"mem_ecc_uncorrected"`
		SramEccCorrected   int64 `json:"sram_ecc_corrected"`
		SramEccUncorrected int64 `json:"sram_ecc_uncorrected"`
	} `json:"neuron_devices"`
}

type nmInstanceInfo struct {
	InstanceID               string `json:"instance_id"`
	InstanceType             string `json:"instance_type"`
	InstanceRegion           string `json:"instance_region"`
	InstanceAvailabilityZone string `json:"instance_availability_zone"`
	AmiID                    string `json:"ami_id"`
}

type nmHardwareInfo struct {
	NeuronDeviceType         string `json:"neuron_device_type"`
	NeuroncoreVersion        string `json:"neuroncore_version"`
	NeuronDeviceCount        int    `json:"neuron_device_count"`
	NeuronDeviceMemorySize   int64  `json:"neuron_device_memory_size"`
	NeuroncorePerDeviceCount int    `json:"neuroncore_per_device_count"`
}

// runner manages the neuron-monitor subprocess and exposes the latest report.
type runner struct {
	command    string
	configFile string
	period     time.Duration
	logger     *zap.Logger

	latest        atomic.Pointer[nmReport]
	degraded      sync.Once
	wg            sync.WaitGroup
	cancel        context.CancelFunc
	stderr        *tailBuffer
	cleanupConfig func()

	// commandContext is swappable in tests to avoid spawning a real process.
	commandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func newRunner(command, configFile string, period time.Duration, logger *zap.Logger) *runner {
	return &runner{
		command:        command,
		configFile:     configFile,
		period:         period,
		logger:         logger,
		commandContext: exec.CommandContext,
	}
}

// defaultMonitorConfig is the neuron-monitor configuration the receiver uses when
// the operator supplies no config_file. It requests exactly the metric groups the
// receiver maps (runtime counters/stats/memory plus system hardware counters,
// vCPU, and memory), so ECC and the rest are collected out of the box.
func defaultMonitorConfig() map[string]any {
	return map[string]any{
		"neuron_runtimes": []any{map[string]any{
			"tag_filter": ".*",
			"metrics": []any{
				map[string]any{"type": "neuroncore_counters"},
				map[string]any{"type": "execution_stats"},
				map[string]any{"type": "memory_used"},
				map[string]any{"type": "neuron_runtime_vcpu_usage"},
			},
		}},
		"system_metrics": []any{
			map[string]any{"type": "neuron_hw_counters"},
			map[string]any{"type": "vcpu_usage"},
			map[string]any{"type": "memory_info"},
		},
	}
}

// monitorPeriod formats a duration the way neuron-monitor's "period" field expects
// (e.g. "10s"). Whole seconds render as "<n>s" to avoid Go's "1m0s" style.
func monitorPeriod(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int64(d/time.Second))
	}
	return d.String()
}

// writeEffectiveConfig builds the neuron-monitor config the receiver launches with
// and writes it to a temp file. The receiver owns the cadence: it always sets
// "period" to collection_interval, overriding any period an operator put in their
// config_file, so both collection halves (subprocess + sysfs) obey one interval.
// A user-supplied config_file's metric selections are preserved; only the period
// is forced. Returns the path and a cleanup func to remove the temp file.
func (r *runner) writeEffectiveConfig() (string, func(), error) {
	cfg := defaultMonitorConfig()
	if r.configFile != "" {
		b, err := os.ReadFile(r.configFile) // #nosec G304 -- operator-provided config path
		if err != nil {
			return "", nil, err
		}
		cfg = map[string]any{}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return "", nil, fmt.Errorf("parse config_file %q: %w", r.configFile, err)
		}
	}
	cfg["period"] = monitorPeriod(r.period)

	b, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "neuron-monitor-config-*.json")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

// start launches neuron-monitor and begins consuming its stdout in a goroutine.
func (r *runner) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel

	// The receiver owns the cadence: derive neuron-monitor's period from
	// collection_interval so the subprocess and sysfs halves stay in lockstep.
	cfgPath, cleanup, err := r.writeEffectiveConfig()
	if err != nil {
		r.markDegraded("failed to prepare neuron-monitor config; continuing without its metrics", err)
		cancel()
		return
	}
	cmd := r.commandContext(ctx, r.command, "-c", cfgPath)
	r.stderr = &tailBuffer{}
	cmd.Stderr = r.stderr // bounded tail, used to diagnose a non-zero exit
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.markDegraded("failed to open neuron-monitor stdout pipe", err)
		cleanup()
		cancel()
		return
	}
	if err := cmd.Start(); err != nil {
		r.markDegraded("neuron-monitor could not be started; continuing without Neuron metrics", err)
		cleanup()
		cancel()
		return
	}
	// Process is running; stop() now owns removing the config temp file.
	r.cleanupConfig = cleanup

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.consume(ctx, stdout)
		// Reap the process and surface its exit status. A non-zero exit (bad
		// config_file, missing permissions, the binary refusing to run) is the
		// typical "won't start properly" case; the exit error plus a tail of
		// stderr is what makes that debuggable in the field.
		err := cmd.Wait()
		if errors.Is(ctx.Err(), context.Canceled) {
			return // our own shutdown killed it; expected, not a degradation
		}
		if err != nil {
			r.markDegraded("neuron-monitor exited unexpectedly; continuing without its metrics", err,
				zap.String("stderr", r.stderr.String()))
			return
		}
		r.markDegraded("neuron-monitor stream ended unexpectedly; continuing without its metrics", nil)
	}()
}

// consume reads JSON reports from neuron-monitor's stdout with a streaming
// decoder. Using json.Decoder (rather than a line scanner) makes parsing robust
// to pretty-printed output and removes any per-report size cap, so multi-line or
// large multi-model reports are handled correctly. It returns when the stream
// ends or our own shutdown cancels the subprocess; the goroutine in start()
// reports any abnormal process exit.
func (r *runner) consume(ctx context.Context, stdout io.Reader) {
	dec := json.NewDecoder(stdout)
	for {
		var rep nmReport
		if err := dec.Decode(&rep); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(ctx.Err(), context.Canceled) {
				return // stream closed or shutdown; exit status handled by start()
			}
			// A malformed token desyncs the stream and we can't reliably resync.
			r.markDegraded("neuron-monitor produced unparseable output; stopping its stream", err)
			return
		}
		r.latest.Store(&rep)
	}
}

// markDegraded logs the degradation exactly once. neuron-monitor is the primary
// collection path, so its failure is logged at error severity.
func (r *runner) markDegraded(msg string, err error, fields ...zap.Field) {
	r.degraded.Do(func() {
		r.logger.Error(msg, append([]zap.Field{zap.String("command", r.command), zap.Error(err)}, fields...)...)
	})
}

// latestReport returns the most recent parsed report, or nil if none yet.
func (r *runner) latestReport() *nmReport {
	return r.latest.Load()
}

// stop cancels the subprocess, waits for the reader goroutine to finish, and
// removes the generated neuron-monitor config temp file.
func (r *runner) stop() {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	if r.cleanupConfig != nil {
		r.cleanupConfig()
	}
}
