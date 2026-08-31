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
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"golang.org/x/net/icmp"
)

// PingResult holds the outcome of a single probe cycle.
type PingResult struct {
	// ICMP fields (populated when Method == "icmp")
	MinRTT     time.Duration
	AvgRTT     time.Duration
	MaxRTT     time.Duration
	PacketLoss float64 // 0.0–1.0

	// HTTP timing fields (populated when Method == "http")
	DNSLookup     time.Duration
	TCPConnect    time.Duration
	TLSHandshake  time.Duration
	RequestWrite  time.Duration
	ResponseRead  time.Duration
	TotalDuration time.Duration
	StatusCode    int

	// DNS fields (populated when Method == "dns")
	QueryDuration time.Duration
	QuerySuccess  bool
	QueryName     string

	// Method is the actual probe method used (may differ from config after fallback).
	Method string

	// --- Fields below are captured for log records only. Metric recording does
	// --- not read them, so adding to this block cannot change metric output.

	// ResolvedIP is the address the target resolved to for this probe. A
	// hostname behind a CDN or round-robin DNS resolves differently over time,
	// which is invisible in the timing numbers alone.
	ResolvedIP string

	// ResponseSize is the number of body bytes read from an HTTP response.
	ResponseSize int64

	// Protocol is the negotiated HTTP version, e.g. "HTTP/1.1" or "HTTP/2.0".
	Protocol string

	// ErrMessage and ErrPhase describe a failed probe. Without them a failed
	// HTTP check is indistinguishable between DNS failure, connection refused,
	// and timeout, since all three surface only as status code 0.
	ErrMessage string
	ErrPhase   string

	// TLS carries certificate and handshake detail, nil for non-TLS probes.
	TLS *TLSDetails
}

// TLSDetails is the certificate and handshake state observed during an HTTPS
// probe. The handshake already computes all of this; it was previously
// discarded.
type TLSDetails struct {
	Version            string
	CipherSuite        string
	NegotiatedProtocol string

	CertIssuer   string
	CertSubject  string
	CertNotAfter time.Time
	CertDaysLeft float64
}

// pinger is the interface implemented by icmpPinger and httpPinger.
type pinger interface {
	ping(ctx context.Context) (PingResult, error)
}

// icmpPinger sends ICMP echo requests and collects RTT statistics.
type icmpPinger struct {
	host       string
	count      int
	timeout    time.Duration
	dnsServer  string
	privileged bool // true = raw ICMP (root), false = datagram ICMP (macOS unprivileged)
}

func newICMPPinger(target TargetConfig, privileged bool) *icmpPinger {
	count := target.PingCount
	if count <= 0 {
		count = 3
	}
	timeout := target.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &icmpPinger{
		host:       target.Endpoint,
		count:      count,
		timeout:    timeout,
		dnsServer:  target.DNSServer,
		privileged: privileged,
	}
}

// checkICMPMode returns whether ICMP probing is available and whether raw
// (privileged) mode is needed. On macOS without root, datagram ICMP works
// without special privileges.
func checkICMPMode() (available bool, privileged bool) {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err == nil {
		_ = conn.Close()
		return true, true
	}
	// Raw ICMP unavailable — try datagram (unprivileged) mode via pro-bing.
	p, err := probing.NewPinger("127.0.0.1")
	if err != nil {
		return false, false
	}
	p.SetPrivileged(false)
	p.Count = 1
	p.Timeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.RunWithContext(ctx) }()
	select {
	case runErr := <-done:
		return runErr == nil, false
	case <-ctx.Done():
		p.Stop()
		return false, false
	}
}

func (p *icmpPinger) ping(ctx context.Context) (PingResult, error) {
	pinger, err := probing.NewPinger(p.host)
	if err != nil {
		return PingResult{}, fmt.Errorf("creating pinger for %s: %w", p.host, err)
	}
	pinger.SetPrivileged(p.privileged)
	pinger.Count = p.count
	pinger.Timeout = p.timeout * time.Duration(p.count)

	// Run in a goroutine so we can respect context cancellation.
	done := make(chan error, 1)
	go func() {
		done <- pinger.RunWithContext(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			return PingResult{}, fmt.Errorf("pinging %s: %w", p.host, err)
		}
	case <-ctx.Done():
		pinger.Stop()
		return PingResult{}, ctx.Err()
	}

	stats := pinger.Statistics()
	loss := 1.0
	if stats.PacketsSent > 0 {
		loss = float64(stats.PacketsSent-stats.PacketsRecv) / float64(stats.PacketsSent)
	}

	return PingResult{
		MinRTT:     stats.MinRtt,
		AvgRTT:     stats.AvgRtt,
		MaxRTT:     stats.MaxRtt,
		PacketLoss: loss,
		Method:     MethodICMP,
	}, nil
}

// dnsPinger sends a DNS query to a specific server and measures response time.
type dnsPinger struct {
	server     string // DNS server address with port, e.g. "8.8.8.8:53"
	query      string // hostname to resolve
	recordType string // "A", "AAAA", "CNAME", "MX", "TXT"
	timeout    time.Duration
}

