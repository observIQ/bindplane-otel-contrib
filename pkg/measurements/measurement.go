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

package measurements

import (
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Measurement holds the size and count of one payload.
// Take it before the payload is forwarded and record it after the forward
// returns. Downstream components can move data out of the payload, so a
// measurement taken after the forward can be empty.
type Measurement struct {
	// Size is the protobuf-encoded size of the payload in bytes.
	Size int64
	// Count is the number of log records, data points, or spans in the payload.
	Count int64
	// RawBytes is the size of the original log content in bytes.
	// It is meaningful only when HasRawBytes is true.
	RawBytes int64
	// HasRawBytes is true when raw bytes were measured.
	HasRawBytes bool
}

// MeasureLogs measures a logs payload. It records nothing.
func MeasureLogs(l plog.Logs, measureLogRawBytes bool) Measurement {
	sizer := plog.ProtoMarshaler{}
	m := Measurement{
		Size:  int64(sizer.LogsSize(l)),
		Count: int64(l.LogRecordCount()),
	}

	if measureLogRawBytes {
		m.HasRawBytes = true
		m.RawBytes = logRawBytes(l)
	}

	return m
}

// MeasureMetrics measures a metrics payload. It records nothing.
func MeasureMetrics(m pmetric.Metrics) Measurement {
	sizer := pmetric.ProtoMarshaler{}
	return Measurement{
		Size:  int64(sizer.MetricsSize(m)),
		Count: int64(m.DataPointCount()),
	}
}

// MeasureTraces measures a traces payload. It records nothing.
func MeasureTraces(t ptrace.Traces) Measurement {
	sizer := ptrace.ProtoMarshaler{}
	return Measurement{
		Size:  int64(sizer.TracesSize(t)),
		Count: int64(t.SpanCount()),
	}
}

// logRawBytes sums the original content size of every log record.
// It uses the log.record.original attribute when present and the body otherwise.
func logRawBytes(l plog.Logs) int64 {
	total := int64(0)
	resourceLogs := l.ResourceLogs()
	for i := 0; i < resourceLogs.Len(); i++ {
		scopeLogs := resourceLogs.At(i).ScopeLogs()
		for j := 0; j < scopeLogs.Len(); j++ {
			logRecords := scopeLogs.At(j).LogRecords()
			for k := 0; k < logRecords.Len(); k++ {
				logRecord := logRecords.At(k)
				if original, ok := logRecord.Attributes().Get("log.record.original"); ok {
					total += int64(len(original.Str()))
				} else {
					total += int64(len(logRecord.Body().AsString()))
				}
			}
		}
	}
	return total
}
