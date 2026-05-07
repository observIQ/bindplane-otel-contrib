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

package bundle

import (
	"context"

	"go.uber.org/zap"
)

type defaultManager struct {
	logger  *zap.Logger
	bundler Bundler
}

// NewDefaultManager returns a Manager that drives the given Bundler.
func NewDefaultManager(logger *zap.Logger, bundler Bundler) Manager {
	return &defaultManager{logger: logger, bundler: bundler}
}

// HandleRequest processes a server-initiated bundle request.
// Messages whose capability or type do not match are silently ignored.
func (m *defaultManager) HandleRequest(ctx context.Context, capability string, msgType string, data []byte, uploadURL string) {
	if capability != Capability || msgType != RequestType {
		return
	}

	payload, err := ParseRequestPayload(data)
	if err != nil || payload.SessionID == "" {
		m.logger.Error("support bundle request has missing or invalid session_id", zap.Error(err))
		return
	}
	if uploadURL == "" {
		m.logger.Error("support bundle upload URL is empty; cannot upload")
		return
	}

	m.logger.Info("support bundle requested by server, collecting...")
	bundleData, err := m.bundler.Collect(ctx)
	if err != nil {
		m.logger.Error("failed to collect support bundle", zap.Error(err))
		if sendErr := m.bundler.SendErrorToURL(ctx, payload.SessionID, err.Error(), uploadURL); sendErr != nil {
			m.logger.Debug("could not send bundle failure notification to server", zap.Error(sendErr))
		}
		return
	}

	if err := m.bundler.SendToURL(ctx, bundleData, payload.SessionID, uploadURL); err != nil {
		m.logger.Error("failed to upload support bundle", zap.Error(err))
		if sendErr := m.bundler.SendErrorToURL(ctx, payload.SessionID, err.Error(), uploadURL); sendErr != nil {
			m.logger.Debug("could not send bundle failure notification to server", zap.Error(sendErr))
		}
		return
	}

	m.logger.Info("support bundle uploaded successfully")
}
