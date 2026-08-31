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

package networkcheckreceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver"

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

// redactEndpoint strips any userinfo from a target endpoint. A target may be
// configured as https://user:pass@host, and the endpoint reaches log records,
// resource attributes, and error messages. Credentials must not follow it there.
func redactEndpoint(endpoint string) string {
	if !strings.Contains(endpoint, "@") {
		return endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.User == nil {
		// Unparseable but contains "@": drop everything up to the last one
		// rather than risk emitting a credential.
		if i := strings.LastIndex(endpoint, "@"); i >= 0 {
			if scheme := strings.Index(endpoint, "://"); scheme >= 0 && scheme < i {
				return endpoint[:scheme+3] + endpoint[i+1:]
			}
			return endpoint[i+1:]
		}
		return endpoint
	}
	return u.Redacted()
}

// userinfoInURL matches the userinfo segment of a URL embedded in free text.
var userinfoInURL = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/@\s"]+@`)

// redactMessage strips credentials from any URL embedded in free-form text.
//
// redactEndpoint must not be used for this: it assumes its input is a URL, so a
// message containing an unrelated "@" would be truncated to whatever followed
// it, discarding the failure detail the message exists to carry.
func redactMessage(msg string) string {
	return userinfoInURL.ReplaceAllString(msg, "$1")
}

// redactErr strips credentials from an error's text.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(redactMessage(err.Error()))
}

// buildHTTPLogRecord renders one HTTP probe as a log record. The record is the
// transaction: phases sit together in the body so a slow check can be
// attributed to a phase, which averaged metric series cannot express.
func buildHTTPLogRecord(lr plog.LogRecord, ts *targetState, r PingResult, startedAt time.Time, observed time.Time, cfg LogsConfig) {
	lr.SetTimestamp(pcommon.NewTimestampFromTime(startedAt))
	lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(observed))

	endpoint := ts.cfg.Endpoint
	if cfg.RedactURLUserinfo {
		endpoint = redactEndpoint(endpoint)
	}

	failed := r.StatusCode == 0 || r.ErrMessage != ""
	if failed {
		lr.SetSeverityNumber(plog.SeverityNumberError)
		lr.SetSeverityText("ERROR")
	} else {
		lr.SetSeverityNumber(plog.SeverityNumberInfo)
		lr.SetSeverityText("INFO")
	}

	attrs := lr.Attributes()
	attrs.PutStr("server.address", endpoint)
	attrs.PutStr("http.request.method", methodOrDefault(ts.cfg.HTTPMethod))
	if r.StatusCode != 0 {
		attrs.PutInt("http.response.status_code", int64(r.StatusCode))
	}
	if r.ResponseSize > 0 {
		attrs.PutInt("http.response.size", r.ResponseSize)
	}
	if r.Protocol != "" {
		attrs.PutStr("network.protocol.version", r.Protocol)
	}
	if r.ResolvedIP != "" {
		attrs.PutStr("server.resolved_ip", r.ResolvedIP)
	}
	if ts.dnsServer != "" {
		attrs.PutStr("dns.server", ts.dnsServer)
	}
	if r.TLS != nil {
		attrs.PutDouble("tls.cert.days_remaining", r.TLS.CertDaysLeft)
	}
	if failed && r.ErrPhase != "" {
		attrs.PutStr("error.type", r.ErrPhase)
	}

	body := lr.Body().SetEmptyMap()

	// Phase semantics follow the existing metric definitions exactly:
	// connect_ms is measured from DNS-done rather than from ConnectStart,
	// write_ms spans the TLS handshake, and ttfb_ms is time to first byte
	// rather than a full body read.
	phases := body.PutEmptyMap("phases")
	phases.PutDouble("dns_ms", msFloat(r.DNSLookup))
	phases.PutDouble("connect_ms", msFloat(r.TCPConnect))
	phases.PutDouble("tls_ms", msFloat(r.TLSHandshake))
	phases.PutDouble("write_ms", msFloat(r.RequestWrite))
	phases.PutDouble("ttfb_ms", msFloat(r.ResponseRead))
	phases.PutDouble("total_ms", msFloat(r.TotalDuration))

	if r.TLS != nil && cfg.IncludeTLSDetails {
		t := body.PutEmptyMap("tls")
		t.PutStr("version", r.TLS.Version)
		t.PutStr("cipher", r.TLS.CipherSuite)
		if r.TLS.NegotiatedProtocol != "" {
			t.PutStr("negotiated_protocol", r.TLS.NegotiatedProtocol)
		}
		if !r.TLS.CertNotAfter.IsZero() {
			cert := t.PutEmptyMap("cert")
			cert.PutStr("issuer", r.TLS.CertIssuer)
			cert.PutStr("subject", r.TLS.CertSubject)
			cert.PutStr("not_after", r.TLS.CertNotAfter.UTC().Format(time.RFC3339))
			cert.PutDouble("days_remaining", r.TLS.CertDaysLeft)
		}
	}

	if failed {
		e := body.PutEmptyMap("error")
		if r.ErrPhase != "" {
			e.PutStr("phase", r.ErrPhase)
		}
		if r.ErrMessage != "" {
			e.PutStr("message", redactMessage(r.ErrMessage))
		}
	}
}

