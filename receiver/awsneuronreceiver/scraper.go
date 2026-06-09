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
	"strconv"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver"

	"github.com/observiq/bindplane-otel-contrib/receiver/awsneuronreceiver/internal/metadata"
)

const defaultSysfsRoot = "/sys/devices/virtual/neuron_device"

type neuronScraper struct {
	cfg      *Config
	settings receiver.Settings
	mb       *metadata.MetricsBuilder
	runner   *runner
	sysfs    *sysfsReader
}

func newNeuronScraper(params receiver.Settings, cfg *Config) *neuronScraper {
	return &neuronScraper{cfg: cfg, settings: params}
}

func (s *neuronScraper) start(_ context.Context, _ component.Host) error {
	s.mb = metadata.NewMetricsBuilder(s.cfg.MetricsBuilderConfig, s.settings)
	s.runner = newRunner(s.cfg.Command, s.cfg.ConfigFile, s.settings.Logger)
	s.runner.start(context.Background()) // background ctx: subprocess outlives start; shutdown cancels
	s.sysfs = newSysfsReader(defaultSysfsRoot, s.settings.Logger)
	return nil
}

func (s *neuronScraper) shutdown(_ context.Context) error {
	if s.runner != nil {
		s.runner.stop()
	}
	return nil
}

func (s *neuronScraper) scrape(_ context.Context) (pmetric.Metrics, error) {
	now := pcommon.NewTimestampFromTime(time.Now())
	rb := s.mb.NewResourceBuilder()

	rep := s.runner.latestReport()
	monitorActive := rep != nil
	if monitorActive {
		s.recordMonitor(now, rep)
		setResourceFromReport(rb, rep)
	}
	// sysfs supplements with finer-grained metrics, and serves as the fallback
	// (ECC, topology) when neuron-monitor is absent.
	s.sysfs.record(s.mb, now, rb, monitorActive)

	return s.mb.Emit(metadata.WithResource(rb.Emit())), nil
}

func (s *neuronScraper) recordMonitor(now pcommon.Timestamp, rep *nmReport) {
	for i := range rep.NeuronRuntimeData {
		r := &rep.NeuronRuntimeData[i].Report

		for coreKey, c := range r.NeuroncoreCounters.NeuroncoresInUse {
			core := coreToInt(coreKey)
			s.mb.RecordAwsNeuronNeuroncoreUtilizationDataPoint(now, c.NeuroncoreUtilization/100.0, core)
			s.mb.RecordAwsNeuronNeuroncoreFlopsDataPoint(now, c.EffectiveFlops, core)
		}

		es := &r.ExecutionStats
		for status, v := range es.ExecutionSummary {
			s.mb.RecordAwsNeuronExecutionCountDataPoint(now, v, status)
		}
		for etype, v := range es.ErrorSummary {
			s.mb.RecordAwsNeuronExecutionErrorsDataPoint(now, v, etype)
		}
		for q, v := range es.LatencyStats.TotalLatency {
			s.mb.RecordAwsNeuronExecutionLatencyDataPoint(now, v, metadata.AttributeLatencyTypeTotal, q)
		}
		for q, v := range es.LatencyStats.DeviceLatency {
			s.mb.RecordAwsNeuronExecutionLatencyDataPoint(now, v, metadata.AttributeLatencyTypeDevice, q)
		}

		mu := &r.MemoryUsed.NeuronRuntimeUsedBytes
		s.mb.RecordAwsNeuronRuntimeMemoryUsageDataPoint(now, mu.Host, "host")
		s.mb.RecordAwsNeuronRuntimeMemoryUsageDataPoint(now, mu.NeuronDevice, "device")
		for coreKey, cats := range mu.UsageBreakdown.NeuroncoreMemoryUsage {
			core := coreToInt(coreKey)
			for cat, b := range cats {
				s.mb.RecordAwsNeuronNeuroncoreMemoryUsageDataPoint(now, b, core, cat)
			}
		}

		for state, v := range r.RuntimeVcpuUsage.VcpuUsage { // default-off
			s.mb.RecordAwsNeuronRuntimeVcpuUtilizationDataPoint(now, v/100.0, state)
		}
	}

	// ECC from neuron-monitor hardware counters (superset: includes corrected).
	for _, d := range rep.SystemData.NeuronHwCounters.NeuronDevices {
		id := strconv.Itoa(d.NeuronDeviceIndex)
		s.mb.RecordAwsNeuronErrorsDataPoint(now, d.MemEccCorrected, id, "dram", "corrected")
		s.mb.RecordAwsNeuronErrorsDataPoint(now, d.MemEccUncorrected, id, "dram", "uncorrected")
		s.mb.RecordAwsNeuronErrorsDataPoint(now, d.SramEccCorrected, id, "sram", "corrected")
		s.mb.RecordAwsNeuronErrorsDataPoint(now, d.SramEccUncorrected, id, "sram", "uncorrected")
	}

	// default-off system duplicates of hostmetrics.
	mi := rep.SystemData.MemoryInfo
	if mi.MemoryTotalBytes > 0 {
		s.mb.RecordAwsNeuronSystemMemoryUsageDataPoint(now, mi.MemoryUsedBytes, "used")
		s.mb.RecordAwsNeuronSystemMemoryUsageDataPoint(now, mi.MemoryTotalBytes, "total")
	}
	for state, v := range rep.SystemData.VcpuUsage.AverageUsage {
		s.mb.RecordAwsNeuronSystemCPUUtilizationDataPoint(now, v/100.0, "average", state)
	}
}

func setResourceFromReport(rb *metadata.ResourceBuilder, rep *nmReport) {
	if ii := rep.InstanceInfo; ii.InstanceID != "" {
		rb.SetCloudProvider("aws")
		rb.SetHostID(ii.InstanceID)
		rb.SetHostType(ii.InstanceType)
		rb.SetCloudRegion(ii.InstanceRegion)
		rb.SetCloudAvailabilityZone(ii.InstanceAvailabilityZone)
	}
	if hi := rep.NeuronHardwareInfo; hi.NeuronDeviceType != "" {
		rb.SetAwsNeuronDeviceType(hi.NeuronDeviceType)
		rb.SetAwsNeuronNeuroncoreVersion(hi.NeuroncoreVersion)
	}
}

func coreToInt(coreKey string) int64 {
	n, _ := strconv.ParseInt(coreKey, 10, 64)
	return n
}
