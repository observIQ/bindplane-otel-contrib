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

	"github.com/hashicorp/go-version"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/opampcustommessages"
	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	logTypeDetectionCapability = "logtypedetection.matchers"
	requestMatchersType        = "requestMatchers"
	updateMatchersType         = "updateMatchers"
	matchersUpToDateType       = "matchersUpToDate"

	matcherStorageKey         = "matchers"
	opampRequestRetryInterval = 5 * time.Second
)

// matchersMessage is the payload of every message in the capability.
type matchersMessage struct {
	Processor component.ID `yaml:"processor"`
	Version   string       `yaml:"version"`
	// MaxVersion is the highest set version the processor accepts, empty for the newest
	MaxVersion string          `yaml:"max_version,omitempty"`
	Matchers   []MatcherConfig `yaml:"matchers"`
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

// startOpAMP registers the capability and asks the server for newer matchers.
func (p *logTypeDetectionProcessor) startOpAMP(host component.Host) error {
	ext, ok := host.GetExtensions()[p.cfg.OpAMP.Extension]
	if !ok {
		return fmt.Errorf("opamp extension %q does not exist", p.cfg.OpAMP.Extension)
	}

	registry, ok := ext.(opampcustommessages.CustomCapabilityRegistry)
	if !ok {
		return fmt.Errorf("extension %q is not a custom message registry", p.cfg.OpAMP.Extension)
	}

	handler, err := registry.Register(logTypeDetectionCapability)
	if err != nil {
		return fmt.Errorf("register custom capability: %w", err)
	}
	p.opampHandler = handler

	p.opampWg.Go(p.processOpAMPMessages)
	p.opampWg.Go(p.awaitMatchers)

	return nil
}

// loadStoredMatchers applies the matchers held in storage, if any.
func (p *logTypeDetectionProcessor) loadStoredMatchers(ctx context.Context) error {
	saved := persistedMatchers{}
	if err := p.storageClient.LoadStorageData(ctx, matcherStorageKey, &saved); err != nil {
		return fmt.Errorf("load matchers: %w", err)
	}

	if saved.Version == "" {
		return nil
	}

	ver, err := version.NewVersion(saved.Version)
	if err == nil {
		_, err = p.useMatchers(ver, saved.Matchers)
	}
	if err != nil {
		p.logger.Warn("Discarding stored matchers.", zap.String("version", saved.Version), zap.Error(err))
		return nil
	}

	p.logger.Info("Loaded stored matchers.",
		zap.String("version", saved.Version), zap.Int("matchers", len(saved.Matchers)))
	return nil
}

func (p *logTypeDetectionProcessor) saveMatchers(ctx context.Context, version string, matchers []MatcherConfig) error {
	state := persistedMatchers{Version: version, Matchers: matchers}
	if err := p.storageClient.SaveStorageData(ctx, matcherStorageKey, &state); err != nil {
		return fmt.Errorf("save matchers: %w", err)
	}

	return nil
}

func (p *logTypeDetectionProcessor) stopOpAMP() {
	p.opampCancel()
	if p.opampHandler == nil {
		return
	}

	p.opampWg.Wait()
	p.opampHandler.Unregister()
	p.matchersDone()
}

// awaitMatchers re-asks the server for newer matchers until it answers, the timeout elapses, or shutdown.
func (p *logTypeDetectionProcessor) awaitMatchers() {
	request, err := yaml.Marshal(matchersMessage{Processor: p.id, Version: p.currentVersion(), MaxVersion: p.cfg.OpAMP.MatchersVersion})
	if err != nil {
		p.logger.Error("Failed to encode matchers request.", zap.Error(err))
		p.matchersDone()
		return
	}

	var timedOut <-chan time.Time
	if p.cfg.OpAMP.RequestTimeout > 0 {
		timeout := time.NewTimer(p.cfg.OpAMP.RequestTimeout)
		defer timeout.Stop()
		timedOut = timeout.C
	}

	retry := time.NewTicker(opampRequestRetryInterval)
	defer retry.Stop()

	for {
		if err := p.sendOpAMPMessage(requestMatchersType, request); err != nil {
			p.logger.Debug("Failed to request matchers, will retry.", zap.Error(err))
		}

		select {
		case <-p.matchersReady:
			return
		case <-retry.C:
			p.logger.Debug("Still waiting on the opamp server, asking again.")
		case <-timedOut:
			p.logger.Warn("Timed out waiting for matchers from the opamp server.",
				zap.Duration("timeout", p.cfg.OpAMP.RequestTimeout))
			p.matchersDone()
			return
		case <-p.opampCtx.Done():
			return
		}
	}
}

// sendOpAMPMessage queues a message, treating an already-queued one as sent.
func (p *logTypeDetectionProcessor) sendOpAMPMessage(messageType string, payload []byte) error {
	_, err := p.opampHandler.SendMessage(messageType, payload)
	if errors.Is(err, types.ErrCustomMessagePending) {
		return nil
	}
	return err
}

func (p *logTypeDetectionProcessor) processOpAMPMessages() {
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
		case <-p.opampCtx.Done():
			return
		}
	}
}

func (p *logTypeDetectionProcessor) handleUpdateMatchers(msg *protobufs.CustomMessage) {
	update, ok := p.decodeMatchersMessage(msg)
	if !ok {
		return
	}
	defer p.matchersDone()

	applied, err := p.applyMatchers(update.Version, update.Matchers)
	if err != nil {
		p.logger.Warn("Ignoring matchers from the opamp server.",
			zap.String("version", update.Version), zap.Error(err))
		return
	}

	if !applied {
		p.logger.Info("Offered matchers are not newer than the ones in use.",
			zap.String("offered", update.Version), zap.String("held", p.currentVersion()))
		return
	}

	if err := p.saveMatchers(context.Background(), update.Version, update.Matchers); err != nil {
		p.logger.Error("Failed to store matchers from the opamp server.", zap.Error(err))
	}

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
		p.logger.Debug("Ignoring matchers message for another processor.", zap.Stringer("processor", decoded.Processor))
		return matchersMessage{}, false
	}

	return decoded, true
}

// matchersDone ends the wait on the server.
func (p *logTypeDetectionProcessor) matchersDone() {
	p.matchersOnce.Do(func() { close(p.matchersReady) })
}

// applyMatchers puts an offered version at or below matchers_version in use, reporting whether it did.
func (p *logTypeDetectionProcessor) applyMatchers(ver string, matchers []MatcherConfig) (bool, error) {
	offered, err := version.NewVersion(ver)
	if err != nil {
		return false, fmt.Errorf("parse version %q: %w", ver, err)
	}

	if p.maxVersion != nil && offered.GreaterThan(p.maxVersion) {
		return false, fmt.Errorf("version %s is above matchers_version %s", ver, p.cfg.OpAMP.MatchersVersion)
	}

	return p.useMatchers(offered, matchers)
}

// useMatchers puts a newer version of the same major in use, reporting whether it did.
func (p *logTypeDetectionProcessor) useMatchers(offered *version.Version, matchers []MatcherConfig) (bool, error) {
	if held := p.heldVersion(); held != nil {
		if offered.Segments()[0] != held.Segments()[0] {
			return false, fmt.Errorf("version %s is a breaking change from %s", offered.Original(), held.Original())
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

	if err := p.setServerMatchers(offered, matchers); err != nil {
		return false, err
	}

	return true, nil
}
