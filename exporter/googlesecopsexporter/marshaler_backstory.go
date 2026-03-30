package googlesecopsexporter

import (
	"context"
	"fmt"

	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/protos/api"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MarshalBackstoryRawLogs marshals the logs into backstory API request payloads.
func (m *protoMarshaler) MarshalBackstoryRawLogs(ctx context.Context, ld plog.Logs) ([]*api.BatchCreateLogsRequest, uint, error) {
	logGrouper, totalBytes, err := m.extractBackstoryRawLogs(ctx, ld)
	if err != nil {
		return nil, 0, fmt.Errorf("extract raw logs: %w", err)
	}
	return m.constructBackstoryPayloads(logGrouper), totalBytes, nil
}

func (m *protoMarshaler) extractBackstoryRawLogs(ctx context.Context, ld plog.Logs) (*logGrouper, uint, error) {
	logGrouper := newLogGrouper()
	totalBytes, err := m.forEachLogRecord(ctx, ld, func(p processedLog) {
		entry := &api.LogEntry{
			Timestamp:      timestamppb.New(p.timestamp),
			CollectionTime: timestamppb.New(p.collectionTime),
			Data:           p.data,
		}

		ingestionLabels := make([]*api.Label, 0, len(p.ingestionLabels))
		for key, value := range p.ingestionLabels {
			ingestionLabels = append(ingestionLabels, &api.Label{
				Key:   key,
				Value: value,
			})
		}
		logGrouper.Add(entry, p.namespace, p.logType, ingestionLabels)
	})
	if err != nil {
		return nil, 0, err
	}

	return logGrouper, totalBytes, nil
}

func (m *protoMarshaler) constructBackstoryPayloads(logGrouper *logGrouper) []*api.BatchCreateLogsRequest {
	payloads := make([]*api.BatchCreateLogsRequest, 0, len(logGrouper.groups))

	metricCtx := context.Background()

	logGrouper.ForEach(func(entries []*api.LogEntry, namespace, logType string, ingestionLabels []*api.Label) {
		if namespace == "" {
			namespace = m.cfg.Namespace
		}

		request := m.buildBackstoryRequest(entries, logType, namespace, ingestionLabels)

		payloads = append(payloads, m.enforceMaximumsBackstoryRequest(request)...)
		for _, payload := range payloads {
			m.telemetry.GoogleSecopsExporterBatchSize.Record(metricCtx, int64(len(payload.Batch.Entries)))
			m.telemetry.GoogleSecopsExporterPayloadSize.Record(metricCtx, int64(proto.Size(payload)))
		}
	})
	return payloads
}

func (m *protoMarshaler) enforceMaximumsBackstoryRequest(request *api.BatchCreateLogsRequest) []*api.BatchCreateLogsRequest {
	size := proto.Size(request)
	entries := request.Batch.Entries
	if size <= m.cfg.BatchRequestSizeLimit {
		return []*api.BatchCreateLogsRequest{
			request,
		}
	}

	if len(entries) < 2 {
		m.teleSettings.Logger.Error("Single entry exceeds max request size. Dropping entry", zap.Int("size", size))
		return []*api.BatchCreateLogsRequest{}
	}

	// split request into two
	mid := len(entries) / 2
	leftHalf := entries[:mid]
	rightHalf := entries[mid:]

	request.Batch.Entries = leftHalf
	otherHalfRequest := m.buildBackstoryRequest(rightHalf, request.Batch.LogType, request.Batch.Source.Namespace, request.Batch.Source.Labels)

	// re-enforce max size restriction on each half
	enforcedRequest := m.enforceMaximumsBackstoryRequest(request)
	enforcedOtherHalfRequest := m.enforceMaximumsBackstoryRequest(otherHalfRequest)

	return append(enforcedRequest, enforcedOtherHalfRequest...)
}

func (m *protoMarshaler) buildBackstoryRequest(entries []*api.LogEntry, logType, namespace string, ingestionLabels []*api.Label) *api.BatchCreateLogsRequest {
	return &api.BatchCreateLogsRequest{
		Batch: &api.LogEntryBatch{
			StartTime: timestamppb.New(m.startTime),
			Entries:   entries,
			LogType:   logType,
			Source: &api.EventSource{
				CollectorId: m.collectorID,
				CustomerId:  m.customerID,
				Labels:      ingestionLabels,
				Namespace:   namespace,
			},
		},
	}
}