func newDNSPinger(target TargetConfig) *dnsPinger {
	timeout := target.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	server := target.Endpoint
	if !strings.Contains(server, ":") {
		server = server + ":53"
	}
	recordType := strings.ToUpper(target.DNSRecordType)
	if recordType == "" {
		recordType = "A"
	}
	return &dnsPinger{
		server:     server,
		query:      target.DNSQuery,
		recordType: recordType,
		timeout:    timeout,
	}
}

func (p *dnsPinger) ping(ctx context.Context) (PingResult, error) {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: p.timeout}
			return d.DialContext(dialCtx, "udp", p.server)
		},
	}

	queryCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	start := time.Now()
	var lookupErr error
	switch p.recordType {
	case "CNAME":
		_, lookupErr = resolver.LookupCNAME(queryCtx, p.query)
	case "MX":
		_, lookupErr = resolver.LookupMX(queryCtx, p.query)
	case "TXT":
		_, lookupErr = resolver.LookupTXT(queryCtx, p.query)
	default: // A, AAAA
		_, lookupErr = resolver.LookupHost(queryCtx, p.query)
	}
	duration := time.Since(start)

	return PingResult{
		QueryDuration: duration,
		QuerySuccess:  lookupErr == nil,
		QueryName:     p.query,
		Method:        MethodDNS,
	}, nil
}

// httpPinger performs a single HTTP request and records per-phase timings using httptrace.
type httpPinger struct {
	client     *http.Client
	url        string
	httpMethod string
	dnsServer  string
}

func newHTTPPinger(target TargetConfig, dnsServer string) (*httpPinger, error) {
	httpMethod := target.HTTPMethod
	if httpMethod == "" {
		httpMethod = http.MethodHead
	}

	// Build a custom dialer that uses the specified DNS server if provided.
	dialServer := dnsServer
	if target.DNSServer != "" {
		dialServer = target.DNSServer
	}

	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	if dialServer != "" {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{}
				addr := dialServer
				if !strings.Contains(addr, ":") {
					addr = addr + ":53"
				}
				return d.DialContext(ctx, "udp", addr)
			},
		}
		dialer.Resolver = resolver
	}

	timeout := target.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true, // fresh connection each probe for accurate timing
	}

	// Apply TLS config from ClientConfig if specified.
	tlsCfg, err := target.TLS.LoadTLSConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("loading TLS config for %s: %w", target.Endpoint, err)
	}
	if tlsCfg != nil {
		transport.TLSClientConfig = tlsCfg
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// Do not follow redirects; we measure the first response.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &httpPinger{
		client:     client,
		url:        target.Endpoint,
		httpMethod: httpMethod,
		dnsServer:  dialServer,
	}, nil
}

