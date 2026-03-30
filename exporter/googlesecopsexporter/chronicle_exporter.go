package googlesecopsexporter

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/internal/metadata"
	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/protos/api"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	grpcgzip "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	// stdlib defaults to 2. Under heavy load, higher default values are
	// useful for avoiding re-connections.
	defaultHTTPClientMaxIdleConnsPerHost = 10

	// stdlib default is 0 (no timeout), best practice is to set a timeout
	defaultHTTPClientResponseHeaderTimeout = 10 * time.Second

	// Settings mirror stdlib defaults
	// https://pkg.go.dev/net/http#RoundTripper
	defaultHTTPClientMaxIdleConns          = 100
	defaultHTTPClientIdleConnTimeout       = 90 * time.Second
	defaultHTTPClientTLSHandshakeTimeout   = 10 * time.Second
	defaultHTTPClientExpectContinueTimeout = 1 * time.Second

	attrError = "error"
)

var (
	attrErrorNone    attribute.KeyValue = attribute.String(attrError, "none")
	attrErrorUnknown attribute.KeyValue = attribute.String(attrError, "unknown")
)

type exists struct{}

type chronicleAPIExporter struct {
	cfg        *Config
	set        component.TelemetrySettings
	exporterID string
	marshaler  *protoMarshaler
	client     *http.Client
	transport  *http.Transport
	metrics    *metricsReporter

	telemetry        *metadata.TelemetryBuilder
	metricAttributes attribute.Set
}

func newChronicleAPIExporter(cfg *Config, params exporter.Settings, telemetry *metadata.TelemetryBuilder) (*chronicleAPIExporter, error) {
	marshaler, err := newProtoMarshaler(*cfg, params.TelemetrySettings, telemetry, params.Logger)
	if err != nil {
		return nil, fmt.Errorf("create proto marshaler: %w", err)
	}
	macAddress := macAddress()
	params.Logger.Debug("Creating Chronicle API exporter", zap.String("exporter_id", params.ID.String()), zap.String("mac_address", macAddress))
	return &chronicleAPIExporter{
		cfg:        cfg,
		set:        params.TelemetrySettings,
		exporterID: params.ID.String(),
		marshaler:  marshaler,
		telemetry:  telemetry,
		metricAttributes: attribute.NewSet(
			attribute.KeyValue{
				Key:   "exporter",
				Value: attribute.StringValue(params.ID.String()),
			},
			attribute.KeyValue{
				Key:   "exporter_type",
				Value: attribute.StringValue(params.ID.Type().String()),
			},
			attribute.KeyValue{
				Key:   "host.mac_address",
				Value: attribute.StringValue(macAddress),
			},
		),
	}, nil
}

// The Chronicle API URL for the request: {baseEndpoint}/logTypes/{logType}/logs:import
// Override for testing
var httpEndpoint = func(cfg *Config, logType string) string {
	formatString := "%s/logTypes/%s/logs:import"
	return fmt.Sprintf(formatString, baseEndpoint(cfg), logType)
}

var getLogTypesEndpoint = func(cfg *Config) string {
	formatString := "%s/logTypes"
	return fmt.Sprintf(formatString, baseEndpoint(cfg))
}

// httpStatsEndpoint returns the URL for the importStatsEvents REST API.
// Override for testing.
var httpStatsEndpoint = func(cfg *Config, collectorID string) string {
	formatString := "%s/forwarders/%s:importStatsEvents"
	return fmt.Sprintf(formatString, baseEndpoint(cfg), collectorID)
}

// The Chronicle API base URL: https://{region}-{hostname}/{version}/projects/{project}/location/{region}/instances/{customerID}
func baseEndpoint(cfg *Config) string {
	var hostname string
	if cfg.OverrideHostname {
		hostname = cfg.Hostname
	} else {
		hostname = fmt.Sprintf("%s-%s", cfg.Location, cfg.Hostname)
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = apiVersionV1Alpha
	}
	formatString := "https://%s/%s/projects/%s/locations/%s/instances/%s"
	return fmt.Sprintf(formatString, hostname, cfg.APIVersion, cfg.ProjectNumber, cfg.Location, cfg.CustomerID)
}

