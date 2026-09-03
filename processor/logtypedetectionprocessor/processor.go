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
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/fingerprint"
	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/metadata"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
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

	matcherMux     sync.RWMutex
	matchers       []Matcher
	matcherHash    string
	matcherVersion string

	matcherStorageClient storageclient.StorageClient
	matcherStorageOwned  bool

	opampHandler    opampcustommessages.CustomCapabilityHandler
	opampDone       chan struct{}
	opampWg         sync.WaitGroup
	matchersReady   chan struct{}
	matchersOnce    sync.Once
	pendingLogTypes *persistedFingerprints

	stopped bool

	fingerprintStorageClient storageclient.StorageClient
	fingerprintPersistCancel context.CancelFunc
	fingerprintPersistDone   chan struct{}

	telemetry *metadata.TelemetryBuilder
	logger    *zap.Logger
}

const fingerprintStorageKey = "fingerprints"

// logTypeMap is the persisted form of the fingerprint map.
type logTypeMap map[string]string

// persistedFingerprints ties the fingerprint map to the matcher config that produced it.
type persistedFingerprints struct {
	MatcherHash string     `json:"matcher_hash"`
	LogTypes    logTypeMap `json:"log_types"`
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

// buildMatchers compiles the matcher configs in priority order and hashes them.
func buildMatchers(configs []MatcherConfig) ([]Matcher, string, error) {
	if len(configs) == 0 {
		return nil, "", nil
	}

	sorted := make([]MatcherConfig, len(configs))
	copy(sorted, configs)
	slices.SortStableFunc(sorted, func(i, j MatcherConfig) int {
		return priorityRank(i.Priority) - priorityRank(j.Priority)
	})

	matchers := make([]Matcher, 0, len(sorted))
	for _, m := range sorted {
		matcher, err := m.Build()
		if err != nil {
			return nil, "", err
		}
		matchers = append(matchers, matcher)
	}

	hash, err := hashMatchers(sorted)
	if err != nil {
		return nil, "", err
	}

	return matchers, hash, nil
}

// hashMatchers renders the matcher config to JSON and hashes it, so a config
// change invalidates log types detected under the old config.
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
		opampDone:                make(chan struct{}),
		matchersReady:            make(chan struct{}),
	}

	if p.matchers, p.matcherHash, err = buildMatchers(cfg.Matchers); err != nil {
		return nil, err
	}

	return p, nil
}

// setServerMatchers merges the opamp server's matchers with the configured
// ones. A changed matcher set invalidates every log type detected under the old
// one; an unchanged set keeps the cache as it is.
func (p *logTypeDetectionProcessor) setServerMatchers(version string, server []MatcherConfig) error {
	matchers, hash, err := buildMatchers(slices.Concat(p.cfg.Matchers, server))
	if err != nil {
		return err
	}

	p.matcherMux.Lock()
	defer p.matcherMux.Unlock()

	p.matcherVersion = version

	if hash != p.matcherHash {
		p.matchers = matchers
		p.matcherHash = hash
		p.logTypes.Purge()
	}

	if p.pendingLogTypes != nil {
		if p.pendingLogTypes.MatcherHash == hash {
			p.addSavedLogTypes(p.pendingLogTypes.LogTypes)
		}
		p.pendingLogTypes = nil
	}

	return nil
}

func (p *logTypeDetectionProcessor) currentVersion() string {
	p.matcherMux.RLock()
	defer p.matcherMux.RUnlock()

	return p.matcherVersion
}

func (p *logTypeDetectionProcessor) addSavedLogTypes(saved logTypeMap) {
	for key, logType := range saved {
		logFingerprint, err := strconv.ParseUint(key, 16, 64)
		if err != nil {
			continue
		}
		p.logTypes.Add(logFingerprint, logType)
	}
}

// priorityRank orders unset priority last.
func priorityRank(priority *int) int {
	if priority == nil {
		return math.MaxInt
	}
	return *priority
}

func (p *logTypeDetectionProcessor) start(ctx context.Context, host component.Host) error {
	if err := p.startStorage(ctx, host); err != nil {
		return err
	}

	if p.cfg.OpAMP == nil {
		return nil
	}

	return p.startOpAMP(ctx, host)
}

func (p *logTypeDetectionProcessor) startStorage(ctx context.Context, host component.Host) error {
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

	switch {
	case saved.MatcherHash == p.matcherHash:
		p.addSavedLogTypes(saved.LogTypes)
	case p.cfg.OpAMP != nil:
		// The matcher set is not final until the server's matchers arrive.
		p.pendingLogTypes = &saved
	case len(saved.LogTypes) > 0:
		p.logger.Info("matcher config changed, discarding persisted log types")
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
	toSave := logTypeMap{}
	for _, logFingerprint := range p.logTypes.Keys() {
		logType, ok := p.logTypes.Peek(logFingerprint)
		if !ok {
			continue
		}
		toSave[strconv.FormatUint(logFingerprint, 16)] = logType
	}

	p.matcherMux.RLock()
	hash := p.matcherHash
	p.matcherMux.RUnlock()

	state := persistedFingerprints{MatcherHash: hash, LogTypes: toSave}
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
	p.stopOpAMP()

	if p.fingerprintPersistCancel == nil {
		return errors.Join(p.closeMatcherStorage(ctx), p.fingerprintStorageClient.Close(ctx))
	}

	p.fingerprintPersistCancel()
	<-p.fingerprintPersistDone

	return errors.Join(p.save(ctx), p.closeMatcherStorage(ctx), p.fingerprintStorageClient.Close(ctx))
}

func (p *logTypeDetectionProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	p.matcherMux.RLock()
	defer p.matcherMux.RUnlock()

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
