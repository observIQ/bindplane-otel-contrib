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
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// HopResult is the latency measurement for a single traceroute hop.
type HopResult struct {
	Index   int
	Address string
	RTT     time.Duration
}

// tracerouter performs traceroute probes for a single host.
type tracerouter struct {
	cfg  TracerouteConfig
	host string
}

func newTracerouter(cfg TracerouteConfig, host string) *tracerouter {
	return &tracerouter{cfg: cfg, host: host}
}

// shouldRun returns true if a traceroute should be performed given the current
// check count and the most recent ping result for the target.
func (t *tracerouter) shouldRun(checkCount int, result PingResult) bool {
	if !t.cfg.Enabled {
		return false
	}
	if t.cfg.Interval > 0 && checkCount%t.cfg.Interval == 0 {
		return true
	}
	if t.cfg.OnFailure && result.Method == MethodICMP && result.PacketLoss >= t.cfg.FailureThreshold {
		return true
	}
	return false
}

// trace performs a traceroute to t.host and returns per-hop results.
// It uses UDP probes by default (method "udp") or ICMP echo probes (method "icmp").
// UDP traceroute does not require root on most Linux kernels.
// ICMP traceroute requires root / CAP_NET_RAW.
func (t *tracerouter) trace(ctx context.Context) ([]HopResult, error) {
	method := strings.ToLower(t.cfg.Method)
	if method == "" {
		method = "udp"
	}

	// Resolve target to an IP address.
	addrs, err := net.LookupHost(t.host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("resolving %s: %w", t.host, err)
	}
	dest := addrs[0]

	switch method {
	case "icmp":
		return t.traceICMP(ctx, dest)
	default:
		return t.traceUDP(ctx, dest)
	}
}

// traceUDP sends UDP packets with incrementing TTL and listens for ICMP
// time-exceeded responses to map the path.
func (t *tracerouter) traceUDP(ctx context.Context, dest string) ([]HopResult, error) {
	destAddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(dest, "33434"))
	if err != nil {
		return nil, fmt.Errorf("resolving UDP dest: %w", err)
	}

	// Open raw ICMP socket to receive time-exceeded responses.
	icmpConn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("opening ICMP listener for traceroute: %w", err)
	}
	defer icmpConn.Close()

	var hops []HopResult
	maxHops := t.cfg.MaxHops
	if maxHops <= 0 {
		maxHops = 30
	}

	for ttl := 1; ttl <= maxHops; ttl++ {
		if ctx.Err() != nil {
			break
		}

		// Send a UDP packet with the given TTL.
		udpConn, err := net.DialUDP("udp4", nil, destAddr)
		if err != nil {
			return hops, fmt.Errorf("dialing UDP: %w", err)
		}
		ipConn := ipv4.NewConn(udpConn)
		if err := ipConn.SetTTL(ttl); err != nil {
			_ = udpConn.Close()
			return hops, fmt.Errorf("setting TTL %d: %w", ttl, err)
		}

		sent := time.Now()
		if _, err := udpConn.Write([]byte("ping")); err != nil {
			_ = udpConn.Close()
			return hops, fmt.Errorf("sending UDP probe: %w", err)
		}
		_ = udpConn.Close()

		// Wait for ICMP time-exceeded.
		hopTimeout := t.cfg.Timeout
		if hopTimeout == 0 {
			hopTimeout = 3 * time.Second
		}
		if err := icmpConn.SetReadDeadline(time.Now().Add(hopTimeout)); err != nil {
			return hops, fmt.Errorf("setting read deadline: %w", err)
		}

		buf := make([]byte, 512)
		_, from, err := icmpConn.ReadFrom(buf)
		rtt := time.Since(sent)

		hopAddr := "*"
		if err == nil && from != nil {
			hopAddr = from.String()
		}

		hops = append(hops, HopResult{
			Index:   ttl,
			Address: hopAddr,
			RTT:     rtt,
		})

		// Stop when we reach the destination.
		if err == nil && from != nil {
			fromHost, _, _ := net.SplitHostPort(from.String())
			if fromHost == dest || from.String() == dest {
				break
			}
		}
	}

	return hops, nil
}

// traceICMP sends ICMP echo requests with incrementing TTL values and collects
// ICMP time-exceeded responses. Requires root / CAP_NET_RAW.
func (t *tracerouter) traceICMP(ctx context.Context, dest string) ([]HopResult, error) {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("opening raw ICMP socket for traceroute: %w", err)
	}
	defer conn.Close()

	destAddr, err := net.ResolveIPAddr("ip4", dest)
	if err != nil {
		return nil, fmt.Errorf("resolving ICMP dest: %w", err)
	}

	var hops []HopResult
	maxHops := t.cfg.MaxHops
	if maxHops <= 0 {
		maxHops = 30
	}
	hopTimeout := t.cfg.Timeout
	if hopTimeout == 0 {
		hopTimeout = 3 * time.Second
	}

	for ttl := 1; ttl <= maxHops; ttl++ {
		if ctx.Err() != nil {
			break
		}

		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 0,
			Body: &icmp.Echo{ID: ttl, Seq: ttl, Data: []byte("netstat")},
		}
		wb, err := msg.Marshal(nil)
		if err != nil {
			return hops, fmt.Errorf("marshaling ICMP echo: %w", err)
		}

		if err := ipv4.NewPacketConn(conn).SetTTL(ttl); err != nil {
			return hops, fmt.Errorf("setting ICMP TTL %d: %w", ttl, err)
		}

		sent := time.Now()
		if _, err := conn.WriteTo(wb, destAddr); err != nil {
			return hops, fmt.Errorf("sending ICMP probe: %w", err)
		}

		if err := conn.SetReadDeadline(time.Now().Add(hopTimeout)); err != nil {
			return hops, fmt.Errorf("setting read deadline: %w", err)
		}

		rb := make([]byte, 512)
		_, from, err := conn.ReadFrom(rb)
		rtt := time.Since(sent)

		hopAddr := "*"
		if err == nil && from != nil {
			hopAddr = from.String()
		}

		hops = append(hops, HopResult{
			Index:   ttl,
			Address: hopAddr,
			RTT:     rtt,
		})

		if err == nil && from != nil && from.String() == destAddr.String() {
			break
		}
	}

	return hops, nil
}
