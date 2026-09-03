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

package logtypedetectionprocessor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pipeline"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	logTypeDetectionCapability = "com.bindplane.logtypedetection"
	requestMatchersType        = "requestMatchers"
	updateMatchersType         = "updateMatchers"
	matchersUpToDateType       = "matchersUpToDate"

	matcherStorageKey         = "matchers"
	opampRequestRetryInterval = 5 * time.Second
)

// matchersMessage is the payload of every message in the capability. A request
// carries the version the processor already has, and the server answers with
// either a newer set or matchersUpToDate.
type matchersMessage struct {
	Processor component.ID    `yaml:"processor"`
	Version   string          `yaml:"version"`
	Matchers  []MatcherConfig `yaml:"matchers"`
}

// persistedMatchers is the stored form of the matchers received over opamp.
type persistedMatchers struct {
	Version  string          `json:"version"`
	Matchers []MatcherConfig `json:"matchers"`
}

func (m *persistedMatchers) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

func (m *persistedMatchers) Unmarshal(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, m)
}

// startOpAMP applies any locally stored matchers, then registers the custom
// capability and asks the server for a newer version. With a stored copy in
// hand the check runs in the background; without one it holds up startup so no
// logs are processed before matchers arrive.
func (p *logTypeDetectionProcessor) startOpAMP(ctx context.Context, host component.Host) error {
	if err := p.startMatcherStorage(ctx, host); err != nil {
		return err
	}

	ext, ok := host.GetExtensions()[*p.cfg.OpAMP]
	if !ok {
		return fmt.Errorf("opamp extension %q does not exist", p.cfg.OpAMP)
	}

	registry, ok := ext.(opampcustommessages.CustomCapabilityRegistry)
	if !ok {
		return fmt.Errorf("extension %q is not a custom message registry", p.cfg.OpAMP)
	}

	handler, err := registry.Register(logTypeDetectionCapability)
	if err != nil {
		return fmt.Errorf("register custom capability: %w", err)
	}
	p.opampHandler = handler

	p.opampWg.Add(1)
	go p.processOpAMPMessages()

	if p.currentVersion() == "" {
		return p.awaitMatchers(ctx)
	}

	p.opampWg.Add(1)
	go func() {
		defer p.opampWg.Done()
		if err := p.awaitMatchers(context.Background()); err != nil {
			p.logger.Error("Failed to check for new matchers.", zap.Error(err))
		}
	}()

	return nil
}

// startMatcherStorage opens the matcher storage and applies what it holds. The
// fingerprint client is reused when both point at the same extension, since a
// storage extension hands out one client per component and signal.
func (p *logTypeDetectionProcessor) startMatcherStorage(ctx context.Context, host component.Host) error {
	if p.cfg.MatcherStorageID == nil {
		return nil
	}

	if p.cfg.FingerprintStorageID != nil && *p.cfg.MatcherStorageID == *p.cfg.FingerprintStorageID {
		p.matcherStorageClient = p.fingerprintStorageClient
	} else {
		client, err := storageclient.NewStorageClient(
			ctx,
			host,
			component.KindProcessor,
			*p.cfg.MatcherStorageID,
			p.id,
			pipeline.SignalLogs,
		)
		if err != nil {
			return fmt.Errorf("create matcher storage client: %w", err)
		}
		p.matcherStorageClient = client
		p.matcherStorageOwned = true
	}

	saved := persistedMatchers{}
	if err := p.matcherStorageClient.LoadStorageData(ctx, matcherStorageKey, &saved); err != nil {
		return errors.Join(fmt.Errorf("load matchers: %w", err), p.closeMatcherStorage(ctx))
	}

	if saved.Version == "" {
		return nil
	}

	if _, err := p.applyMatchers(saved.Version, saved.Matchers); err != nil {
		p.logger.Warn("Discarding stored matchers.", zap.String("version", saved.Version), zap.Error(err))
		return nil
	}

	p.logger.Info("Loaded stored matchers.",
		zap.String("version", saved.Version), zap.Int("matchers", len(saved.Matchers)))
	return nil
}

func (p *logTypeDetectionProcessor) closeMatcherStorage(ctx context.Context) error {
	if !p.matcherStorageOwned {
		return nil
	}

	p.matcherStorageOwned = false
	return p.matcherStorageClient.Close(ctx)
}

func (p *logTypeDetectionProcessor) saveMatchers(ctx context.Context, version string, matchers []MatcherConfig) error {
	if p.matcherStorageClient == nil {
		return nil
	}

	state := persistedMatchers{Version: version, Matchers: matchers}
	if err := p.matcherStorageClient.SaveStorageData(ctx, matcherStorageKey, &state); err != nil {
		return fmt.Errorf("save matchers: %w", err)
	}

	return nil
}

func (p *logTypeDetectionProcessor) stopOpAMP() {
	if p.opampHandler == nil {
		return
	}

	close(p.opampDone)
	p.opampWg.Wait()
	p.opampHandler.Unregister()
}