func (exp *chronicleAPIExporter) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (exp *chronicleAPIExporter) Start(ctx context.Context, _ component.Host) error {
	ts, err := tokenSource(ctx, exp.cfg)
	if err != nil {
		return fmt.Errorf("load Google credentials: %w", err)
	}

	exp.transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          defaultHTTPClientMaxIdleConns,
		MaxIdleConnsPerHost:   defaultHTTPClientMaxIdleConnsPerHost,
		IdleConnTimeout:       defaultHTTPClientIdleConnTimeout,
		TLSHandshakeTimeout:   defaultHTTPClientTLSHandshakeTimeout,
		ExpectContinueTimeout: defaultHTTPClientExpectContinueTimeout,
		ResponseHeaderTimeout: defaultHTTPClientResponseHeaderTimeout,
	}

	exp.client = &http.Client{
		Transport: &oauth2.Transport{
			Base:   exp.transport,
			Source: ts,
		},
	}

	if exp.cfg.ValidateLogTypes {
		exp.marshaler.logTypes = exp.loadLogTypes(ctx)
	}

	if exp.cfg.CollectAgentMetrics {
		f := func(ctx context.Context, request *api.BatchCreateEventsRequest) error {
			return exp.uploadStatsEvents(ctx, request, string(exp.cfg.CollectorID[:]))
		}
		metrics, err := newMetricsReporter(exp.cfg, exp.set, exp.exporterID, f)
		if err != nil {
			return fmt.Errorf("create metrics reporter: %w", err)
		}
		exp.metrics = metrics
		exp.metrics.start()
	}

	return nil
}

// https://cloud.google.com/chronicle/docs/reference/rest/v1alpha/projects.locations.instances.logTypes/list
type logType struct {
	Name string `json:"name"`
}

type logTypeResponse struct {
	LogTypes      []logType `json:"logTypes"`
	NextPageToken string    `json:"nextPageToken"`
}

func (exp *chronicleAPIExporter) loadLogTypes(ctx context.Context) map[string]exists {
	logTypes := make(map[string]exists)
	endpoint := getLogTypesEndpoint(exp.cfg)

	request, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		exp.set.Logger.Warn("Failed to create request for loading log types", zap.Error(err))
		return nil
	}

	// time out after 10 seconds
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for {
		if err := ctx.Err(); err != nil {
			exp.set.Logger.Warn("Context cancelled for loading log types", zap.Error(err))
			return nil
		}
		resp, err := exp.client.Do(request)
		if err != nil {
			exp.set.Logger.Warn("Failed to send request to Chronicle for loading log types", zap.Error(err))
			return nil
		}

		var response logTypeResponse

		respBody, err := io.ReadAll(resp.Body)
		if err := resp.Body.Close(); err != nil {
			exp.set.Logger.Warn("Failed to close response body for loading log types", zap.Error(err))
		}

		if err == nil && resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(respBody, &response); err != nil {
				exp.set.Logger.Warn("Failed to unmarshal response body", zap.Error(err))
				return nil
			}
			for _, logType := range response.LogTypes {
				logTypes[parseLogTypes(logType.Name)] = exists{}
			}
		}

		if err != nil {
			exp.set.Logger.Warn("Failed to read response body for loading log types", zap.Error(err))
			return nil
		}
		if resp.StatusCode != http.StatusOK {
			exp.set.Logger.Warn("Received non-OK response from Chronicle for loading log types", zap.String("status", resp.Status), zap.ByteString("response", respBody))
			return nil
		}
		if response.NextPageToken == "" {
			break
		}
		request, err = http.NewRequestWithContext(ctx, "GET", endpoint+"?pageToken="+response.NextPageToken, nil)
		if err != nil {
			exp.set.Logger.Warn("Failed to create request for loading log types", zap.Error(err))
			return nil
		}

	}
	return logTypes
}

// API returns a list of log types in the format:
//
//	{
//	     "name": "projects/408460088155/locations/us/instances/b536658e-469e-44a5-b764-d5ab15b72ce0/logTypes/AKAMAI_SIEM_CONNECTOR",
//	     "displayName": "Akamai SIEM Connector"
//	},
//
// we need to get the token after the last /
func parseLogTypes(logTypes string) string {

	parts := strings.Split(logTypes, "/")
	return parts[len(parts)-1]
}

