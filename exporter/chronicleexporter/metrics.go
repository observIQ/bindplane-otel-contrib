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

package chronicleexporter

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/observiq/bindplane-otel-contrib/exporter/chronicleexporter/protos/api"
	"github.com/shirou/gopsutil/v3/process"
	"go.opentelemetry.io/collector/component"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type metricsReporter struct {
	set    component.TelemetrySettings
	cancel context.CancelFunc
	wg     sync.WaitGroup

	send     sendMetricsFunc
	interval time.Duration

	mutex      sync.Mutex
	agentID    []byte
	exporterID string
	source     *api.EventSource

	startTime  *timestamppb.Timestamp
	agentStats *api.AgentStatsEvent
}

type sendMetricsFunc func(context.Context, *api.BatchCreateEventsRequest) error

func newMetricsReporter(cfg *Config, set component.TelemetrySettings, exporterID string, send sendMetricsFunc) (*metricsReporter, error) {
	customerID, err := uuid.Parse(cfg.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("parse customer ID: %w", err)
	}

	agentID := uuid.New()
	if sid, ok := set.Resource.Attributes().Get(string(semconv.ServiceInstanceIDKey)); ok {
		serviceID, err := uuid.Parse(sid.AsString())
		if err != nil {
			set.Logger.Error("Failed to parse service instance ID, using random ID", zap.String("service_instance_id", sid.AsString()), zap.Error(err))
		} else {
			agentID = serviceID
		}
	}

	now := timestamppb.Now()
	mr := &metricsReporter{
		set:        set,
		send:       send,
		interval:   cfg.MetricsInterval,
		agentID:    agentID[:],
		exporterID: exporterID,
		source: &api.EventSource{
			CustomerId:  customerID[:],
			CollectorId: getCollectorID(cfg.LicenseType),
			Namespace:   cfg.Namespace,
		},
		startTime:  now,
		agentStats: newAgentStats(agentID[:], now, now, exporterID),
	}

	return mr, nil
}

func (mr *metricsReporter) start() {
	ctx, cancel := context.WithCancel(context.Background())
	mr.cancel = cancel
	mr.wg.Add(1)

	go func() {
		ticker := time.NewTicker(mr.interval)

		defer func() {
			mr.wg.Done()
			ticker.Stop()
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := mr.collectHostMetrics(); err != nil {
					mr.set.Logger.Error("Failed to collect host metrics", zap.Error(err))
				}
				request, sent := mr.drainRequest()
				if err := mr.send(ctx, request); err != nil {
					mr.set.Logger.Error("Failed to upload metrics", zap.Error(err))
					mr.restore(sent) // retry these counters in the next window
				}
			}
		}
	}()
}

// drainRequest snapshots the current window into a request and starts a fresh one atomically.
func (mr *metricsReporter) drainRequest() (*api.BatchCreateEventsRequest, *api.AgentStatsEvent) {
	mr.mutex.Lock()
	defer mr.mutex.Unlock()

	stats := mr.agentStats
	now := timestamppb.Now()
	batchID := uuid.New()

	request := &api.BatchCreateEventsRequest{
		Batch: &api.EventBatch{
			Id:        batchID[:],
			Source:    mr.source,
			Type:      api.EventBatch_AGENT_STATS,
			StartTime: mr.startTime,
			Events: []*api.Event{
				{
					Timestamp:      now,
					CollectionTime: now,
					Source:         mr.source,
					Payload: &api.Event_AgentStats{
						AgentStats: stats,
					},
				},
			},
		},
	}

	mr.agentStats = newAgentStats(mr.agentID, mr.startTime, now, mr.exporterID)
	return request, stats
}

// restore merges a failed upload's counters back so they retry next window.
func (mr *metricsReporter) restore(failed *api.AgentStatsEvent) {
	mr.mutex.Lock()
	defer mr.mutex.Unlock()

	if len(mr.agentStats.ExporterStats) == 0 || len(failed.ExporterStats) == 0 {
		return
	}
	mr.agentStats.ExporterStats[0].AcceptedSpans += failed.ExporterStats[0].AcceptedSpans
	mr.agentStats.ExporterStats[0].RefusedSpans += failed.ExporterStats[0].RefusedSpans
	mr.agentStats.WindowStartTime = failed.WindowStartTime
}

func (mr *metricsReporter) shutdown() {
	if mr.cancel != nil {
		mr.cancel()
		mr.wg.Wait()
	}
}

// collectHostMetrics collects the host metrics and updates the agent stats object
func (mr *metricsReporter) collectHostMetrics() error {
	mr.mutex.Lock()
	defer mr.mutex.Unlock()

	// Get the current process using the current PID
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return fmt.Errorf("get process: %w", err)
	}

	// Collect CPU time used by the process
	cpuTimes, err := proc.Times()
	if err != nil {
		return fmt.Errorf("get cpu times: %w", err)
	}
	mr.agentStats.ProcessCpuSeconds = int64(cpuTimes.User + cpuTimes.System)

	// Collect memory usage (RSS)
	memInfo, err := proc.MemoryInfo()
	if err != nil {
		return fmt.Errorf("get memory info: %w", err)
	}
	mr.agentStats.ProcessMemoryRss = int64(memInfo.RSS / 1024) // Convert bytes to kilobytes

	// Calculate process uptime
	startTimeMs, err := proc.CreateTime()
	if err != nil {
		return fmt.Errorf("get process start time: %w", err)
	}
	startTimeSec := startTimeMs / 1000
	currentTimeSec := time.Now().Unix()
	mr.agentStats.ProcessUptime = currentTimeSec - startTimeSec

	return nil
}

// newAgentStats returns a fresh agent stats object for a new window.
func newAgentStats(agentID []byte, startTime, windowStartTime *timestamppb.Timestamp, exporterID string) *api.AgentStatsEvent {
	return &api.AgentStatsEvent{
		AgentId:         agentID,
		StartTime:       startTime,
		WindowStartTime: windowStartTime,
		ExporterStats: []*api.ExporterStats{
			{
				Name: exporterID,
			},
		},
	}
}

func (mr *metricsReporter) recordSent(count int64) {
	mr.mutex.Lock()
	defer mr.mutex.Unlock()

	if len(mr.agentStats.ExporterStats) == 0 {
		mr.agentStats.ExporterStats = []*api.ExporterStats{
			{
				Name: mr.exporterID,
			},
		}
	}

	mr.agentStats.ExporterStats[0].AcceptedSpans += count
	mr.agentStats.LastSuccessfulUploadTime = timestamppb.Now()
}

func (mr *metricsReporter) recordDropped(count int64) {
	mr.mutex.Lock()
	defer mr.mutex.Unlock()

	if len(mr.agentStats.ExporterStats) == 0 {
		mr.agentStats.ExporterStats = []*api.ExporterStats{
			{
				Name: mr.exporterID,
			},
		}
	}

	mr.agentStats.ExporterStats[0].RefusedSpans += count
}
