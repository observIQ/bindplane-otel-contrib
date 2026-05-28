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
	hmr := &metricsReporter{
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
		startTime: now,
	}

	hmr.resetWindow(now)
	return hmr, nil
}

func (hmr *metricsReporter) start() {
	ctx, cancel := context.WithCancel(context.Background())
	hmr.cancel = cancel
	hmr.wg.Add(1)

	go func() {
		ticker := time.NewTicker(hmr.interval)

		defer func() {
			hmr.wg.Done()
			ticker.Stop()
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := hmr.collectHostMetrics(); err != nil {
					hmr.set.Logger.Error("Failed to collect host metrics", zap.Error(err))
				}
				request, sent := hmr.drainRequest()
				if err := hmr.send(ctx, request); err != nil {
					hmr.set.Logger.Error("Failed to upload metrics", zap.Error(err))
					hmr.restore(sent) // retry these counters in the next window
				}
			}
		}
	}()
}

// drainRequest snapshots the current window into a request and starts a fresh one atomically.
func (hmr *metricsReporter) drainRequest() (*api.BatchCreateEventsRequest, *api.AgentStatsEvent) {
	hmr.mutex.Lock()
	defer hmr.mutex.Unlock()

	stats := hmr.agentStats
	now := timestamppb.Now()
	batchID := uuid.New()

	request := &api.BatchCreateEventsRequest{
		Batch: &api.EventBatch{
			Id:        batchID[:],
			Source:    hmr.source,
			Type:      api.EventBatch_AGENT_STATS,
			StartTime: hmr.startTime,
			Events: []*api.Event{
				{
					Timestamp:      now,
					CollectionTime: now,
					Source:         hmr.source,
					Payload: &api.Event_AgentStats{
						AgentStats: stats,
					},
				},
			},
		},
	}

	hmr.agentStats = newAgentStats(hmr.agentID, hmr.startTime, now, hmr.exporterID)
	return request, stats
}

// restore merges a failed upload's counters back so they retry next window.
func (hmr *metricsReporter) restore(failed *api.AgentStatsEvent) {
	hmr.mutex.Lock()
	defer hmr.mutex.Unlock()

	if len(hmr.agentStats.ExporterStats) == 0 || len(failed.ExporterStats) == 0 {
		return
	}
	hmr.agentStats.ExporterStats[0].AcceptedSpans += failed.ExporterStats[0].AcceptedSpans
	hmr.agentStats.ExporterStats[0].RefusedSpans += failed.ExporterStats[0].RefusedSpans
	hmr.agentStats.WindowStartTime = failed.WindowStartTime
}

func (hmr *metricsReporter) shutdown() {
	if hmr.cancel != nil {
		hmr.cancel()
		hmr.wg.Wait()
	}
}

// collectHostMetrics collects the host metrics and updates the agent stats object
func (hmr *metricsReporter) collectHostMetrics() error {
	hmr.mutex.Lock()
	defer hmr.mutex.Unlock()

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
	hmr.agentStats.ProcessCpuSeconds = int64(cpuTimes.User + cpuTimes.System)

	// Collect memory usage (RSS)
	memInfo, err := proc.MemoryInfo()
	if err != nil {
		return fmt.Errorf("get memory info: %w", err)
	}
	hmr.agentStats.ProcessMemoryRss = int64(memInfo.RSS / 1024) // Convert bytes to kilobytes

	// Calculate process uptime
	startTimeMs, err := proc.CreateTime()
	if err != nil {
		return fmt.Errorf("get process start time: %w", err)
	}
	startTimeSec := startTimeMs / 1000
	currentTimeSec := time.Now().Unix()
	hmr.agentStats.ProcessUptime = currentTimeSec - startTimeSec

	return nil
}

// resetWindow resets the agent stats object and sets the window start time
func (hmr *metricsReporter) resetWindow(windowStartTime *timestamppb.Timestamp) {
	hmr.mutex.Lock()
	defer hmr.mutex.Unlock()

	hmr.agentStats = newAgentStats(hmr.agentID, hmr.startTime, windowStartTime, hmr.exporterID)
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

func (hmr *metricsReporter) recordSent(count int64) {
	hmr.mutex.Lock()
	defer hmr.mutex.Unlock()

	if len(hmr.agentStats.ExporterStats) == 0 {
		hmr.agentStats.ExporterStats = []*api.ExporterStats{
			{
				Name: hmr.exporterID,
			},
		}
	}

	hmr.agentStats.ExporterStats[0].AcceptedSpans += count
	hmr.agentStats.LastSuccessfulUploadTime = timestamppb.Now()
}

func (hmr *metricsReporter) recordDropped(count int64) {
	hmr.mutex.Lock()
	defer hmr.mutex.Unlock()

	if len(hmr.agentStats.ExporterStats) == 0 {
		hmr.agentStats.ExporterStats = []*api.ExporterStats{
			{
				Name: hmr.exporterID,
			},
		}
	}

	hmr.agentStats.ExporterStats[0].RefusedSpans += count
}
