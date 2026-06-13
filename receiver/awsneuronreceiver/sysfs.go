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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/receiver/awsneuronreceiver/internal/metadata"
)

// sysfsReader reads the Neuron kernel-driver sysfs tree. All reads are strictly
// read-only (os.ReadFile opens O_RDONLY) so write-to-clear counters are never
// mutated. A wholly-unreadable sysfs root logs a single error (see markDegraded)
// and the receiver continues; individual missing or unreadable files are logged
// at Debug and skipped, so partial trees are tolerated but still diagnosable.
type sysfsReader struct {
	root     string
	logger   *zap.Logger
	degraded sync.Once
}

// markDegraded logs a single error when the sysfs stream is unreadable, then
// the receiver continues serving whatever the other stream provides.
func (r *sysfsReader) markDegraded(err error) {
	r.degraded.Do(func() {
		r.logger.Error("unable to read Neuron sysfs devices; sysfs metrics unavailable, continuing",
			zap.String("root", r.root), zap.Error(err))
	})
}

func newSysfsReader(root string, logger *zap.Logger) *sysfsReader {
	return &sysfsReader{root: root, logger: logger}
}

// record walks the sysfs tree and records the finer-grained sysfs metrics. When
// monitorActive is false (neuron-monitor absent), it also records ECC and sets
// the resource topology, acting as the graceful-degradation fallback.
func (r *sysfsReader) record(mb *metadata.MetricsBuilder, now pcommon.Timestamp, rb *metadata.ResourceBuilder, monitorActive bool) {
	devices, err := filepath.Glob(filepath.Join(r.root, "neuron*"))
	if err != nil || len(devices) == 0 {
		r.markDegraded(err) // single error log, then continue
		return
	}
	for _, dev := range devices {
		devIdx := strings.TrimPrefix(filepath.Base(dev), "neuron")
		if _, err := strconv.Atoi(devIdx); err != nil {
			continue // e.g. "neuron_device" root itself
		}

		// Device-level host memory, by sysfs category (present + peak).
		r.recordCategoryMem(filepath.Join(dev, "stats/memory_usage/host_mem"), func(cat string, st metadata.AttributeMemoryState, v int64) {
			mb.RecordAwsNeuronDeviceHostMemoryUsageDataPoint(now, v, cat, st)
		})

		if mn, mx, avg, ok := r.readPowerUtil(filepath.Join(dev, "stats/power/utilization")); ok {
			mb.RecordAwsNeuronDevicePowerUtilizationDataPoint(now, mn, metadata.AttributePowerStatisticMin)
			mb.RecordAwsNeuronDevicePowerUtilizationDataPoint(now, mx, metadata.AttributePowerStatisticMax)
			mb.RecordAwsNeuronDevicePowerUtilizationDataPoint(now, avg, metadata.AttributePowerStatisticAvg)
		}

		// ECC: the repairable count is unique to sysfs and is always emitted; the
		// uncorrected counts overlap neuron-monitor's identical series, so sysfs
		// emits them only when the monitor is absent (avoids double-counting).
		r.recordSysfsECC(mb, now, devIdx, filepath.Join(dev, "stats/hardware"), monitorActive)

		if !monitorActive {
			// Topology is identical from both sources; sysfs sets it only as a fallback.
			if name := r.readStr(filepath.Join(dev, "info/architecture/device_name")); name != "" {
				rb.SetAwsNeuronDeviceType(strings.ToLower(name))
			}
			if arch := r.readStr(filepath.Join(dev, "neuron_core0/info/architecture/arch_type")); arch != "" {
				rb.SetAwsNeuronNeuroncoreVersion(strings.ToLower(arch))
			}
		}

		cores, _ := filepath.Glob(filepath.Join(dev, "neuron_core*"))
		for _, core := range cores {
			ci, err := strconv.ParseInt(strings.TrimPrefix(filepath.Base(core), "neuron_core"), 10, 64)
			if err != nil {
				continue
			}
			r.recordCategoryMem(filepath.Join(core, "stats/memory_usage/device_mem"), func(cat string, st metadata.AttributeMemoryState, v int64) {
				mb.RecordAwsNeuronNeuroncoreDeviceMemoryUsageDataPoint(now, v, ci, cat, st)
			})
			if v, ok := r.readUint(filepath.Join(core, "stats/memory_usage/host_mem/present")); ok {
				mb.RecordAwsNeuronNeuroncoreHostMemoryUsageDataPoint(now, v, ci, metadata.AttributeMemoryStatePresent)
			}
			if v, ok := r.readUint(filepath.Join(core, "stats/memory_usage/host_mem/peak")); ok {
				mb.RecordAwsNeuronNeuroncoreHostMemoryUsageDataPoint(now, v, ci, metadata.AttributeMemoryStatePeak)
			}
			// Runtime-gated counters (default-off; recorded so enabling works).
			if v, ok := r.readUint(filepath.Join(core, "stats/other_info/nc_time_in_use/total")); ok {
				mb.RecordAwsNeuronNeuroncoreTimeInUseDataPoint(now, v, ci)
			}
			if v, ok := r.readUint(filepath.Join(core, "stats/other_info/inference_count/total")); ok {
				mb.RecordAwsNeuronNeuroncoreInferencesDataPoint(now, v, ci)
			}
			r.recordStatusCounters(mb, now, ci, filepath.Join(core, "stats/status"))
		}
	}
}

