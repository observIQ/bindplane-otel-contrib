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

// MarshalChronicleAPIRawLogs marshals the logs into chronicle API request payloads.
func (m *protoMarshaler) MarshalChronicleAPIRawLogs(ctx context.Context, ld plog.Logs) (map[string][]*api.ImportLogsRequest, uint, error) {
	rawLogs, totalBytes, err := m.extractChronicleAPIRawLogs(ctx, ld)
	if err != nil {
		return nil, 0, fmt.Errorf("extract raw logs: %w", err)
	}
	return m.constructChronicleAPIPayloads(rawLogs), totalBytes, nil
}

func (m *protoMarshaler) extractChronicleAPIRawLogs(ctx context.Context, ld plog.Logs) (map[string][]*api.Log, uint, error) {
	entries := make(map[string][]*api.Log)
	totalBytes, err := m.forEachLogRecord(ctx, ld, func(p processedLog) {
		ingestionLabels := make(map[string]*api.Log_LogLabel, len(p.ingestionLabels))
		for key, value := range p.ingestionLabels {
			ingestionLabels[key] = &api.Log_LogLabel{
				Value: value,
			}
		}

		entry := &api.Log{
			LogEntryTime:         timestamppb.New(p.timestamp),
			CollectionTime:       timestamppb.New(p.collectionTime),
			Data:                 p.data,
			EnvironmentNamespace: p.namespace,
			Labels:               ingestionLabels,
		}
		entries[p.logType] = append(entries[p.logType], entry)
	})
	if err != nil {
		return nil, 0, err
	}

	return entries, totalBytes, nil
}

func (m *protoMarshaler) constructChronicleAPIPayloads(rawLogs map[string][]*api.Log) map[string][]*api.ImportLogsRequest {
	payloads := make(map[string][]*api.ImportLogsRequest, len(rawLogs))

	metricCtx := context.Background()

	for logType, entries := range rawLogs {
		if len(entries) > 0 {
			request := m.buildChronicleAPIRequest(entries)

			payloads[logType] = m.enforceMaximumsChronicleAPIRequest(request)
			for _, payload := range payloads[logType] {
				m.telemetry.GoogleSecopsExporterBatchSize.Record(metricCtx, int64(len(payload.GetInlineSource().Logs)))
				m.telemetry.GoogleSecopsExporterPayloadSize.Record(metricCtx, int64(proto.Size(payload)))
			}
		}
	}
	return payloads
}

func (m *protoMarshaler) enforceMaximumsChronicleAPIRequest(request *api.ImportLogsRequest) []*api.ImportLogsRequest {
	size := proto.Size(request)
	logs := request.GetInlineSource().Logs
	if size <= m.cfg.BatchRequestSizeLimit {
		return []*api.ImportLogsRequest{
			request,
		}
	}

	if len(logs) < 2 {
		m.teleSettings.Logger.Error("Single entry exceeds max request size. Dropping entry", zap.Int("size", size))
		return []*api.ImportLogsRequest{}
	}

	// split request into two
	mid := len(logs) / 2
	leftHalf := logs[:mid]
	rightHalf := logs[mid:]

	request.GetInlineSource().Logs = leftHalf
	otherHalfRequest := m.buildChronicleAPIRequest(rightHalf)

	// re-enforce max size restriction on each half
	enforcedRequest := m.enforceMaximumsChronicleAPIRequest(request)
	enforcedOtherHalfRequest := m.enforceMaximumsChronicleAPIRequest(otherHalfRequest)

	return append(enforcedRequest, enforcedOtherHalfRequest...)
}

func (m *protoMarshaler) buildChronicleAPIRequest(entries []*api.Log) *api.ImportLogsRequest {
	return &api.ImportLogsRequest{
		// TODO: Add hint?
		// No solid guidance on what this should be
		Hint: "",

		Source: &api.ImportLogsRequest_InlineSource{
			InlineSource: &api.ImportLogsRequest_LogsInlineSource{
				Forwarder: m.buildForwarderString(),
				Logs:      entries,
			},
		},
	}
}

func (m *protoMarshaler) buildForwarderString() string {
	format := "projects/%s/locations/%s/instances/%s/forwarders/%s"
	return fmt.Sprintf(format, m.cfg.ProjectNumber, m.cfg.Location, m.cfg.CustomerID, string(m.collectorID[:]))
}
