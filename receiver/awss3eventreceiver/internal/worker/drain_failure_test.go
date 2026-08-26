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

package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
)

// errStorage fails every save, so the checkpoint error path runs.
type errStorage struct{ err error }

func (s errStorage) SaveStorageData(context.Context, string, storageclient.StorageData) error {
	return s.err
}
func (s errStorage) LoadStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (s errStorage) DeleteStorageData(context.Context, string) error { return nil }
func (s errStorage) Close(context.Context) error                     { return nil }

func newTestObsReport(t *testing.T) *receiverhelper.ObsReport {
	t.Helper()
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             component.NewID(metadata.Type),
		Transport:              "sqs",
		ReceiverCreateSettings: receivertest.NewNopSettings(metadata.Type),
	})
	require.NoError(t, err)
	return obsrecv
}

// TestFlush_ReportsConsumerFailure asserts a rejected batch surfaces as an error and a
// log line. The caller nacks the message, so the object is redelivered.
func TestFlush_ReportsConsumerFailure(t *testing.T) {
	t.Parallel()

	consumeErr := errors.New("pipeline backpressure")
	core, logs := observer.New(zap.ErrorLevel)

	w := &Worker{
		nextConsumer: consumertest.NewErr(consumeErr),
		obsrecv:      newTestObsReport(t),
	}

	err := w.flush(context.Background(), plog.NewLogs(), 3, zap.New(core))
	require.ErrorIs(t, err, consumeErr)
	require.ErrorContains(t, err, "consume logs")
	require.Equal(t, 1, logs.FilterMessage("consume logs").Len())
}

// TestCheckpoint_LogsSaveFailure asserts a failed save is logged rather than dropped.
// The offset stays where it was, so the object resumes from the last saved position.
func TestCheckpoint_LogsSaveFailure(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.ErrorLevel)
	w := &Worker{offsetStorage: errStorage{err: errors.New("storage extension unavailable")}}

	w.checkpoint(context.Background(), "_aws_s3_event_offset_key", 512, zap.New(core))

	entries := logs.FilterMessage("Failed to save offset").All()
	require.Len(t, entries, 1)
	require.Equal(t, int64(512), entries[0].ContextMap()["offset"])
}