// buildTracerouteLogRecord renders one traceroute as a single record. The path
// is the unit of meaning, so hops stay together and ordered — including hops
// that never answered, which as metrics can only be represented by a separate
// status series.
func buildTracerouteLogRecord(lr plog.LogRecord, ts *targetState, tr TraceResult, startedAt time.Time, observed time.Time, cfg LogsConfig) {
	lr.SetTimestamp(pcommon.NewTimestampFromTime(startedAt))
	lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(observed))

	endpoint := ts.cfg.Endpoint
	if cfg.RedactURLUserinfo {
		endpoint = redactEndpoint(endpoint)
	}

	answered, retried := 0, 0
	for _, h := range tr.Hops {
		if !h.TimedOut {
			answered++
		}
		if h.Probes > 1 {
			retried++
		}
	}

	// A trace that reached its destination is routine. One that gave up early,
	// or walked to the TTL ceiling without arriving, is a path problem worth
	// surfacing without making it an error.
	if tr.Reached {
		lr.SetSeverityNumber(plog.SeverityNumberInfo)
		lr.SetSeverityText("INFO")
	} else {
		lr.SetSeverityNumber(plog.SeverityNumberWarn)
		lr.SetSeverityText("WARN")
	}

	attrs := lr.Attributes()
	attrs.PutStr("server.address", endpoint)
	if tr.DestIP != "" {
		attrs.PutStr("server.resolved_ip", tr.DestIP)
	}
	if tr.Method != "" {
		attrs.PutStr("traceroute.method", tr.Method)
	}
	attrs.PutInt("traceroute.hop_count", int64(len(tr.Hops)))
	attrs.PutInt("traceroute.hops_answered", int64(answered))
	if retried > 0 {
		attrs.PutInt("traceroute.hops_retried", int64(retried))
	}
	attrs.PutBool("traceroute.reached_dest", tr.Reached)
	attrs.PutBool("traceroute.aborted_early", tr.AbortedEarly)
	if ts.dnsServer != "" {
		attrs.PutStr("dns.server", ts.dnsServer)
	}

	body := lr.Body().SetEmptyMap()
	hops := body.PutEmptySlice("hops")
	for _, h := range tr.Hops {
		m := hops.AppendEmpty().SetEmptyMap()
		m.PutInt("index", int64(h.Index))
		m.PutStr("address", h.Address)
		m.PutBool("timed_out", h.TimedOut)
		// Probes above 1 means earlier attempts went unanswered, which
		// separates a hop that is merely rate-limiting from a healthy one.
		if h.Probes > 0 {
			m.PutInt("probes", int64(h.Probes))
		}
		// A hop that did not answer has no latency, only the timeout we chose.
		// Emitting that as an rtt would read as a real, very slow measurement.
		if !h.TimedOut {
			m.PutDouble("rtt_ms", msFloat(h.RTT))
		}
	}
}

func methodOrDefault(m string) string {
	if m == "" {
		return "HEAD"
	}
	return m
}
