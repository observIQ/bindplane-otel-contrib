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

	// Method is the actual probe method used (may differ from config after fallback).
	Method string
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
		dnsStart, dnsDone    time.Time
		connectDone          time.Time
		tlsStart, tlsDone    time.Time
		wroteRequest         time.Time
		gotFirstResponseByte time.Time
		requestStart         time.Time
	)

	trace := &httptrace.ClientTrace{
		DNSStart:             func(_ httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(_ httptrace.DNSDoneInfo) { dnsDone = time.Now() },
		ConnectDone:          func(_, _ string, _ error) { connectDone = time.Now() },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(_ tls.ConnectionState, _ error) { tlsDone = time.Now() },
		WroteRequest:         func(_ httptrace.WroteRequestInfo) { wroteRequest = time.Now() },
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
	if resp != nil {
		statusCode = resp.StatusCode
		// Drain and close body so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	if err != nil {
		// Record a failed probe but don't return an error — it's a valid measurement.
		return PingResult{
			TotalDuration: end.Sub(requestStart),
			StatusCode:    0,
			Method:        MethodHTTP,
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

	return PingResult{
		DNSLookup:     dnsLookup,
		TCPConnect:    tcpConnect,
		TLSHandshake:  tlsHandshake,
		RequestWrite:  reqWrite,
		ResponseRead:  respRead,
		TotalDuration: end.Sub(requestStart),
		StatusCode:    statusCode,
		Method:        MethodHTTP,
	}, nil
}
