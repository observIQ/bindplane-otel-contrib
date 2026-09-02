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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/fingerprint"
	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pipeline"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

type logTypeDetectionProcessor struct {
	logTypes       sync.Map
	logTypeCount   atomic.Int64
	detectionGroup singleflight.Group

	logTypeField     string
	fingerprintField string

	matchers []Matcher

	id                         component.ID
	fingerprintStorageID       *component.ID
	fingerprintStorageClient   storageclient.StorageClient
	fingerprintPersistInterval time.Duration
	maxSavedFingerprints       int64
	fingerprintPersistCancel   context.CancelFunc
	fingerprintPersistDone     chan struct{}

	telemetry *metadata.TelemetryBuilder
	logger    *zap.Logger
}

const fingerprintStorageKey = "fingerprints"

// logTypeMap is the persisted form of the fingerprint map.
type logTypeMap map[string]string

func (m *logTypeMap) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

func (m *logTypeMap) Unmarshal(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, m)
}

func newLogTypeDetectionProcessor(cfg *Config, id component.ID, logger *zap.Logger, telemetry *metadata.TelemetryBuilder) (*logTypeDetectionProcessor, error) {
	p := &logTypeDetectionProcessor{
		telemetry:                  telemetry,
		logger:                     logger,
		logTypeField:               cfg.LogTypeField,
		fingerprintField:           cfg.FingerprintField,
		id:                         id,
		fingerprintStorageID:       cfg.FingerprintStorageID,
		fingerprintStorageClient:   storageclient.NewNopStorage(),
		fingerprintPersistInterval: cfg.FingerprintPersistInterval,
		maxSavedFingerprints:       int64(cfg.MaxSavedFingerprints),
	}

	if cfg.Matchers != nil {
		matchers := make([]MatcherConfig, len(cfg.Matchers))
		copy(matchers, cfg.Matchers)
		slices.SortStableFunc(matchers, func(i, j MatcherConfig) int {
			return priorityRank(i.Priority) - priorityRank(j.Priority)
		})

		for _, m := range matchers {
			matcher, err := m.Build()
			if err != nil {
				return nil, err
			}
			p.matchers = append(p.matchers, matcher)
		}
	}

	return p, nil
}

// priorityRank orders unset priority last.
func priorityRank(priority *int) int {
	if priority == nil {
		return math.MaxInt
	}
	return *priority
}

func (p *logTypeDetectionProcessor) start(ctx context.Context, host component.Host) error {
	if p.fingerprintStorageID == nil {
		return nil
	}

	client, err := storageclient.NewStorageClient(ctx, host, *p.fingerprintStorageID, p.id, pipeline.SignalLogs)
	if err != nil {
		return fmt.Errorf("create storage client: %w", err)
	}
	saved := logTypeMap{}
	if err := client.LoadStorageData(ctx, fingerprintStorageKey, &saved); err != nil {
		return errors.Join(fmt.Errorf("load log types: %w", err), client.Close(ctx))
	}
	p.fingerprintStorageClient = client

	for key, logType := range saved {
		logFingerprint, err := strconv.ParseUint(key, 16, 64)
		if err != nil {
			continue
		}
		p.storeFingerprintMapping(logFingerprint, logType)
	}

	persistCtx, cancel := context.WithCancel(context.Background())
	p.fingerprintPersistCancel = cancel
	p.fingerprintPersistDone = make(chan struct{})
	go p.fingerprintPersistLoop(persistCtx)

	return nil
}

func (p *logTypeDetectionProcessor) storeFingerprintMapping(logFingerprint uint64, logType string) {
	if p.logTypeCount.Load() >= p.maxSavedFingerprints {
		return
	}
	if _, loaded := p.logTypes.LoadOrStore(logFingerprint, logType); !loaded {
		p.logTypeCount.Add(1)
	}
}

func (p *logTypeDetectionProcessor) fingerprintPersistLoop(ctx context.Context) {
	defer close(p.fingerprintPersistDone)

	ticker := time.NewTicker(p.fingerprintPersistInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.save(ctx); err != nil {
				p.logger.Error("persist log types", zap.Error(err))
			}
		}
	}
}

func (p *logTypeDetectionProcessor) save(ctx context.Context) error {
	toSave := logTypeMap{}
	p.logTypes.Range(func(key, value any) bool {
		logFingerprint, ok := key.(uint64)
		if !ok {
			return true
		}
		logType, ok := value.(string)
		if !ok || logType == "" {
			return true
		}
		toSave[strconv.FormatUint(logFingerprint, 16)] = logType
		return true
	})

	if err := p.fingerprintStorageClient.SaveStorageData(ctx, fingerprintStorageKey, &toSave); err != nil {
		return fmt.Errorf("save log types: %w", err)
	}
	return nil
}

func (p *logTypeDetectionProcessor) stop(ctx context.Context) error {
	p.telemetry.Shutdown()

	if p.fingerprintPersistCancel == nil {
		return p.fingerprintStorageClient.Close(ctx)
	}

	p.fingerprintPersistCancel()
	<-p.fingerprintPersistDone

	return errors.Join(p.save(ctx), p.fingerprintStorageClient.Close(ctx))
}

func (p *logTypeDetectionProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		resourceLogs := ld.ResourceLogs().At(i)
		for j := 0; j < resourceLogs.ScopeLogs().Len(); j++ {
			scopeLogs := resourceLogs.ScopeLogs().At(j)
			for k := 0; k < scopeLogs.LogRecords().Len(); k++ {
				logRecord := scopeLogs.LogRecords().At(k)
				body := logRecord.Body().AsString()
				logFingerprint := fingerprint.HashLog(body)
				if logFingerprint == 0 {
					p.telemetry.ProcessorLogTypeDetectionLogsUnclassified.Add(ctx, 1)
					logRecord.Attributes().PutStr(p.logTypeField, unknownLogType)
					continue
				}
				if p.fingerprintField != "" {
					logRecord.Attributes().PutStr(p.fingerprintField, strconv.FormatUint(logFingerprint, 16))
				}
				logType, ok := p.logTypes.Load(logFingerprint)
				if !ok {
					newLogType, err, _ := p.detectionGroup.Do(
						strconv.FormatUint(logFingerprint, 10),
						func() (any, error) {
							// An earlier flight may have finished since we missed the cache.
							if cached, ok := p.logTypes.Load(logFingerprint); ok {
								return cached, nil
							}
							logType := p.logType(ctx, body)
							p.storeFingerprintMapping(logFingerprint, logType)
							return logType, nil
						},
					)
					if err != nil {
						return ld, err
					}
					logType = newLogType.(string)
				}
				lt, _ := logType.(string)
				if lt == "" {
					lt = unknownLogType
					p.telemetry.ProcessorLogTypeDetectionLogsUnclassified.Add(ctx, 1)
				} else {
					p.telemetry.ProcessorLogTypeDetectionLogsClassified.Add(ctx, 1,
						metric.WithAttributes(attribute.String("log_type", lt)))
				}
				logRecord.Attributes().PutStr(p.logTypeField, lt)
			}
		}
	}
	return ld, nil
}

func (p *logTypeDetectionProcessor) logType(ctx context.Context, logData string) string {
	p.telemetry.ProcessorLogTypeDetectionAttempts.Add(ctx, 1)
	for _, m := range p.matchers {
		if m.Test(logData) {
			p.telemetry.ProcessorLogTypeDetectionAttemptsMatched.Add(ctx, 1)
			return m.Name()
		}
	}
	return ""
}