// recordCategoryMem reads each <category>/{present,peak} pair under dir.
func (r *sysfsReader) recordCategoryMem(dir string, rec func(cat string, st metadata.AttributeMemoryState, v int64)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		r.logger.Debug("sysfs directory unreadable", zap.String("path", dir), zap.Error(err))
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue // skip the aggregate present/peak/total files at this level
		}
		cat := e.Name()
		if v, ok := r.readUint(filepath.Join(dir, cat, "present")); ok {
			rec(cat, metadata.AttributeMemoryStatePresent, v)
		}
		if v, ok := r.readUint(filepath.Join(dir, cat, "peak")); ok {
			rec(cat, metadata.AttributeMemoryStatePeak, v)
		}
	}
}

func (r *sysfsReader) recordStatusCounters(mb *metadata.MetricsBuilder, now pcommon.Timestamp, core int64, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		r.logger.Debug("sysfs directory unreadable", zap.String("path", dir), zap.Error(err))
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if v, ok := r.readUint(filepath.Join(dir, e.Name(), "total")); ok {
			mb.RecordAwsNeuronNeuroncoreStatusDataPoint(now, v, core, e.Name())
		}
	}
}

// recordSysfsECC records sysfs ECC counters. The repairable count is unique to
// sysfs (neuron-monitor doesn't expose it), so it is always emitted. The
// uncorrected counts are the same metric+attributes neuron-monitor already
// reports, so they are emitted from sysfs only when the monitor is inactive, to
// avoid double-counting those series. (sysfs never exposes corrected counts.)
func (r *sysfsReader) recordSysfsECC(mb *metadata.MetricsBuilder, now pcommon.Timestamp, devIdx, dir string, monitorActive bool) {
	if v, ok := r.readUint(filepath.Join(dir, "mem_ecc_repairable_uncorrected")); ok {
		mb.RecordAwsNeuronErrorsDataPoint(now, v, devIdx, "dram", "repairable")
	}
	if monitorActive {
		return // neuron-monitor owns the uncorrected series while it is running
	}
	if v, ok := r.readUint(filepath.Join(dir, "mem_ecc_uncorrected")); ok {
		mb.RecordAwsNeuronErrorsDataPoint(now, v, devIdx, "dram", "uncorrected")
	}
	if v, ok := r.readUint(filepath.Join(dir, "sram_ecc_uncorrected")); ok {
		mb.RecordAwsNeuronErrorsDataPoint(now, v, devIdx, "sram", "uncorrected")
	}
}

// readUint reads an integer from a sysfs file. Missing or malformed files are
// logged at Debug and reported as not-ok, so partial trees are tolerated.
func (r *sysfsReader) readUint(path string) (int64, bool) {
	b, err := os.ReadFile(path) // #nosec G304 -- path is a constant sysfs root joined with driver-provided node names, read-only
	if err != nil {
		r.logger.Debug("sysfs file unreadable", zap.String("path", path), zap.Error(err))
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		r.logger.Debug("sysfs value not an integer", zap.String("path", path), zap.Error(err))
		return 0, false
	}
	return n, true
}

func (r *sysfsReader) readStr(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- path is a constant sysfs root joined with driver-provided node names, read-only
	if err != nil {
		r.logger.Debug("sysfs file unreadable", zap.String("path", path), zap.Error(err))
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readPowerUtil parses "POWER_STATUS_VALID,<epoch>,<min>,<max>,<avg>" and returns
// the min, max, and avg as fractions of the device's max power (the sysfs values
// are percentages). AWS documents only that these refresh ~every minute; the
// averaging-window length is unspecified. Best-effort: many instances report zeros.
func (r *sysfsReader) readPowerUtil(path string) (minPct, maxPct, avgPct float64, ok bool) {
	s := r.readStr(path)
	if s == "" || !strings.Contains(s, "VALID") {
		return 0, 0, 0, false
	}
	parts := strings.Split(s, ",")
	if len(parts) < 5 {
		r.logger.Debug("sysfs power utilization malformed", zap.String("path", path), zap.String("value", s))
		return 0, 0, 0, false
	}
	// Trailing fields are min, max, avg (percentages); index from the end so any
	// leading fields (status, epoch) don't shift the parse.
	n := len(parts)
	mn, e1 := strconv.ParseFloat(strings.TrimSpace(parts[n-3]), 64)
	mx, e2 := strconv.ParseFloat(strings.TrimSpace(parts[n-2]), 64)
	av, e3 := strconv.ParseFloat(strings.TrimSpace(parts[n-1]), 64)
	if e1 != nil || e2 != nil || e3 != nil {
		r.logger.Debug("sysfs power utilization malformed", zap.String("path", path), zap.String("value", s))
		return 0, 0, 0, false
	}
	return mn / 100.0, mx / 100.0, av / 100.0, true
}
