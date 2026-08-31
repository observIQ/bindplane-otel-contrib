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

package networkcheckreceiver

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
)

func defaultLogsConfig() LogsConfig {
	return LogsConfig{IncludeTLSDetails: true, RedactURLUserinfo: true}
}

func TestRedactEndpoint(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no userinfo", "https://example.com/path", "https://example.com/path"},
		{"user and password", "https://user:secret@example.com", "https://user:xxxxx@example.com"},
		{"user only", "https://user@example.com", "https://user@example.com"},
		{"bare host", "example.com", "example.com"},
		{"unparseable with at", "http://us er:pw@example.com", "http://example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactEndpoint(tc.in)
			require.Equal(t, tc.want, got)
			require.NotContains(t, got, "secret", "password must never survive redaction")
		})
	}
}

func TestBuildHTTPLogRecord_Success(t *testing.T) {
	start := time.Now().Add(-2 * time.Second)
	ts := &targetState{
		cfg:       TargetConfig{Method: MethodHTTP, HTTPMethod: "GET"},
		dnsServer: "1.1.1.1:53",
	}
	ts.cfg.Endpoint = "https://example.com"

	r := PingResult{
		Method:        MethodHTTP,
		StatusCode:    200,
		DNSLookup:     2 * time.Millisecond,
		TCPConnect:    11 * time.Millisecond,
		TLSHandshake:  184 * time.Millisecond,
		RequestWrite:  300 * time.Microsecond,
		ResponseRead:  8 * time.Millisecond,
		TotalDuration: 206 * time.Millisecond,
		ResolvedIP:    "93.184.216.34",
		ResponseSize:  1256,
		Protocol:      "HTTP/2.0",
		TLS: &TLSDetails{
			Version:      "TLS 1.3",
			CipherSuite:  "TLS_AES_128_GCM_SHA256",
			CertIssuer:   "CN=Test CA",
			CertSubject:  "CN=example.com",
			CertNotAfter: time.Now().Add(30 * 24 * time.Hour),
			CertDaysLeft: 30,
		},
	}

	rec := plog.NewLogRecord()
	buildHTTPLogRecord(rec, ts, r, start, time.Now(), defaultLogsConfig())

	require.Equal(t, plog.SeverityNumberInfo, rec.SeverityNumber())
	require.Equal(t, start.UnixNano(), rec.Timestamp().AsTime().UnixNano(),
		"timestamp must be request start, not completion")

	attrs := rec.Attributes()
	v, ok := attrs.Get("http.response.status_code")
	require.True(t, ok)
	require.EqualValues(t, 200, v.Int())

	v, ok = attrs.Get("server.resolved_ip")
	require.True(t, ok)
	require.Equal(t, "93.184.216.34", v.Str())

	v, ok = attrs.Get("tls.cert.days_remaining")
	require.True(t, ok)
	require.InDelta(t, 30, v.Double(), 0.001)

	phases, ok := rec.Body().Map().Get("phases")
	require.True(t, ok)
	pm := phases.Map()
	dns, _ := pm.Get("dns_ms")
	require.InDelta(t, 2.0, dns.Double(), 0.001)
	tlsMs, _ := pm.Get("tls_ms")
	require.InDelta(t, 184.0, tlsMs.Double(), 0.001)

	// Sub-millisecond phases must survive as fractions rather than truncating,
	// which is what msFloat exists to guarantee.
	write, _ := pm.Get("write_ms")
	require.InDelta(t, 0.3, write.Double(), 0.001)

	_, hasErr := rec.Body().Map().Get("error")
	require.False(t, hasErr, "successful check must not carry an error block")
}

func TestBuildHTTPLogRecord_FailureIsError(t *testing.T) {
	ts := &targetState{cfg: TargetConfig{Method: MethodHTTP}}
	ts.cfg.Endpoint = "https://127.0.0.1:9"

	r := PingResult{
		Method:        MethodHTTP,
		StatusCode:    0,
		TotalDuration: 5 * time.Millisecond,
		ErrMessage:    "dial tcp 127.0.0.1:9: connect: connection refused",
		ErrPhase:      "connect",
	}

	rec := plog.NewLogRecord()
	buildHTTPLogRecord(rec, ts, r, time.Now(), time.Now(), defaultLogsConfig())

	require.Equal(t, plog.SeverityNumberError, rec.SeverityNumber())

	v, ok := rec.Attributes().Get("error.type")
	require.True(t, ok)
	require.Equal(t, "connect", v.Str())

	errBlock, ok := rec.Body().Map().Get("error")
	require.True(t, ok)
	msg, ok := errBlock.Map().Get("message")
	require.True(t, ok)
	require.Contains(t, msg.Str(), "connection refused",
		"error text is the whole point: status 0 alone cannot distinguish failure modes")
}

