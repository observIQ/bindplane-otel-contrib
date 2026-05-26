// Copyright observIQ, Inc.
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

// Package blitzpdata adapts blitz embed record batches to OpenTelemetry
// pdata. The package is internal to the telemetrygeneratorreceiver
// and exists so the receiver can satisfy blitz's embed.LogConsumer
// interface without dragging pdata into blitz itself — per the embed
// contract, format conversion lives on the consuming host.
package blitzpdata // import "github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/internal/blitzpdata"

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/observiq/blitz/embed"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// scopeName is set on every plog.ScopeLogs emitted by the adapter so
// downstream pipelines can attribute the records to the blitz source.
const scopeName = "github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/blitz"

// LogAdapter implements embed.LogConsumer. Each ConsumeLogs call builds
// a fresh plog.Logs from the batch and pushes it to the receiver's
// downstream consumer.
//
// The adapter is constructed once per receiver Start cycle. Resource
// and per-record attribute maps are drawn from receiver config at
// construction time and remain stable for the session; the v0.16.1
// embed.LogRecord contract has no per-record attributes or resource
// of its own (PIPE-1021 will lift this), so receiver config is the
// sole source today.
type LogAdapter struct {
	consumer      consumer.Logs
	resourceAttrs map[string]any
	attrs         map[string]any
	parseBody     bool
	logger        *zap.Logger
}

// NewLogAdapter constructs an adapter that emits to the given consumer.
//
// resourceAttrs is written onto every outgoing plog.ResourceLogs.
// attrs is written onto every outgoing log record. Either map may be
// nil; nil means "no attributes of that kind on the output."
//
// parseBody controls whether the adapter calls LogRecord.ParseFunc
// (when set) to build a structured map body. The default for the
// receiver is false — emit the raw Message string as the body and let
// downstream processors parse if needed. parseBody=true opts into the
// structured-map path with raw-string fallback on parser error or
// empty result.
//
// logger is used for in-band warnings (failed attribute conversion,
// ParseFunc errors). A nil logger yields a no-op logger.
func NewLogAdapter(c consumer.Logs, resourceAttrs, attrs map[string]any, parseBody bool, logger *zap.Logger) *LogAdapter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LogAdapter{
		consumer:      c,
		resourceAttrs: resourceAttrs,
		attrs:         attrs,
		parseBody:     parseBody,
		logger:        logger,
	}
}

// ConsumeLogs satisfies embed.LogConsumer. Each record in the batch
// becomes one plog log record under a single shared resource + scope.
//
// An empty batch is a no-op (returns nil without invoking the
// downstream consumer); blitz framework allows producers to coalesce,
// and the receiver should not push empty payloads downstream.
func (a *LogAdapter) ConsumeLogs(ctx context.Context, records []embed.LogRecord) error {
	if len(records) == 0 {
		return nil
	}
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	if err := rl.Resource().Attributes().FromRaw(a.resourceAttrs); err != nil {
		a.logger.Warn("blitzpdata: failed to set resource attributes", zap.Error(err))
	}
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName(scopeName)
	observedNow := pcommon.NewTimestampFromTime(time.Now())
	for i := range records {
		a.appendRecord(sl.LogRecords().AppendEmpty(), &records[i], observedNow)
	}
	return a.consumer.ConsumeLogs(ctx, logs)
}

// appendRecord populates a fresh plog.LogRecord from a single
// embed.LogRecord. observedNow is the ObservedTimestamp shared across
// the batch — captured once per ConsumeLogs call so every record in a
// batch sees the same observation timestamp.
func (a *LogAdapter) appendRecord(lr plog.LogRecord, rec *embed.LogRecord, observedNow pcommon.Timestamp) {
	ts := rec.Metadata.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	lr.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	lr.SetObservedTimestamp(observedNow)

	if rec.Metadata.Severity != "" {
		lr.SetSeverityText(rec.Metadata.Severity)
		lr.SetSeverityNumber(severityNumberFor(rec.Metadata.Severity))
	}

	a.setBody(lr, rec)

	if err := lr.Attributes().FromRaw(a.attrs); err != nil {
		a.logger.Warn("blitzpdata: failed to set record attributes", zap.Error(err))
	}
}

