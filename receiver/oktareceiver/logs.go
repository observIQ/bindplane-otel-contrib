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

package oktareceiver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/okta/okta-sdk-golang/v6/okta"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

const oktaLogLimit int32 = 1000

type oktaLogsReceiver struct {
	cfg          Config
	client       *okta.APIClient
	consumer     consumer.Logs
	logger       *zap.Logger
	lastPollTime time.Time
	cancel       context.CancelFunc
	wg           *sync.WaitGroup
}

func newOktaLogsReceiver(cfg *Config, logger *zap.Logger, consumer consumer.Logs, client *okta.APIClient) *oktaLogsReceiver {
	return &oktaLogsReceiver{
		cfg:      *cfg,
		client:   client,
		consumer: consumer,
		logger:   logger,
		wg:       &sync.WaitGroup{},
	}
}

func (r *oktaLogsReceiver) Start(ctx context.Context, _ component.Host) error {
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.wg.Add(1)
	go r.startPolling(ctx)
	return nil
}

func (r *oktaLogsReceiver) startPolling(ctx context.Context) {
	defer r.wg.Done()
	t := time.NewTicker(r.cfg.PollInterval)

	err := r.poll(ctx)
	if err != nil {
		r.logger.Error("there was an error during the first poll", zap.Error(err))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := r.poll(ctx)
			if err != nil {
				r.logger.Error("there was an error during the poll", zap.Error(err))
			}
		}
	}
}

func (r *oktaLogsReceiver) poll(ctx context.Context) error {
	logEvents, err := r.getLogs(ctx)
	if err != nil {
		return err
	}
	observedTime := pcommon.NewTimestampFromTime(time.Now())
	logs := r.processLogEvents(observedTime, logEvents)
	if logs.LogRecordCount() > 0 {
		if err := r.consumer.ConsumeLogs(ctx, logs); err != nil {
			return err
		}
	}
	return nil
}

func (r *oktaLogsReceiver) getLogs(ctx context.Context) ([]okta.LogEvent, error) {
	now := time.Now().UTC()

	if r.lastPollTime.IsZero() {
		r.lastPollTime = now.Add(-r.cfg.PollInterval)
	}

	since := r.lastPollTime.Format(oktaTimeFormat)
	until := now.Format(oktaTimeFormat)

	events, err := r.getLogEvents(ctx, since, until)
	if err != nil {
		return nil, fmt.Errorf("error fetching okta log events: %w", err)
	}

	r.lastPollTime = now
	return events, nil
}

func (r *oktaLogsReceiver) getLogEvents(ctx context.Context, since, until string) ([]okta.LogEvent, error) {
	events, resp, err := r.client.SystemLogAPI.ListLogEvents(ctx).
		Since(since).
		Until(until).
		Limit(oktaLogLimit).
		Execute()
	if err != nil {
		return nil, err
	}

	allEvents := events
	for resp.HasNextPage() {
		var nextEvents []okta.LogEvent
		resp, err = resp.Next(&nextEvents)
		if err != nil {
			return nil, err
		}
		allEvents = append(allEvents, nextEvents...)
	}

	return allEvents, nil
}

func (r *oktaLogsReceiver) processLogEvents(observedTime pcommon.Timestamp, logEvents []okta.LogEvent) plog.Logs {
	logs := plog.NewLogs()

	// resource attributes
	resourceLogs := logs.ResourceLogs().AppendEmpty()
	resourceLogs.ScopeLogs().AppendEmpty()
	resourceAttributes := resourceLogs.Resource().Attributes()
	resourceAttributes.PutStr("okta.domain", r.cfg.Domain)

	for _, logEvent := range logEvents {
		logRecord := resourceLogs.ScopeLogs().At(0).LogRecords().AppendEmpty()

		// timestamps
		logRecord.SetObservedTimestamp(observedTime)
		if logEvent.Published != nil {
			logRecord.SetTimestamp(pcommon.NewTimestampFromTime(*logEvent.Published))
		}

		// body
		logEventBytes, err := json.Marshal(logEvent)
		if err != nil {
			r.logger.Error("unable to marshal logEvent", zap.Error(err))
		} else {
			logRecord.Body().SetStr(string(logEventBytes))
		}

		// attributes
		logRecord.Attributes().PutStr("uuid", derefStr(logEvent.Uuid))
		logRecord.Attributes().PutStr("eventType", derefStr(logEvent.EventType))
		logRecord.Attributes().PutStr("displayMessage", derefStr(logEvent.DisplayMessage))
		if logEvent.Outcome != nil {
			logRecord.Attributes().PutStr("outcome.result", derefStr(logEvent.Outcome.Result))
		}
		if logEvent.Actor != nil {
			logRecord.Attributes().PutStr("actor.id", derefStr(logEvent.Actor.Id))
			logRecord.Attributes().PutStr("actor.alternateId", derefStr(logEvent.Actor.AlternateId))
			logRecord.Attributes().PutStr("actor.displayName", derefStr(logEvent.Actor.DisplayName))
		}
	}

	return logs
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (r *oktaLogsReceiver) Shutdown(_ context.Context) error {
	r.logger.Debug("shutting down logs receiver")
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}
