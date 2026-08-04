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

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
)

type logTypeDetectionProcessor struct {
	cfg *Config
}

func newLogTypeDetectionProcessor(cfg *Config) *logTypeDetectionProcessor {
	return &logTypeDetectionProcessor{cfg: cfg}
}

func (p *logTypeDetectionProcessor) start(_ context.Context, _ component.Host) error {
	return nil
}

func (p *logTypeDetectionProcessor) stop(_ context.Context) error {
	return nil
}

func (p *logTypeDetectionProcessor) processLogs(_ context.Context, ld plog.Logs) (plog.Logs, error) {
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
				logRecord.Attributes().PutStr("fingerprint", strconv.FormatUint(fingerprint, 16))
			}
		}
	}
	return ld, nil
}
