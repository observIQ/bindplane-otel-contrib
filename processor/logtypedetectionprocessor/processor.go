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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
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
	logTypes       *lru.Cache[uint64, string]
	detectionGroup singleflight.Group

	id  component.ID
	cfg *Config

	matchers    []Matcher
	matcherHash string

	stopped bool

	fingerprintStorageClient storageclient.StorageClient
	fingerprintPersistCancel context.CancelFunc
	fingerprintPersistDone   chan struct{}

	telemetry *metadata.TelemetryBuilder
	logger    *zap.Logger
}

const fingerprintStorageKey = "fingerprints"

type persistedFingerprints struct {
	MatcherHash string            `json:"matcher_hash"`
	LogTypes    map[string]string `json:"log_types"`
}

func (m *persistedFingerprints) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

func (m *persistedFingerprints) Unmarshal(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, m)
}

func hashMatchers(matchers []MatcherConfig) (string, error) {
	encoded, err := json.Marshal(matchers)
	if err != nil {
		return "", fmt.Errorf("encode matchers: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func newLogTypeDetectionProcessor(cfg *Config, id component.ID, logger *zap.Logger, telemetry *metadata.TelemetryBuilder) (*logTypeDetectionProcessor, error) {
	logTypes, err := lru.New[uint64, string](cfg.MaxSavedFingerprints)
	if err != nil {
		return nil, fmt.Errorf("create fingerprint cache: %w", err)
	}

	p := &logTypeDetectionProcessor{
		logTypes:                 logTypes,
		telemetry:                telemetry,
		logger:                   logger,
		id:                       id,
		cfg:                      cfg,
		fingerprintStorageClient: storageclient.NewNopStorage(),
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

		if p.matcherHash, err = hashMatchers(matchers); err != nil {
			return nil, err
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
	if p.cfg.FingerprintStorageID == nil {
		return nil
	}

	client, err := storageclient.NewStorageClient(
		ctx,
		host,
		component.KindProcessor,
		*p.cfg.FingerprintStorageID,
		p.id,
		pipeline.SignalLogs,
	)
	if err != nil {
		return fmt.Errorf("create storage client: %w", err)
	}
	saved := persistedFingerprints{}
	if err := client.LoadStorageData(ctx, fingerprintStorageKey, &saved); err != nil {
		return errors.Join(fmt.Errorf("load log types: %w", err), client.Close(ctx))
	}
	p.fingerprintStorageClient = client

	if saved.MatcherHash != p.matcherHash {
		if len(saved.LogTypes) > 0 {
			p.logger.Info("matcher config changed, discarding persisted log types")
		}
		saved.LogTypes = nil
	}

	for key, logType := range saved.LogTypes {
		logFingerprint, err := strconv.ParseUint(key, 16, 64)
		if err != nil {
			continue
		}
		p.logTypes.Add(logFingerprint, logType)
	}

	persistCtx, cancel := context.WithCancel(context.Background())
	p.fingerprintPersistCancel = cancel
	p.fingerprintPersistDone = make(chan struct{})
	go p.fingerprintPersistLoop(persistCtx)

	return nil
}

func (p *logTypeDetectionProcessor) fingerprintPersistLoop(ctx context.Context) {
	defer close(p.fingerprintPersistDone)

	ticker := time.NewTicker(p.cfg.FingerprintPersistInterval)
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
	toSave := map[string]string{}
	for _, logFingerprint := range p.logTypes.Keys() {
		logType, ok := p.logTypes.Peek(logFingerprint)
		if !ok {
			continue
		}
		toSave[strconv.FormatUint(logFingerprint, 16)] = logType
	}

	state := persistedFingerprints{MatcherHash: p.matcherHash, LogTypes: toSave}
	if err := p.fingerprintStorageClient.SaveStorageData(ctx, fingerprintStorageKey, &state); err != nil {
		return fmt.Errorf("save log types: %w", err)
	}
	return nil
}

func (p *logTypeDetectionProcessor) stop(ctx context.Context) error {
	if p.stopped {
		return nil
	}
	p.stopped = true
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
					logRecord.Attributes().PutStr(p.cfg.LogTypeField, unknownLogType)
					continue
				}
				if p.cfg.FingerprintField != "" {
					logRecord.Attributes().PutStr(p.cfg.FingerprintField, strconv.FormatUint(logFingerprint, 16))
				}
				logType, ok := p.logTypes.Get(logFingerprint)
				if !ok {
					newLogType, err, _ := p.detectionGroup.Do(
						strconv.FormatUint(logFingerprint, 10),
						func() (any, error) {
							// An earlier flight may have finished since we missed the cache.
							if cached, ok := p.logTypes.Get(logFingerprint); ok {
								return cached, nil
							}
							logType := p.logType(ctx, body)
							p.logTypes.Add(logFingerprint, logType)
							return logType, nil
						},
					)
					if err != nil {
						return ld, err
					}
					logType = newLogType.(string)
				}
				if logType == "" {
					logType = unknownLogType
					p.telemetry.ProcessorLogTypeDetectionLogsUnclassified.Add(ctx, 1)
				} else {
					p.telemetry.ProcessorLogTypeDetectionLogsClassified.Add(ctx, 1,
						metric.WithAttributes(attribute.String("log_type", logType)))
				}
				logRecord.Attributes().PutStr(p.cfg.LogTypeField, logType)
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
