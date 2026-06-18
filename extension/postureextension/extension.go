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

package postureextension

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/internal/posture"
)

// postureExtension exposes a posture.Provider to other components (the posture
// processor) by reference. The embedded Provider supplies the read-only
// Current/Subscribe/RecordExportResult methods; the extension owns the source
// lifecycle via the controller.
type postureExtension struct {
	posture.Provider
	ctrl   posture.Controller
	logger *zap.Logger
}

var (
	_ extension.Extension = (*postureExtension)(nil)
	_ posture.Provider    = (*postureExtension)(nil)
)

// Start begins watching the posture sources.
func (e *postureExtension) Start(ctx context.Context, _ component.Host) error {
	e.logger.Info("starting posture extension")
	return e.ctrl.Start(ctx)
}

// Shutdown stops the posture sources.
func (e *postureExtension) Shutdown(context.Context) error {
	e.ctrl.Shutdown()
	return nil
}