func (exp *chronicleAPIExporter) Shutdown(context.Context) error {
	if exp.metrics != nil {
		exp.metrics.shutdown()
	}
	if exp.transport != nil {
		exp.transport.CloseIdleConnections()
	}
	return nil
}

// ConsumeLogs sends logs to Chronicle via HTTP.
//
// Retry behavior: When this function returns an error, the OTel collector's
// exporterhelper will retry the entire batch (ld plog.Logs) from the beginning.
// This means all payloads will be retried, including any that succeeded before
// the error occurred. Chronicle is expected to handle duplicate requests
// idempotently to prevent duplicate log entries.
//
// Metrics: When retry is enabled, raw bytes are only counted on success to prevent
// double-counting across retry attempts. When retry is disabled, bytes are counted
// regardless of success/failure since this is the only attempt to send the data.
func (exp *chronicleAPIExporter) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	payloads, totalBytes, err := exp.marshaler.MarshalChronicleAPIRawLogs(ctx, ld)
	if err != nil {
		return fmt.Errorf("marshal logs: %w", err)
	}
	successfulPayloads := []*api.ImportLogsRequest{}
	for logType, logTypePayloads := range payloads {
		for _, payload := range logTypePayloads {
			if err := exp.uploadToChronicleAPI(ctx, payload, logType); err != nil {
				// Track the failure for observability
				exp.telemetry.GoogleSecopsExporterLogsSendFailed.Add(ctx, 1, metric.WithAttributeSet(exp.metricAttributes))

				// If retry is disabled, count bytes for payloads that succeeded before this failure
				if !exp.cfg.BackOffConfig.Enabled {
					exp.countAndReportBatchBytes(ctx, successfulPayloads)
				}
				return fmt.Errorf("upload to chronicle API: %w", err)
			}
			successfulPayloads = append(successfulPayloads, payload)
		}
	}
	// Count bytes on success (for both retry enabled and disabled cases)
	exp.telemetry.GoogleSecopsExporterRawBytes.Add(
		ctx,
		int64(totalBytes),
		metric.WithAttributeSet(exp.metricAttributes),
	)
	return nil
}

func (exp *chronicleAPIExporter) countAndReportBatchBytes(ctx context.Context, payloads []*api.ImportLogsRequest) {
	totalBytes := uint(0)
	for _, payload := range payloads {
		inlineSource := payload.GetInlineSource()
		if inlineSource == nil {
			exp.set.Logger.Warn("Payload source is not InlineSource, skipping bytes calculation")
			continue
		}
		for _, entry := range inlineSource.Logs {
			totalBytes += uint(len(entry.Data))
		}
	}
	if totalBytes > 0 {
		exp.telemetry.GoogleSecopsExporterRawBytes.Add(
			ctx,
			int64(totalBytes),
			metric.WithAttributeSet(exp.metricAttributes),
		)
	}
}

