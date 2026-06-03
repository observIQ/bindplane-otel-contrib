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
// The adapter holds per-receiver-entry LockableAttrs maps for the resource
// and per-record attributes. At ConsumeLogs time, blitz's per-record
// `Metadata.Resource` and `Metadata.Attributes` (PIPE-1021, available
// in blitz v0.18.0+) merge over the configured base maps with locked keys
// preserved from the receiver-config side. Records whose merged
// resource maps fingerprint differently are emitted under separate
// `ResourceLogs` (resource grouping); records that share a
// fingerprint share a single `ResourceLogs`.
type LogAdapter struct {
	consumer  consumer.Logs
	resource  LockableAttrs
	attrs     LockableAttrs
	parseBody bool
	logger    *zap.Logger
}

// NewLogAdapter constructs an adapter that emits to the given consumer.
//
// resource and attrs are parsed per-key-lockable config maps (see LockableAttrs). Either
// may be a zero-value LockableAttrs; an empty config contributes no base
// values and locks no keys — blitz's per-record metadata flows through
// unmodified.
//
// parseBody controls whether the adapter calls LogRecord.ParseFunc
// (when set) to build a structured map body. The default for the
// receiver is false — emit the raw Message string as the body and let
// downstream processors parse if needed. parseBody=true opts into the
// structured-map path with raw-string fallback on parser error, panic,
// or empty result.
//
// logger is used for in-band warnings (failed attribute conversion,
// ParseFunc errors). A nil logger yields a no-op logger.
func NewLogAdapter(c consumer.Logs, resource, attrs LockableAttrs, parseBody bool, logger *zap.Logger) *LogAdapter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LogAdapter{
		consumer:  c,
		resource:  resource,
		attrs:     attrs,
		parseBody: parseBody,
		logger:    logger,
	}
}

// ConsumeLogs satisfies embed.LogConsumer.
//
// For each record in the batch:
//  1. Compute the effective resource by merging blitz's per-record
//     `Metadata.Resource` over the adapter's `resource` base
//     (locked keys from the base are not overridden).
//  2. Group records by the fingerprint of their effective resource —
//     records sharing a fingerprint go under a single `ResourceLogs`
//     with one `ScopeLogs`; records with distinct fingerprints get
//     their own `ResourceLogs`.
//  3. For each record, merge blitz's per-record `Metadata.Attributes`
//     over the adapter's `attrs` base (same locking semantics) and
//     write the result onto the outgoing `LogRecord.Attributes`.
//
// Empty batches are no-ops (return nil without invoking the downstream
// consumer) — blitz allows producers to coalesce and the receiver
// should not push empty payloads downstream.
func (a *LogAdapter) ConsumeLogs(ctx context.Context, records []embed.LogRecord) error {
	if len(records) == 0 {
		return nil
	}

	// Group records by their merged-resource fingerprint. Each group
	// becomes one ResourceLogs in the outgoing plog.Logs. The order
	// slice preserves first-occurrence ordering so the output is
	// deterministic for a given input batch — useful for downstream
	// pipelines that batch on consume order and for test assertions.
	type group struct {
		merged  map[string]any
		records []int
	}
	groups := make(map[string]*group)
	order := make([]string, 0)
	for i := range records {
		merged := a.resource.MergeWithStringOverlay(records[i].Metadata.Resource)
		fp := FingerprintMap(merged)
		g, exists := groups[fp]
		if !exists {
			g = &group{merged: merged}
			groups[fp] = g
			order = append(order, fp)
		}
		g.records = append(g.records, i)
	}

	logs := plog.NewLogs()
	observedNow := pcommon.NewTimestampFromTime(time.Now())
	for _, fp := range order {
		g := groups[fp]
		rl := logs.ResourceLogs().AppendEmpty()
		if err := rl.Resource().Attributes().FromRaw(g.merged); err != nil {
			a.logger.Warn("blitzpdata: failed to set resource attributes", zap.Error(err))
		}
		sl := rl.ScopeLogs().AppendEmpty()
		sl.Scope().SetName(scopeName)
		for _, idx := range g.records {
			a.appendRecord(sl.LogRecords().AppendEmpty(), &records[idx], observedNow)
		}
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

	mergedAttrs := a.attrs.MergeWithAnyOverlay(rec.Metadata.Attributes)
	if err := lr.Attributes().FromRaw(mergedAttrs); err != nil {
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