// awaitMatchers asks the server for matchers newer than the version held and
// waits for its answer, re-asking until it arrives, the timeout elapses, or the
// collector shuts down. A zero timeout waits indefinitely. A timeout is not
// fatal: the processor carries on with the matchers it already has.
func (p *logTypeDetectionProcessor) awaitMatchers(ctx context.Context) error {
	request, err := yaml.Marshal(matchersMessage{Processor: p.id, Version: p.currentVersion()})
	if err != nil {
		return fmt.Errorf("encode matchers request: %w", err)
	}

	var timedOut <-chan time.Time
	if p.cfg.OpAMPRequestTimeout > 0 {
		timeout := time.NewTimer(p.cfg.OpAMPRequestTimeout)
		defer timeout.Stop()
		timedOut = timeout.C
	}

	retry := time.NewTicker(opampRequestRetryInterval)
	defer retry.Stop()

	for {
		p.sendOpAMPMessage(requestMatchersType, request)

		select {
		case <-p.matchersReady:
			return nil
		case <-retry.C:
			p.logger.Debug("Still waiting on the opamp server, asking again.")
		case <-timedOut:
			p.logger.Warn("Timed out waiting for matchers from the opamp server.",
				zap.Duration("timeout", p.cfg.OpAMPRequestTimeout))
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-p.opampDone:
			return nil
		}
	}
}

func (p *logTypeDetectionProcessor) sendOpAMPMessage(messageType string, payload []byte) {
	for {
		sending, err := p.opampHandler.SendMessage(messageType, payload)
		switch {
		case err == nil:
			return
		case errors.Is(err, types.ErrCustomMessagePending):
			select {
			case <-sending:
			case <-p.opampDone:
				return
			}
		default:
			p.logger.Error("Failed to send opamp message.",
				zap.String("messageType", messageType), zap.Error(err))
			return
		}
	}
}

func (p *logTypeDetectionProcessor) processOpAMPMessages() {
	defer p.opampWg.Done()

	for {
		select {
		case msg := <-p.opampHandler.Message():
			switch msg.Type {
			case updateMatchersType:
				p.handleUpdateMatchers(msg)
			case matchersUpToDateType:
				p.handleMatchersUpToDate(msg)
			default:
				p.logger.Warn("Received message of unknown type.", zap.String("messageType", msg.Type))
			}
		case <-p.opampDone:
			return
		}
	}
}

func (p *logTypeDetectionProcessor) handleUpdateMatchers(msg *protobufs.CustomMessage) {
	update, ok := p.decodeMatchersMessage(msg)
	if !ok {
		return
	}

	applied, err := p.applyMatchers(update.Version, update.Matchers)
	if err != nil {
		p.logger.Warn("Ignoring matchers from the opamp server.",
			zap.String("version", update.Version), zap.Error(err))
		p.matchersDone()
		return
	}

	if !applied {
		p.logger.Debug("Offered matchers are not newer than the ones in use.",
			zap.String("version", update.Version))
		p.matchersDone()
		return
	}

	if err := p.saveMatchers(context.Background(), update.Version, update.Matchers); err != nil {
		p.logger.Error("Failed to store matchers from the opamp server.", zap.Error(err))
	}

	p.matchersDone()
	p.logger.Info("Applied matchers from the opamp server.",
		zap.String("version", update.Version), zap.Int("matchers", len(update.Matchers)))
}

func (p *logTypeDetectionProcessor) handleMatchersUpToDate(msg *protobufs.CustomMessage) {
	if _, ok := p.decodeMatchersMessage(msg); !ok {
		return
	}

	p.logger.Debug("Matchers are up to date.", zap.String("version", p.currentVersion()))
	p.matchersDone()
}

func (p *logTypeDetectionProcessor) decodeMatchersMessage(msg *protobufs.CustomMessage) (matchersMessage, bool) {
	var decoded matchersMessage
	if err := yaml.Unmarshal(msg.Data, &decoded); err != nil {
		p.logger.Error("Got an invalid matchers message.", zap.Error(err))
		return matchersMessage{}, false
	}

	if decoded.Processor != p.id {
		return matchersMessage{}, false
	}

	return decoded, true
}

// matchersDone releases a startup that is waiting on the server.
func (p *logTypeDetectionProcessor) matchersDone() {
	p.matchersOnce.Do(func() { close(p.matchersReady) })
}

// applyMatchers validates a versioned matcher set and puts it in use,
// reporting whether it was taken up. Only a higher version of the same major is
// accepted; a major bump is a breaking change the running processor may not
// understand, so it is refused. Anything is accepted when no version is held.
func (p *logTypeDetectionProcessor) applyMatchers(version string, matchers []MatcherConfig) (bool, error) {
	offered, err := semver.NewVersion(version)
	if err != nil {
		return false, fmt.Errorf("parse version %q: %w", version, err)
	}

	if current := p.currentVersion(); current != "" {
		held, err := semver.NewVersion(current)
		if err != nil {
			return false, fmt.Errorf("parse held version %q: %w", current, err)
		}

		if offered.Major() != held.Major() {
			return false, fmt.Errorf("version %s is a breaking change from %s", version, current)
		}

		if !offered.GreaterThan(held) {
			return false, nil
		}
	}

	for _, m := range matchers {
		if err := m.Validate(); err != nil {
			return false, fmt.Errorf("invalid matcher %q: %w", m.Name, err)
		}
	}

	if err := p.setServerMatchers(version, matchers); err != nil {
		return false, err
	}

	return true, nil
}
