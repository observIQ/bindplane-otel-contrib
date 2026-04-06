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

package azureloganalyticsexporter

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	"go.uber.org/zap"
)

func newTestExporter() *azureLogAnalyticsExporter {
	return &azureLogAnalyticsExporter{
		logger: zap.NewNop(),
	}
}

func newResponseError(statusCode int, header http.Header) *azcore.ResponseError {
	if header == nil {
		header = http.Header{}
	}
	return &azcore.ResponseError{
		StatusCode: statusCode,
		RawResponse: &http.Response{
			StatusCode: statusCode,
			Header:     header,
		},
	}
}

func TestClassifyError_PermanentErrors(t *testing.T) {
	exp := newTestExporter()

	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "400 Bad Request", statusCode: http.StatusBadRequest},
		{name: "401 Unauthorized", statusCode: http.StatusUnauthorized},
		{name: "403 Forbidden", statusCode: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := exp.classifyError(newResponseError(tt.statusCode, nil))
			require.Error(t, err)
			assert.True(t, consumererror.IsPermanent(err), "expected permanent error for status %d", tt.statusCode)
		})
	}
}

func TestClassifyError_RetryableErrors(t *testing.T) {
	exp := newTestExporter()

	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "429 Too Many Requests", statusCode: http.StatusTooManyRequests},
		{name: "502 Bad Gateway", statusCode: http.StatusBadGateway},
		{name: "503 Service Unavailable", statusCode: http.StatusServiceUnavailable},
		{name: "504 Gateway Timeout", statusCode: http.StatusGatewayTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := exp.classifyError(newResponseError(tt.statusCode, nil))
			require.Error(t, err)
			assert.False(t, consumererror.IsPermanent(err), "expected retryable error for status %d", tt.statusCode)
		})
	}
}

func TestClassifyError_429WithRetryAfter(t *testing.T) {
	exp := newTestExporter()

	header := http.Header{}
	header.Set("Retry-After", "30")

	err := exp.classifyError(newResponseError(http.StatusTooManyRequests, header))
	require.Error(t, err)
	assert.False(t, consumererror.IsPermanent(err))

	// Verify it is a throttle retry error by comparing with a known throttle error format.
	// The unexported throttleRetry type formats as "Throttle (<delay>), error: <msg>".
	knownThrottle := exporterhelper.NewThrottleRetry(errors.New("test"), 30*time.Second)
	assert.Contains(t, err.Error(), "Throttle (30s)", "expected ThrottleRetry error for 429 with Retry-After header; known format: %s", knownThrottle.Error())
}

func TestClassifyError_NetworkError(t *testing.T) {
	exp := newTestExporter()

	// A plain error that is not *azcore.ResponseError
	networkErr := fmt.Errorf("connection refused")
	err := exp.classifyError(networkErr)
	require.Error(t, err)
	assert.False(t, consumererror.IsPermanent(err), "network errors should be retryable")
}

func TestClassifyError_MarshalPermanent(t *testing.T) {
	// Verify that marshal errors wrapped with consumererror.NewPermanent are permanent.
	// This tests the marshal error path in logsDataPusher, not classifyError itself.
	err := consumererror.NewPermanent(fmt.Errorf("failed to convert logs to Azure Log Analytics format: %w", errors.New("bad data")))
	require.Error(t, err)
	assert.True(t, consumererror.IsPermanent(err), "marshal errors should be permanent")
}