func (exp *chronicleAPIExporter) compressBody(data []byte) (io.Reader, error) {
	if exp.cfg.Compression != grpcgzip.Name {
		return bytes.NewBuffer(data), nil
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	if _, err := gz.Write(data); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return &b, nil
}

func (exp *chronicleAPIExporter) uploadToChronicleAPI(ctx context.Context, logs *api.ImportLogsRequest, logType string) error {
	data, err := protojson.Marshal(logs)
	if err != nil {
		return fmt.Errorf("marshal protobuf logs to JSON: %w", err)
	}

	body, err := exp.compressBody(data)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, "POST", httpEndpoint(exp.cfg, logType), body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	if exp.cfg.Compression == grpcgzip.Name {
		request.Header.Set("Content-Encoding", "gzip")
	}

	request.Header.Set("Content-Type", "application/json")

	// Track request latency
	start := time.Now()

	resp, err := exp.client.Do(request)
	if err != nil {
		logTypeAttr := attribute.String("logType", logType)
		errAttr := attrErrorUnknown
		if errors.Is(err, context.DeadlineExceeded) {
			errAttr = attribute.String(attrError, "timeout")
		}
		exp.telemetry.GoogleSecopsExporterRequestLatency.Record(
			ctx, time.Since(start).Milliseconds(),
			metric.WithAttributeSet(attribute.NewSet(errAttr, logTypeAttr)),
		)
		exp.telemetry.GoogleSecopsExporterRequestCount.Add(ctx, 1,
			metric.WithAttributeSet(attribute.NewSet(errAttr, logTypeAttr)))
		return fmt.Errorf("send request to Chronicle API: %w", err)
	}
	defer resp.Body.Close()

	statusAttr := attribute.String("status", resp.Status)
	exp.telemetry.GoogleSecopsExporterRequestLatency.Record(
		ctx, time.Since(start).Milliseconds(),
		metric.WithAttributeSet(attribute.NewSet(statusAttr)),
	)
	exp.telemetry.GoogleSecopsExporterRequestCount.Add(ctx, 1,
		metric.WithAttributeSet(attribute.NewSet(attrErrorNone)))

	if resp.StatusCode == http.StatusOK {
		if exp.metrics != nil {
			if inlineSource := logs.GetInlineSource(); inlineSource != nil {
				totalLogs := int64(len(inlineSource.GetLogs()))
				exp.metrics.recordSent(totalLogs)
			}
		}
		return nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		exp.set.Logger.Warn("Failed to read response body", zap.Error(err), zap.String("logType", logType))
	}

	exp.set.Logger.Warn("Received non-OK response from Chronicle API", zap.String("status", resp.Status), zap.ByteString("response", respBody), zap.String("logType", logType))

	// TODO interpret with https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/internal/coreinternal/errorutil/http.go
	statusErr := errors.New(resp.Status)
	switch resp.StatusCode {
	// Retryable response codes: https://github.com/open-telemetry/opentelemetry-proto/blob/main/docs/specification.md#retryable-response-codes
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return statusErr
	default:
		if exp.cfg.LogErroredPayloads {
			exp.set.Logger.Warn("Import request rejected", zap.String("logType", logType), zap.String("rejectedRequest", string(data)))
		}
		if exp.metrics != nil {
			if inlineSource := logs.GetInlineSource(); inlineSource != nil {
				totalLogs := int64(len(inlineSource.GetLogs()))
				exp.metrics.recordDropped(totalLogs)
			}
		}
		return consumererror.NewPermanent(statusErr)
	}
}

func (exp *chronicleAPIExporter) uploadStatsEvents(ctx context.Context, request *api.BatchCreateEventsRequest, collectorID string) error {
	// Convert from the gRPC BatchCreateEventsRequest to the REST ImportStatsEventsRequest format.
	batch := request.GetBatch()
	statsEvents := make([]*api.IngestionStatsEvent, 0, len(batch.GetEvents()))
	for _, event := range batch.GetEvents() {
		statsEvents = append(statsEvents, &api.IngestionStatsEvent{
			EventTime: event.GetTimestamp(),
			Event: &api.IngestionStatsEvent_AgentStats{
				AgentStats: event.GetAgentStats(),
			},
		})
	}

	importRequest := &api.ImportStatsEventsRequest{
		Source: &api.ImportStatsEventsRequest_InlineSource{
			InlineSource: &api.StatsInlineSource{
				Events:       statsEvents,
				EventBatchId: base64.StdEncoding.EncodeToString(batch.GetId()),
			},
		},
	}

	data, err := protojson.Marshal(importRequest)
	if err != nil {
		return fmt.Errorf("marshal stats event to JSON: %w", err)
	}

	body, err := exp.compressBody(data)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", httpStatsEndpoint(exp.cfg, collectorID), body)
	if err != nil {
		return fmt.Errorf("create stats request: %w", err)
	}

	if exp.cfg.Compression == grpcgzip.Name {
		httpReq.Header.Set("Content-Encoding", "gzip")
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := exp.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send stats request to Chronicle API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		exp.set.Logger.Warn("Failed to upload agent stats",
			zap.String("status", resp.Status),
			zap.ByteString("response", respBody),
		)
		return fmt.Errorf("upload stats to Chronicle API: %s", resp.Status)
	}

	return nil
}
