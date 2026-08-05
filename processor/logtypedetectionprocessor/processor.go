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
	"strconv"
	"sync"

	"github.com/observiq/bindplane-otel-contrib/processor/logtypedetectionprocessor/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
	"golang.org/x/sync/singleflight"
)

type logTypeDetectionProcessor struct {
	cfg      *Config
	logTypes sync.Map
	sf       singleflight.Group

	telemetry *metadata.TelemetryBuilder
}

func newLogTypeDetectionProcessor(cfg *Config, telemetry *metadata.TelemetryBuilder) *logTypeDetectionProcessor {
	return &logTypeDetectionProcessor{
		cfg:       cfg,
		telemetry: telemetry,
	}
}

func (p *logTypeDetectionProcessor) start(_ context.Context, _ component.Host) error {
	return nil
}

func (p *logTypeDetectionProcessor) stop(_ context.Context) error {
	p.telemetry.Shutdown()
	return nil
}

func (p *logTypeDetectionProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		resourceLogs := ld.ResourceLogs().At(i)
		for j := 0; j < resourceLogs.ScopeLogs().Len(); j++ {
			scopeLogs := resourceLogs.ScopeLogs().At(j)
			for k := 0; k < scopeLogs.LogRecords().Len(); k++ {
				logRecord := scopeLogs.LogRecords().At(k)
				body := logRecord.Body().AsString()
				fingerprint := fingerprintLog(body)
				if fingerprint == 0 {
					continue
				}
				logType, ok := p.logTypes.Load(fingerprint)
				if !ok {
					newLogType, err, _ := p.sf.Do(
						strconv.FormatUint(fingerprint, 10),
						func() (any, error) {
							logType := p.logType(ctx, body)
							p.logTypes.Store(fingerprint, logType)
							return logType, nil
						},
					)
					if err != nil {
						return ld, err
					}
					logType = newLogType.(string)
				}
				logRecord.Attributes().PutStr("fingerprint", strconv.FormatUint(fingerprint, 16))
				logRecord.Attributes().PutStr("logType", logType.(string))
			}
		}
	}
	return ld, nil
}

func (p *logTypeDetectionProcessor) logType(ctx context.Context, _ string) string {
	p.telemetry.LogTypeDetectionRuns.Add(ctx, 1)
	return ""
}