func TestBuildHTTPLogRecord_RedactsCredentials(t *testing.T) {
	ts := &targetState{cfg: TargetConfig{Method: MethodHTTP}}
	ts.cfg.Endpoint = "https://admin:hunter2@example.com/health"

	rec := plog.NewLogRecord()
	buildHTTPLogRecord(rec, ts, PingResult{Method: MethodHTTP, StatusCode: 200},
		time.Now(), time.Now(), defaultLogsConfig())

	v, ok := rec.Attributes().Get("server.address")
	require.True(t, ok)
	require.NotContains(t, v.Str(), "hunter2")
	require.Contains(t, v.Str(), "example.com")
}

func TestBuildTracerouteLogRecord(t *testing.T) {
	ts := &targetState{cfg: TargetConfig{}, dnsServer: "8.8.8.8:53"}
	ts.cfg.Endpoint = "www.cloudflare.com"

	tr := TraceResult{
		DestIP:  "104.16.124.96",
		Method:  "udp",
		MaxHops: 30,
		Reached: true,
		Hops: []HopResult{
			{Index: 1, Address: "172.16.1.1", RTT: 443 * time.Microsecond},
			{Index: 2, Address: "10.112.162.67", RTT: 11 * time.Millisecond},
			{Index: 3, Address: unansweredHopAddress, TimedOut: true},
			{Index: 4, Address: "104.16.124.96", RTT: 13 * time.Millisecond},
		},
	}

	rec := plog.NewLogRecord()
	buildTracerouteLogRecord(rec, ts, tr, time.Now(), time.Now(), defaultLogsConfig())

	require.Equal(t, plog.SeverityNumberInfo, rec.SeverityNumber())

	attrs := rec.Attributes()
	hopCount, _ := attrs.Get("traceroute.hop_count")
	require.EqualValues(t, 4, hopCount.Int())
	answered, _ := attrs.Get("traceroute.hops_answered")
	require.EqualValues(t, 3, answered.Int())
	reached, _ := attrs.Get("traceroute.reached_dest")
	require.True(t, reached.Bool())

	hops, ok := rec.Body().Map().Get("hops")
	require.True(t, ok)
	require.Equal(t, 4, hops.Slice().Len(), "unanswered hops must still appear in the path")

	// Hop 1 is sub-millisecond — the exact case that used to truncate to zero.
	h0 := hops.Slice().At(0).Map()
	rtt, ok := h0.Get("rtt_ms")
	require.True(t, ok)
	require.InDelta(t, 0.443, rtt.Double(), 0.001)

	// The timed-out hop carries no latency: the only duration available for it
	// is the timeout we chose, which is not a measurement.
	h2 := hops.Slice().At(2).Map()
	timedOut, _ := h2.Get("timed_out")
	require.True(t, timedOut.Bool())
	_, hasRTT := h2.Get("rtt_ms")
	require.False(t, hasRTT)
	addr, _ := h2.Get("address")
	require.Equal(t, unansweredHopAddress, addr.Str())
}

func TestBuildTracerouteLogRecord_IncompleteIsWarn(t *testing.T) {
	ts := &targetState{cfg: TargetConfig{}}
	ts.cfg.Endpoint = "blackhole.example"

	tr := TraceResult{
		Method:       "udp",
		MaxHops:      30,
		Reached:      false,
		AbortedEarly: true,
		Hops: []HopResult{
			{Index: 1, Address: "172.16.1.1", RTT: time.Millisecond},
			{Index: 2, Address: unansweredHopAddress, TimedOut: true},
		},
	}

	rec := plog.NewLogRecord()
	buildTracerouteLogRecord(rec, ts, tr, time.Now(), time.Now(), defaultLogsConfig())

	require.Equal(t, plog.SeverityNumberWarn, rec.SeverityNumber())
	aborted, _ := rec.Attributes().Get("traceroute.aborted_early")
	require.True(t, aborted.Bool(),
		"an early bail-out must be distinguishable from a genuinely short path")
}

func TestTLSDetailsOmittedWhenDisabled(t *testing.T) {
	ts := &targetState{cfg: TargetConfig{Method: MethodHTTP}}
	ts.cfg.Endpoint = "https://example.com"

	r := PingResult{
		Method:     MethodHTTP,
		StatusCode: 200,
		TLS:        &TLSDetails{Version: "TLS 1.3", CertIssuer: "CN=Test CA", CertDaysLeft: 10},
	}

	cfg := defaultLogsConfig()
	cfg.IncludeTLSDetails = false

	rec := plog.NewLogRecord()
	buildHTTPLogRecord(rec, ts, r, time.Now(), time.Now(), cfg)

	_, ok := rec.Body().Map().Get("tls")
	require.False(t, ok)

	// The summary attribute stays regardless, since cert expiry is the field
	// most likely to be alerted on.
	_, ok = rec.Attributes().Get("tls.cert.days_remaining")
	require.True(t, ok)
}

