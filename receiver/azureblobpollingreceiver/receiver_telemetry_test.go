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

package azureblobpollingreceiver

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/observiq/bindplane-otel-contrib/internal/blobconsume"
	"github.com/observiq/bindplane-otel-contrib/receiver/azureblobpollingreceiver/internal/metadatatest"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"
	"go.uber.org/zap"
)

func TestPollingReceiver_recordsConsumedEmittedMetrics(t *testing.T) {
	// A json blob with one valid and one malformed line records consumed=2, emitted=1 on the
	// per-blob_format counters, so the consumed-minus-emitted gap surfaces the dropped record.
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })

	sink := new(consumertest.LogsSink)
	r := &pollingReceiver{
		logger:   zap.NewNop(),
		cfg:      &Config{Container: "c", BlobFormat: BlobFormatJSON, EnableIncrementalRead: true},
		consumer: blobconsume.NewNDJSONLogsConsumer(sink, zap.NewNop()),
		mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
	}
	require.NoError(t, r.initTelemetry(tt.NewTelemetrySettings()))

	_, err := r.consume(context.Background(), []byte("{\"a\":1}\nnot json\n"))
	require.NoError(t, err)
	require.Equal(t, 1, sink.LogRecordCount())

	jsonAttr := attribute.NewSet(attribute.String("blob_format", "json"))
	metadatatest.AssertEqualAzureblobpollingRecordsConsumed(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 2, Attributes: jsonAttr}}, metricdatatest.IgnoreTimestamp())
	metadatatest.AssertEqualAzureblobpollingRecordsEmitted(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 1, Attributes: jsonAttr}}, metricdatatest.IgnoreTimestamp())
}

func TestPollingReceiver_recordsConsumedNotInflatedOnRetry(t *testing.T) {
	// A downstream error must not record records_consumed: the caller leaves the offset
	// unadvanced and re-reads the same bytes next poll, so recording on the failed attempt
	// double-counts on the retry and opens a permanent false drop gap. Only the finally-
	// successful attempt records, once.
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })

	sink := &failOnceLogsConsumer{}
	r := &pollingReceiver{
		logger:   zap.NewNop(),
		cfg:      &Config{Container: "c", BlobFormat: BlobFormatJSON, EnableIncrementalRead: true},
		consumer: blobconsume.NewNDJSONLogsConsumer(sink, zap.NewNop()),
		mut:      &sync.Mutex{}, wg: &sync.WaitGroup{},
	}
	require.NoError(t, r.initTelemetry(tt.NewTelemetrySettings()))

	content := []byte("{\"a\":1}\n{\"b\":2}\n")
	_, err := r.consume(context.Background(), content)
	require.Error(t, err, "first attempt fails downstream")

	_, err = r.consume(context.Background(), content)
	require.NoError(t, err, "retry succeeds")

	jsonAttr := attribute.NewSet(attribute.String("blob_format", "json"))
	metadatatest.AssertEqualAzureblobpollingRecordsConsumed(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 2, Attributes: jsonAttr}}, metricdatatest.IgnoreTimestamp())
	metadatatest.AssertEqualAzureblobpollingRecordsEmitted(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 2, Attributes: jsonAttr}}, metricdatatest.IgnoreTimestamp())
}

// failOnceLogsConsumer errors on its first ConsumeLogs call and succeeds afterward, so a test
// can drive a downstream failure followed by a successful retry.
type failOnceLogsConsumer struct{ calls int }

func (c *failOnceLogsConsumer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (c *failOnceLogsConsumer) ConsumeLogs(context.Context, plog.Logs) error {
	c.calls++
	if c.calls == 1 {
		return errors.New("downstream boom")
	}
	return nil
}

func TestPollingReceiver_blobFormatAttr(t *testing.T) {
	require.Equal(t, "otlp", (&pollingReceiver{cfg: &Config{}}).blobFormatAttr(), "the empty default format reports as otlp")
	require.Equal(t, "records-json", (&pollingReceiver{cfg: &Config{BlobFormat: BlobFormatRecordsJSON}}).blobFormatAttr())
}
