// Copyright  observIQ, Inc.
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

package lookupprocessor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAPISourceLookup_FlattenJSON(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer t", r.Header.Get("Authorization"))
		_, _ = fmt.Fprintln(w, `{"host":"h1","region":"us-west","count":42}`)
	}))
	defer server.Close()

	src, err := NewAPISource(&APIConfig{
		URL:     server.URL + "/lookup/${fieldValue}",
		Method:  http.MethodGet,
		Headers: map[string]string{"Authorization": "Bearer t"},
		Timeout: 2 * time.Second,
	}, zap.NewNop())
	require.NoError(t, err)

	got, err := src.Lookup("0.0.0.0")
	require.NoError(t, err)
	require.Equal(t, "h1", got["host"])
	require.Equal(t, "us-west", got["region"])
	require.Equal(t, "42", got["count"])
	require.Equal(t, "/lookup/0.0.0.0", capturedPath)
	require.NoError(t, src.Close())
}

func TestAPISourceLookup_ResponseMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, `{"data":{"hostname":"h1","owner":{"team":"sre"}}}`)
	}))
	defer server.Close()

	src, err := NewAPISource(&APIConfig{
		URL:     server.URL + "/h/${fieldValue}",
		Timeout: 2 * time.Second,
		ResponseMapping: map[string]string{
			"host": "data.hostname",
			"team": "data.owner.team",
		},
	}, zap.NewNop())
	require.NoError(t, err)

	got, err := src.Lookup("any")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"host": "h1", "team": "sre"}, got)
}

func TestAPISourceLookup_RetryThenSuccess(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintln(w, `{"k":"v"}`)
	}))
	defer server.Close()

	src, err := NewAPISource(&APIConfig{
		URL:     server.URL,
		Timeout: 2 * time.Second,
	}, zap.NewNop())
	require.NoError(t, err)

	got, err := src.Lookup("any")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"k": "v"}, got)
	require.EqualValues(t, 3, attempts.Load())
}

func TestAPISourceLookup_AllRetriesFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()

	src, err := NewAPISource(&APIConfig{
		URL:     server.URL,
		Timeout: 1 * time.Second,
	}, zap.NewNop())
	require.NoError(t, err)

	_, err = src.Lookup("any")
	require.Error(t, err)
}

func TestAPISourceSubstituteURL(t *testing.T) {
	src, err := NewAPISource(&APIConfig{URL: "https://example.com", Timeout: time.Second}, zap.NewNop())
	require.NoError(t, err)

	cases := map[string]struct {
		tmpl string
		want string
	}{
		"fieldValue":        {tmpl: "https://example.com/$fieldValue", want: "https://example.com/a%2Fb"},
		"braced fieldValue": {tmpl: "https://example.com/${fieldValue}", want: "https://example.com/a%2Fb"},
		"key":               {tmpl: "https://example.com?q=$key", want: "https://example.com?q=a%2Fb"},
		"braced key":        {tmpl: "https://example.com?q=${key}", want: "https://example.com?q=a%2Fb"},
		"no placeholder":    {tmpl: "https://example.com", want: "https://example.com"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			src.urlTemplate = tc.tmpl
			require.Equal(t, tc.want, src.substituteURL("a/b"))
		})
	}
}
