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

package snapshot

import (
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// The copy*Tail helpers copy everything after the first skip items (in
// traversal order) from src to dst, so callers can retain only the newest
// items of a payload without deep-copying the whole batch. Groups that are
// copied in full take the pdata whole-group CopyTo fast path, which preserves
// full fidelity; partially copied groups copy their resource, scope, and
// schema URL explicitly.

// copyLogsTail copies all log records after the first skip records from src
// to dst. src is only read, never retained or mutated.
func copyLogsTail(src, dst plog.Logs, skip int) {
	rls := src.ResourceLogs()
	for ri := 0; ri < rls.Len(); ri++ {
		srcRL := rls.At(ri)

		// Nothing left to skip: copy the whole group.
		if skip == 0 {
			srcRL.CopyTo(dst.ResourceLogs().AppendEmpty())
			continue
		}

		// Group is entirely within the skipped range.
		if count := resourceLogsRecordCount(srcRL); skip >= count {
			skip -= count
			continue
		}

		// Group is partially copied.
		dstRL := dst.ResourceLogs().AppendEmpty()
		srcRL.Resource().CopyTo(dstRL.Resource())
		dstRL.SetSchemaUrl(srcRL.SchemaUrl())

		sls := srcRL.ScopeLogs()
		for si := 0; si < sls.Len(); si++ {
			srcSL := sls.At(si)

			if skip == 0 {
				srcSL.CopyTo(dstRL.ScopeLogs().AppendEmpty())
				continue
			}

			lrs := srcSL.LogRecords()
			if skip >= lrs.Len() {
				skip -= lrs.Len()
				continue
			}

			dstSL := dstRL.ScopeLogs().AppendEmpty()
			srcSL.Scope().CopyTo(dstSL.Scope())
			dstSL.SetSchemaUrl(srcSL.SchemaUrl())

			dstLRs := dstSL.LogRecords()
			dstLRs.EnsureCapacity(lrs.Len() - skip)
			for li := skip; li < lrs.Len(); li++ {
				lrs.At(li).CopyTo(dstLRs.AppendEmpty())
			}
			skip = 0
		}
	}
}

// resourceLogsRecordCount returns the number of log records in the resource logs
func resourceLogsRecordCount(rl plog.ResourceLogs) int {
	count := 0
	sls := rl.ScopeLogs()
	for si := 0; si < sls.Len(); si++ {
		count += sls.At(si).LogRecords().Len()
	}
	return count
}

// copyMetricsTail copies all data points after the first skip data points
// from src to dst. src is only read, never retained or mutated.
func copyMetricsTail(src, dst pmetric.Metrics, skip int) {
	rms := src.ResourceMetrics()
	for ri := 0; ri < rms.Len(); ri++ {
		srcRM := rms.At(ri)

		// Nothing left to skip: copy the whole group.
		if skip == 0 {
			srcRM.CopyTo(dst.ResourceMetrics().AppendEmpty())
			continue
		}

		// Group is entirely within the skipped range.
		if count := resourceMetricsDataPointCount(srcRM); skip >= count {
			skip -= count
			continue
		}

		// Group is partially copied.
		dstRM := dst.ResourceMetrics().AppendEmpty()
		srcRM.Resource().CopyTo(dstRM.Resource())
		dstRM.SetSchemaUrl(srcRM.SchemaUrl())

		sms := srcRM.ScopeMetrics()
		for si := 0; si < sms.Len(); si++ {
			srcSM := sms.At(si)

			if skip == 0 {
				srcSM.CopyTo(dstRM.ScopeMetrics().AppendEmpty())
				continue
			}

			if count := scopeMetricsDataPointCount(srcSM); skip >= count {
				skip -= count
				continue
			}

			dstSM := dstRM.ScopeMetrics().AppendEmpty()
			srcSM.Scope().CopyTo(dstSM.Scope())
			dstSM.SetSchemaUrl(srcSM.SchemaUrl())

			ms := srcSM.Metrics()
			for mi := 0; mi < ms.Len(); mi++ {
				srcMetric := ms.At(mi)

				if skip == 0 {
					srcMetric.CopyTo(dstSM.Metrics().AppendEmpty())
					continue
				}

				dpCount := getDataPointCount(srcMetric)
				if skip >= dpCount {
					skip -= dpCount
					continue
				}

				dstMetric := dstSM.Metrics().AppendEmpty()
				dstMetric.SetName(srcMetric.Name())
				dstMetric.SetDescription(srcMetric.Description())
				dstMetric.SetUnit(srcMetric.Unit())
				srcMetric.Metadata().CopyTo(dstMetric.Metadata())
				initMetricDataPoints(srcMetric, dstMetric)
				for di := skip; di < dpCount; di++ {
					copyDataPoint(srcMetric, dstMetric, di)
				}
				skip = 0
			}
		}
	}
}

// resourceMetricsDataPointCount returns the number of data points in the resource metrics
func resourceMetricsDataPointCount(rm pmetric.ResourceMetrics) int {
	count := 0
	sms := rm.ScopeMetrics()
	for si := 0; si < sms.Len(); si++ {
		count += scopeMetricsDataPointCount(sms.At(si))
	}
	return count
}

// scopeMetricsDataPointCount returns the number of data points in the scope metrics
func scopeMetricsDataPointCount(sm pmetric.ScopeMetrics) int {
	count := 0
	ms := sm.Metrics()
	for mi := 0; mi < ms.Len(); mi++ {
		count += getDataPointCount(ms.At(mi))
	}
	return count
}

// copyTracesTail copies all spans after the first skip spans from src to
// dst. src is only read, never retained or mutated.
func copyTracesTail(src, dst ptrace.Traces, skip int) {
	rss := src.ResourceSpans()
	for ri := 0; ri < rss.Len(); ri++ {
		srcRS := rss.At(ri)

		// Nothing left to skip: copy the whole group.
		if skip == 0 {
			srcRS.CopyTo(dst.ResourceSpans().AppendEmpty())
			continue
		}

		// Group is entirely within the skipped range.
		if count := resourceSpansSpanCount(srcRS); skip >= count {
			skip -= count
			continue
		}

		// Group is partially copied.
		dstRS := dst.ResourceSpans().AppendEmpty()
		srcRS.Resource().CopyTo(dstRS.Resource())
		dstRS.SetSchemaUrl(srcRS.SchemaUrl())

		sss := srcRS.ScopeSpans()
		for si := 0; si < sss.Len(); si++ {
			srcSS := sss.At(si)

			if skip == 0 {
				srcSS.CopyTo(dstRS.ScopeSpans().AppendEmpty())
				continue
			}

			sps := srcSS.Spans()
			if skip >= sps.Len() {
				skip -= sps.Len()
				continue
			}

			dstSS := dstRS.ScopeSpans().AppendEmpty()
			srcSS.Scope().CopyTo(dstSS.Scope())
			dstSS.SetSchemaUrl(srcSS.SchemaUrl())

			dstSpans := dstSS.Spans()
			dstSpans.EnsureCapacity(sps.Len() - skip)
			for spi := skip; spi < sps.Len(); spi++ {
				sps.At(spi).CopyTo(dstSpans.AppendEmpty())
			}
			skip = 0
		}
	}
}

// resourceSpansSpanCount returns the number of spans in the resource spans
func resourceSpansSpanCount(rs ptrace.ResourceSpans) int {
	count := 0
	sss := rs.ScopeSpans()
	for si := 0; si < sss.Len(); si++ {
		count += sss.At(si).Spans().Len()
	}
	return count
}
