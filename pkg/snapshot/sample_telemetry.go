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
	"math/rand/v2"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// selectSampledIndices returns a boolean membership set of size n with target
// randomly chosen entries set to true. Items are identified by their flat
// position in traversal order, so membership checks during the rebuild are a
// slice index instead of a struct-keyed map lookup. Selection is a partial
// Fisher-Yates shuffle costing O(target) swaps instead of shuffling the whole
// index space.
func selectSampledIndices(n, target int) []bool {
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = int32(i)
	}

	keep := make([]bool, n)
	for i := 0; i < target; i++ {
		j := i + rand.IntN(n-i)
		idx[i], idx[j] = idx[j], idx[i]
		keep[idx[i]] = true
	}

	return keep
}

// randomSampleLogs samples a given percentage of log records from the given logs.
// n must be the total number of log records in originalLogs.
// Returns a new logs object with only the sampled log records.
func randomSampleLogs(originalLogs plog.Logs, n int, retentionPercent int) plog.Logs {
	// If retention is 100%, return the original logs
	// If retention is 0%, set to 1%
	switch retentionPercent {
	case 100:
		return originalLogs
	case 0:
		retentionPercent = 1
	}

	// Calculate the number of log records to keep based on the retention percentage
	targetCount := (n * retentionPercent) / 100
	keep := selectSampledIndices(n, targetCount)

	// Rebuild the logs with only the kept log records without modifying the original logs
	result := plog.NewLogs()
	flat := 0
	rls := originalLogs.ResourceLogs()
	for ri := 0; ri < rls.Len(); ri++ {
		srcRL := rls.At(ri)
		var dstRL plog.ResourceLogs
		rlCreated := false

		sls := srcRL.ScopeLogs()
		for si := 0; si < sls.Len(); si++ {
			srcSL := sls.At(si)
			var dstSL plog.ScopeLogs
			slCreated := false

			lrs := srcSL.LogRecords()
			for li := 0; li < lrs.Len(); li++ {
				if keep[flat] {
					if !rlCreated {
						dstRL = result.ResourceLogs().AppendEmpty()
						srcRL.Resource().CopyTo(dstRL.Resource())
						rlCreated = true
					}
					if !slCreated {
						dstSL = dstRL.ScopeLogs().AppendEmpty()
						srcSL.Scope().CopyTo(dstSL.Scope())
						slCreated = true
					}
					lrs.At(li).CopyTo(dstSL.LogRecords().AppendEmpty())
				}
				flat++
			}
		}
	}

	return result
}

// getDataPointCount returns the number of data points in a metric based on its type
func getDataPointCount(metric pmetric.Metric) int {
	switch metric.Type() {
	case pmetric.MetricTypeGauge:
		return metric.Gauge().DataPoints().Len()
	case pmetric.MetricTypeSum:
		return metric.Sum().DataPoints().Len()
	case pmetric.MetricTypeHistogram:
		return metric.Histogram().DataPoints().Len()
	case pmetric.MetricTypeExponentialHistogram:
		return metric.ExponentialHistogram().DataPoints().Len()
	case pmetric.MetricTypeSummary:
		return metric.Summary().DataPoints().Len()
	default:
		return 0
	}
}

// randomSampleMetrics samples a given percentage of data points from the given metrics.
// n must be the total number of data points in originalMetrics.
// Returns a new metrics object with only the sampled data points.
func randomSampleMetrics(originalMetrics pmetric.Metrics, n int, retentionPercent int) pmetric.Metrics {
	// If retention is 100%, return the original metrics
	// If retention is 0%, set to 1%
	switch retentionPercent {
	case 100:
		return originalMetrics
	case 0:
		retentionPercent = 1
	}

	// Calculate the number of data points to keep based on the retention percentage
	targetCount := (n * retentionPercent) / 100
	keep := selectSampledIndices(n, targetCount)

	// Rebuild the metrics with only the kept data points without modifying the original metrics
	result := pmetric.NewMetrics()
	flat := 0
	rms := originalMetrics.ResourceMetrics()
	for ri := 0; ri < rms.Len(); ri++ {
		srcRM := rms.At(ri)
		var dstRM pmetric.ResourceMetrics
		rmCreated := false

		sms := srcRM.ScopeMetrics()
		for si := 0; si < sms.Len(); si++ {
			srcSM := sms.At(si)
			var dstSM pmetric.ScopeMetrics
			smCreated := false

			ms := srcSM.Metrics()
			for mi := 0; mi < ms.Len(); mi++ {
				srcMetric := ms.At(mi)
				var dstMetric pmetric.Metric
				metricCreated := false

				dpCount := getDataPointCount(srcMetric)
				for di := 0; di < dpCount; di++ {
					if keep[flat] {
						if !rmCreated {
							dstRM = result.ResourceMetrics().AppendEmpty()
							srcRM.Resource().CopyTo(dstRM.Resource())
							rmCreated = true
						}
						if !smCreated {
							dstSM = dstRM.ScopeMetrics().AppendEmpty()
							srcSM.Scope().CopyTo(dstSM.Scope())
							smCreated = true
						}
						if !metricCreated {
							dstMetric = dstSM.Metrics().AppendEmpty()
							dstMetric.SetName(srcMetric.Name())
							dstMetric.SetDescription(srcMetric.Description())
							dstMetric.SetUnit(srcMetric.Unit())
							initMetricDataPoints(srcMetric, dstMetric)
							metricCreated = true
						}
						copyDataPoint(srcMetric, dstMetric, di)
					}
					flat++
				}
			}
		}
	}

	return result
}

