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

package blobconsume //import "github.com/observiq/bindplane-otel-contrib/internal/blobconsume"

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

// LineTextLogsConsumer consumes raw text content and creates one log record per
// non-empty line, with the line's text as the record body. It is the per-line
// counterpart to RawTextLogsConsumer (which emits the whole content as a single
// record) and mirrors NDJSON's line splitting without the JSON parsing.
type LineTextLogsConsumer struct {
	nextConsumer consumer.Logs
}

// NewLineTextLogsConsumer creates a new line-oriented text logs consumer.
func NewLineTextLogsConsumer(nextConsumer consumer.Logs) *LineTextLogsConsumer {
	return &LineTextLogsConsumer{
		nextConsumer: nextConsumer,
	}
}

// Consume splits entityContent on newlines and emits one log record per non-empty
// line. Empty or whitespace-only lines are skipped. Content with no non-empty line
// produces no records and is not forwarded.
func (r *LineTextLogsConsumer) Consume(ctx context.Context, entityContent []byte) error {
	logs := plog.NewLogs()
	scopeLogs := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	logRecords := scopeLogs.LogRecords()

	now := pcommon.NewTimestampFromTime(time.Now())
	for _, line := range bytes.Split(entityContent, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r")) // drop the CR of a CRLF line ending
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lr := logRecords.AppendEmpty()
		lr.SetObservedTimestamp(now)
		lr.Body().SetStr(string(line))
	}

	if logRecords.Len() == 0 {
		return nil
	}

	if err := r.nextConsumer.ConsumeLogs(ctx, logs); err != nil {
		return fmt.Errorf("line text consume: %w: %w", ErrDownstream, err)
	}
	return nil
}