func TestNoHeadersOrBodyEverRecorded(t *testing.T) {
	ts := &targetState{cfg: TargetConfig{Method: MethodHTTP}}
	ts.cfg.Endpoint = "https://example.com"

	rec := plog.NewLogRecord()
	buildHTTPLogRecord(rec, ts, PingResult{Method: MethodHTTP, StatusCode: 200, ResponseSize: 42},
		time.Now(), time.Now(), defaultLogsConfig())

	// Guards the one part of this feature that can cause a security incident.
	banned := []string{"header", "cookie", "authorization", "body"}
	for k := range rec.Attributes().All() {
		for _, b := range banned {
			require.NotContains(t, strings.ToLower(k), b)
		}
	}
	for k := range rec.Body().Map().All() {
		for _, b := range banned {
			require.NotContains(t, strings.ToLower(k), b)
		}
	}
}

func TestFailurePhase(t *testing.T) {
	now := time.Now()
	refused := errors.New("connect: connection refused")

	cases := []struct {
		name string
		in   phaseTimings
		want string
	}{
		{
			// ConnectDone fires even when the connection is refused, so a
			// timestamp-only heuristic blames "request" — the phase after the
			// one that actually failed.
			name: "refused connection reports connect, not request",
			in:   phaseTimings{connectDone: now, connectErr: refused},
			want: "connect",
		},
		{
			name: "dns failure",
			in:   phaseTimings{dnsStart: now, dnsDone: now, dnsErr: errors.New("no such host")},
			want: "dns",
		},
		{
			name: "tls failure",
			in: phaseTimings{
				dnsStart: now, dnsDone: now, connectDone: now,
				tlsStart: now, tlsDone: now, tlsErr: errors.New("bad certificate"),
			},
			want: "tls",
		},
		{
			name: "timeout waiting for response",
			in:   phaseTimings{dnsStart: now, dnsDone: now, connectDone: now, wroteRequest: now},
			want: "response",
		},
		{
			name: "hung mid-connect with no error reported",
			in:   phaseTimings{dnsStart: now, dnsDone: now},
			want: "connect",
		},
		{
			name: "nothing started",
			in:   phaseTimings{},
			want: "setup",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, failurePhase(tc.in))
		})
	}
}

// The record builders redacted the endpoint they embed, but the resource
// attribute carried the raw one — which put the credential straight back into
// every emitted record, one level up.
func TestResourceEndpointIsRedacted(t *testing.T) {
	raw := "https://admin:hunter2@example.com/health"
	require.NotContains(t, redactEndpoint(raw), "hunter2")

	// Guard the scraper call sites too, so the resource attribute cannot drift
	// back to the raw value.
	for _, src := range []string{"scraper.go", "logs_scraper.go"} {
		b, err := os.ReadFile(src)
		require.NoError(t, err)
		require.NotContains(t, string(b), "SetTargetEndpoint(ts.cfg.Endpoint)",
			"%s must not pass an unredacted endpoint to the resource builder", src)
	}
}

func TestFailurePhase_IPLiteralDialFailure(t *testing.T) {
	now := time.Now()
	// An IP-literal target runs no DNS lookup, so dnsDone stays zero. Before
	// ConnectStart was tracked, a dial that hung was reported as "setup".
	got := failurePhase(phaseTimings{connectStart: now})
	require.Equal(t, "connect", got)
}

// redactEndpoint assumes a URL. Applied to a free-form error message it
// truncated everything before the last "@", discarding the failure detail the
// message exists to carry.
func TestRedactMessagePreservesText(t *testing.T) {
	cases := []struct{ name, in, wantContains, wantAbsent string }{
		{
			name:         "credential in embedded url is stripped",
			in:           `Head "https://admin:hunter2@example.com": dial tcp: connection refused`,
			wantContains: "connection refused",
			wantAbsent:   "hunter2",
		},
		{
			name:         "unrelated at-sign does not truncate the message",
			in:           "lookup user@host failed: no such host",
			wantContains: "no such host",
			wantAbsent:   "",
		},
		{
			name:         "plain message is untouched",
			in:           "context deadline exceeded",
			wantContains: "context deadline exceeded",
			wantAbsent:   "",
		},
		{
			// The match must stop at the URL authority: an "@" in a query
			// string would otherwise swallow the host on the way to it.
			name:         "at-sign in a query string does not eat the host",
			in:           "GET https://api.example.test?email=user@example.test failed",
			wantContains: "api.example.test",
			wantAbsent:   "",
		},
		{
			name:         "at-sign in a fragment does not eat the host",
			in:           "https://host.example#frag@anchor unreachable",
			wantContains: "host.example",
			wantAbsent:   "",
		},
		{
			name:         "credential still stripped when a query follows",
			in:           `Get "https://admin:hunter2@example.com/health?verbose=1": timeout`,
			wantContains: "verbose=1",
			wantAbsent:   "hunter2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactMessage(tc.in)
			require.Contains(t, got, tc.wantContains)
			if tc.wantAbsent != "" {
				require.NotContains(t, got, tc.wantAbsent)
			}
		})
	}
}