// setBody chooses raw-string or parsed-map representation for the log
// record body.
//
// Default (parseBody=false): emit the raw Message. This matches what a
// real log-collection pipeline sees on the wire and lets downstream
// processors decide whether and how to parse.
//
// Opt-in (parseBody=true): if ParseFunc is set and succeeds with a
// non-empty map, the body becomes a structured pcommon.Map. The
// ParseFunc is treated as untrusted closure code — recipes ship
// arbitrary parsers and the embed contract does not require them to
// be panic-free — so the call is wrapped in a recover() and any
// panic is treated the same as a parser error. On parser error
// (returned or recovered), empty result, or map-conversion failure,
// fall back to the raw Message and log at debug. ParseFunc=nil with
// parseBody=true also falls back to raw (no work for the adapter to do).
func (a *LogAdapter) setBody(lr plog.LogRecord, rec *embed.LogRecord) {
	if !a.parseBody || rec.ParseFunc == nil {
		lr.Body().SetStr(rec.Message)
		return
	}
	parsed, err := safeParseFunc(rec.ParseFunc, rec.Message)
	if err != nil {
		a.logger.Debug("blitzpdata: ParseFunc returned error; using raw message", zap.Error(err))
		lr.Body().SetStr(rec.Message)
		return
	}
	if len(parsed) == 0 {
		lr.Body().SetStr(rec.Message)
		return
	}
	if err := lr.Body().SetEmptyMap().FromRaw(parsed); err != nil {
		a.logger.Debug("blitzpdata: parsed map not representable as pcommon.Map; using raw message", zap.Error(err))
		lr.Body().SetStr(rec.Message)
	}
}

// safeParseFunc invokes a blitz-supplied ParseFunc with panic
// protection. ParseFuncs are closures shipped by recipes; the embed
// contract does not require them to be panic-free, so any panic is
// caught and converted to an error — the caller will then fall back
// to the raw Message exactly as it would on any other parser error.
func safeParseFunc(parse func(string) (map[string]any, error), msg string) (parsed map[string]any, err error) {
	defer func() {
		if r := recover(); r != nil {
			parsed = nil
			err = fmt.Errorf("ParseFunc panicked: %v", r)
		}
	}()
	return parse(msg)
}

// severityNumberFor maps a free-form severity string to an OTel
// SeverityNumber. The mapping is best-effort and case-insensitive,
// covering OTel's native short names (TRACE / DEBUG / INFO / WARN /
// ERROR / FATAL) plus common syslog-style synonyms (NOTICE, CRITICAL,
// ALERT, EMERGENCY) per OTel logs semantic conventions. Unknown text
// returns SeverityNumberUnspecified — the caller will still have
// SeverityText set so downstream processors can recover the original
// label.
func severityNumberFor(text string) plog.SeverityNumber {
	switch strings.ToUpper(strings.TrimSpace(text)) {
	case "TRACE":
		return plog.SeverityNumberTrace
	case "DEBUG", "DBG":
		return plog.SeverityNumberDebug
	case "INFO", "INFORMATION", "INFORMATIONAL":
		return plog.SeverityNumberInfo
	case "NOTICE":
		return plog.SeverityNumberInfo2
	case "WARN", "WARNING":
		return plog.SeverityNumberWarn
	case "ERROR", "ERR":
		return plog.SeverityNumberError
	case "CRITICAL", "CRIT":
		return plog.SeverityNumberFatal2
	case "ALERT":
		return plog.SeverityNumberFatal3
	case "FATAL":
		return plog.SeverityNumberFatal
	case "EMERGENCY", "EMERG", "PANIC":
		return plog.SeverityNumberFatal4
	default:
		return plog.SeverityNumberUnspecified
	}
}