func (p *httpPinger) ping(ctx context.Context) (PingResult, error) {
	var (
		dnsStart, dnsDone         time.Time
		connectStart, connectDone time.Time
		tlsStart, tlsDone         time.Time
		wroteRequest              time.Time
		gotFirstResponseByte      time.Time
		requestStart              time.Time
	)

	var (
		resolvedIP string
		tlsState   tls.ConnectionState
		haveTLS    bool

		// Each of these hooks fires on failure as well as success, so the
		// timestamps alone cannot say which phase broke. The errors can.
		dnsErr, connectErr, tlsErr, writeErr error
	)

	trace := &httptrace.ClientTrace{
		DNSStart: func(_ httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			dnsDone = time.Now()
			dnsErr = info.Err
			if len(info.Addrs) > 0 {
				resolvedIP = info.Addrs[0].IP.String()
			}
		},
		ConnectStart: func(_, _ string) { connectStart = time.Now() },
		ConnectDone: func(_, addr string, err error) {
			connectDone = time.Now()
			connectErr = err
			// Authoritative even when the address was already cached and no DNS
			// lookup ran, so prefer it over the DNSDone value.
			if host, _, splitErr := net.SplitHostPort(addr); splitErr == nil {
				resolvedIP = host
			}
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			tlsDone = time.Now()
			tlsErr = err
			if err == nil {
				tlsState, haveTLS = cs, true
			}
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			wroteRequest = time.Now()
			writeErr = info.Err
		},
		GotFirstResponseByte: func() { gotFirstResponseByte = time.Now() },
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), p.httpMethod, p.url, nil)
	if err != nil {
		return PingResult{}, fmt.Errorf("building request for %s: %w", p.url, err)
	}

	requestStart = time.Now()
	resp, err := p.client.Do(req)
	end := time.Now()

	statusCode := 0
	var responseSize int64
	var protocol string
	if resp != nil {
		statusCode = resp.StatusCode
		protocol = resp.Proto
		// Drain and close body so the connection can be reused. The byte count
		// is the response size; only the timing was kept before.
		responseSize, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	if err != nil {
		// Record a failed probe but don't return an error — it's a valid measurement.
		return PingResult{
			TotalDuration: end.Sub(requestStart),
			StatusCode:    0,
			Method:        MethodHTTP,
			ResolvedIP:    resolvedIP,
			ErrMessage:    err.Error(),
			ErrPhase: failurePhase(phaseTimings{
				dnsStart: dnsStart, dnsDone: dnsDone,
				connectStart: connectStart, connectDone: connectDone,
				tlsStart: tlsStart, tlsDone: tlsDone,
				wroteRequest: wroteRequest, gotFirstByte: gotFirstResponseByte,
				dnsErr: dnsErr, connectErr: connectErr, tlsErr: tlsErr, writeErr: writeErr,
			}),
		}, nil
	}

	var (
		dnsLookup    time.Duration
		tcpConnect   time.Duration
		tlsHandshake time.Duration
		reqWrite     time.Duration
		respRead     time.Duration
	)
	if !dnsStart.IsZero() && !dnsDone.IsZero() {
		dnsLookup = dnsDone.Sub(dnsStart)
	}
	if !dnsDone.IsZero() && !connectDone.IsZero() {
		tcpConnect = connectDone.Sub(dnsDone)
	}
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		tlsHandshake = tlsDone.Sub(tlsStart)
	}
	if !connectDone.IsZero() && !wroteRequest.IsZero() {
		reqWrite = wroteRequest.Sub(connectDone)
	}
	if !wroteRequest.IsZero() && !gotFirstResponseByte.IsZero() {
		respRead = gotFirstResponseByte.Sub(wroteRequest)
	}

	res := PingResult{
		DNSLookup:     dnsLookup,
		TCPConnect:    tcpConnect,
		TLSHandshake:  tlsHandshake,
		RequestWrite:  reqWrite,
		ResponseRead:  respRead,
		TotalDuration: end.Sub(requestStart),
		StatusCode:    statusCode,
		Method:        MethodHTTP,
		ResolvedIP:    resolvedIP,
		ResponseSize:  responseSize,
		Protocol:      protocol,
	}
	if haveTLS {
		res.TLS = tlsDetailsFrom(tlsState, end)
	}
	return res, nil
}

// phaseTimings carries what the trace hooks observed about one request.
type phaseTimings struct {
	dnsStart, dnsDone                    time.Time
	connectStart, connectDone            time.Time
	tlsStart, tlsDone                    time.Time
	wroteRequest, gotFirstByte           time.Time
	dnsErr, connectErr, tlsErr, writeErr error
}

// failurePhase names the request phase that broke. A bare status code of 0 says
// a check failed but not where, which is the difference between a DNS problem
// and a slow origin.
//
// Reported errors are checked before timestamps because every hook fires on
// failure as well as success: a refused connection still calls ConnectDone, so
// timing alone would blame the phase after the one that actually failed.
func failurePhase(t phaseTimings) string {
	switch {
	case t.dnsErr != nil:
		return "dns"
	case t.connectErr != nil:
		return "connect"
	case t.tlsErr != nil:
		return "tls"
	case t.writeErr != nil:
		return "request"

	// No hook reported an error, so fall back to the first phase that started
	// and never finished.
	case !t.dnsStart.IsZero() && t.dnsDone.IsZero():
		return "dns"
	case !t.connectStart.IsZero() && t.connectDone.IsZero():
		// Covers an IP-literal endpoint, where no DNS lookup runs and the dial
		// is the first thing to happen.
		return "connect"
	case !t.dnsDone.IsZero() && t.connectDone.IsZero():
		return "connect"
	case !t.tlsStart.IsZero() && t.tlsDone.IsZero():
		return "tls"
	case !t.connectDone.IsZero() && t.wroteRequest.IsZero():
		return "request"
	case !t.wroteRequest.IsZero() && t.gotFirstByte.IsZero():
		return "response"
	case t.dnsStart.IsZero() && t.connectDone.IsZero():
		// Nothing started: usually an invalid URL or a proxy rejection.
		return "setup"
	default:
		return "unknown"
	}
}

// tlsDetailsFrom extracts certificate and handshake detail from a completed
// handshake. DisableKeepAlives means every probe performs a real handshake, so
// this is always current rather than cached from an earlier connection.
func tlsDetailsFrom(cs tls.ConnectionState, now time.Time) *TLSDetails {
	d := &TLSDetails{
		Version:            tls.VersionName(cs.Version),
		CipherSuite:        tls.CipherSuiteName(cs.CipherSuite),
		NegotiatedProtocol: cs.NegotiatedProtocol,
	}
	if len(cs.PeerCertificates) > 0 {
		leaf := cs.PeerCertificates[0]
		d.CertIssuer = leaf.Issuer.String()
		d.CertSubject = leaf.Subject.String()
		d.CertNotAfter = leaf.NotAfter
		d.CertDaysLeft = leaf.NotAfter.Sub(now).Hours() / 24
	}
	return d
}
