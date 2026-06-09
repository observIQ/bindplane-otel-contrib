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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
)

// maxLineBytes bounds a single neuron-monitor JSON report line. The default
// bufio.Scanner token cap (64 KiB) can be exceeded when many models are loaded.
const maxLineBytes = 4 * 1024 * 1024

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
	logger     *zap.Logger

	latest   atomic.Pointer[nmReport]
	degraded sync.Once
	wg       sync.WaitGroup
	cancel   context.CancelFunc

	// commandContext is swappable in tests to avoid spawning a real process.
	commandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func newRunner(command, configFile string, logger *zap.Logger) *runner {
	return &runner{
		command:        command,
		configFile:     configFile,
		logger:         logger,
		commandContext: exec.CommandContext,
	}
}

// start launches neuron-monitor and begins consuming its stdout in a goroutine.
func (r *runner) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel

	args := []string{}
	if r.configFile != "" {
		args = append(args, "-c", r.configFile)
	}
	cmd := r.commandContext(ctx, r.command, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.markDegraded("failed to open neuron-monitor stdout pipe", err)
		cancel()
		return
	}
	if err := cmd.Start(); err != nil {
		r.markDegraded("neuron-monitor could not be started; continuing without Neuron metrics", err)
		cancel()
		return
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.consume(ctx, stdout)
		// Reap the process so we never leave a zombie.
		_ = cmd.Wait()
	}()
}

// consume scans newline-delimited JSON reports from neuron-monitor's stdout.
// When the stream ends for any reason other than our own shutdown, it logs a
// single warning (via the shared latch) and returns; the receiver keeps serving
// the sysfs stream.
func (r *runner) consume(ctx context.Context, stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rep nmReport
		if err := json.Unmarshal(line, &rep); err != nil {
			r.logger.Debug("failed to parse neuron-monitor report", zap.Error(err))
			continue
		}
		r.latest.Store(&rep)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return // expected: shutdown cancelled the subprocess
	}
	r.markDegraded("neuron-monitor stream ended; continuing without its metrics", scanner.Err())
}

// markDegraded logs the degradation exactly once. neuron-monitor is the primary
// collection path, so its failure is logged at error severity.
func (r *runner) markDegraded(msg string, err error) {
	r.degraded.Do(func() {
		r.logger.Error(msg, zap.String("command", r.command), zap.Error(err))
	})
}

// latestReport returns the most recent parsed report, or nil if none yet.
func (r *runner) latestReport() *nmReport {
	return r.latest.Load()
}

// stop cancels the subprocess and waits for the reader goroutine to finish.
func (r *runner) stop() {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
}