// initMetricDataPoints initializes the destination metric with the same type as the source
func initMetricDataPoints(src, dst pmetric.Metric) {
	switch src.Type() {
	case pmetric.MetricTypeGauge:
		dst.SetEmptyGauge()
	case pmetric.MetricTypeSum:
		dst.SetEmptySum()
		dst.Sum().SetIsMonotonic(src.Sum().IsMonotonic())
		dst.Sum().SetAggregationTemporality(src.Sum().AggregationTemporality())
	case pmetric.MetricTypeHistogram:
		dst.SetEmptyHistogram()
		dst.Histogram().SetAggregationTemporality(src.Histogram().AggregationTemporality())
	case pmetric.MetricTypeExponentialHistogram:
		dst.SetEmptyExponentialHistogram()
		dst.ExponentialHistogram().SetAggregationTemporality(src.ExponentialHistogram().AggregationTemporality())
	case pmetric.MetricTypeSummary:
		dst.SetEmptySummary()
	}
}

// copyDataPoint copies a specific data point from src to dst metric
func copyDataPoint(src, dst pmetric.Metric, idx int) {
	switch src.Type() {
	case pmetric.MetricTypeGauge:
		src.Gauge().DataPoints().At(idx).CopyTo(dst.Gauge().DataPoints().AppendEmpty())
	case pmetric.MetricTypeSum:
		src.Sum().DataPoints().At(idx).CopyTo(dst.Sum().DataPoints().AppendEmpty())
	case pmetric.MetricTypeHistogram:
		src.Histogram().DataPoints().At(idx).CopyTo(dst.Histogram().DataPoints().AppendEmpty())
	case pmetric.MetricTypeExponentialHistogram:
		src.ExponentialHistogram().DataPoints().At(idx).CopyTo(dst.ExponentialHistogram().DataPoints().AppendEmpty())
	case pmetric.MetricTypeSummary:
		src.Summary().DataPoints().At(idx).CopyTo(dst.Summary().DataPoints().AppendEmpty())
	}
}

// randomSampleTraces samples a given percentage of spans from the given traces.
// n must be the total number of spans in originalTraces.
// Returns a new traces object with only the sampled spans.
func randomSampleTraces(originalTraces ptrace.Traces, n int, retentionPercent int) ptrace.Traces {
	// If retention is 100%, return the original traces
	// If retention is 0%, set to 1%
	switch retentionPercent {
	case 100:
		return originalTraces
	case 0:
		retentionPercent = 1
	}

	// Calculate the number of spans to keep based on the retention percentage
	targetCount := (n * retentionPercent) / 100
	keep := selectSampledIndices(n, targetCount)

	// Rebuild the traces with only the kept spans without modifying the original traces
	result := ptrace.NewTraces()
	flat := 0
	rss := originalTraces.ResourceSpans()
	for ri := 0; ri < rss.Len(); ri++ {
		srcRS := rss.At(ri)
		var dstRS ptrace.ResourceSpans
		rsCreated := false

		sss := srcRS.ScopeSpans()
		for si := 0; si < sss.Len(); si++ {
			srcSS := sss.At(si)
			var dstSS ptrace.ScopeSpans
			ssCreated := false

			sps := srcSS.Spans()
			for spi := 0; spi < sps.Len(); spi++ {
				if keep[flat] {
					if !rsCreated {
						dstRS = result.ResourceSpans().AppendEmpty()
						srcRS.Resource().CopyTo(dstRS.Resource())
						rsCreated = true
					}
					if !ssCreated {
						dstSS = dstRS.ScopeSpans().AppendEmpty()
						srcSS.Scope().CopyTo(dstSS.Scope())
						ssCreated = true
					}
					sps.At(spi).CopyTo(dstSS.Spans().AppendEmpty())
				}
				flat++
			}
		}
	}

	return result
}
