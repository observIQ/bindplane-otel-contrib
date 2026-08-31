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
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// unansweredHopAddress is the address reported for a hop that did not answer
// within the probe timeout.
const unansweredHopAddress = "*"

// maxConsecutiveTimeouts bounds how many unanswered hops in a row we tolerate
// before abandoning the trace. Without this a path that never answers (a
// firewall silently dropping probes, for example) walks the full max_hops
// range at the per-hop timeout, which on the defaults is 30 * 3s = 90s inside
// a single scrape.
const maxConsecutiveTimeouts = 5

// icmpProtocolIPv4 is the IANA protocol number for ICMP, required by
// icmp.ParseMessage to interpret an IPv4 ICMP message.
const icmpProtocolIPv4 = 1

// errProbeTimeout is returned when no reply matching the probe arrived before
// the hop deadline.
var errProbeTimeout = errors.New("no matching ICMP reply before deadline")

// probeKey identifies the probe that a reply must correspond to. A raw ICMP
// socket receives every ICMP packet the host sees, so a reply has to be matched
// back to the probe that provoked it rather than assumed to belong to whichever
// TTL is currently in flight.
type probeKey struct {
	// udp is true when the probe was a UDP datagram, false for an ICMP echo.
	udp bool

	srcPort int
	dstPort int

	echoID  int
	echoSeq int
}

// matchesProbe reports whether the original datagram quoted inside an ICMP
// error refers to the probe described by k. ICMP errors echo back the offending
// IP header plus at least its first 8 payload bytes, which is enough to recover
// the UDP port pair or the ICMP echo identifier.
func matchesProbe(inner []byte, k probeKey) bool {
	hdr, err := ipv4.ParseHeader(inner)
	if err != nil || hdr.Len <= 0 || len(inner) < hdr.Len+8 {
		return false
	}
	payload := inner[hdr.Len:]
	if k.udp {
		src := int(binary.BigEndian.Uint16(payload[0:2]))
		dst := int(binary.BigEndian.Uint16(payload[2:4]))
		return src == k.srcPort && dst == k.dstPort
	}
	// Quoted ICMP echo header: type, code, checksum, id, seq.
	id := int(binary.BigEndian.Uint16(payload[4:6]))
	seq := int(binary.BigEndian.Uint16(payload[6:8]))
	return id == k.echoID && seq == k.echoSeq
}

// awaitProbeReply reads from conn until a message matching k arrives or the
// deadline passes. Unrelated ICMP traffic - echo replies belonging to this
// receiver's own ping, late replies to earlier TTLs, other processes' ICMP - is
// discarded instead of being attributed to the current hop. reachedDest is true
// when the reply shows the probe arrived at the target rather than expiring in
// transit.
func awaitProbeReply(conn *icmp.PacketConn, deadline time.Time, k probeKey) (from net.Addr, reachedDest bool, err error) {
	buf := make([]byte, 1500)
	for {
		if time.Now().After(deadline) {
			return nil, false, errProbeTimeout
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, false, err
		}
		n, peer, readErr := conn.ReadFrom(buf)
		if readErr != nil {
			return nil, false, readErr
		}
		if peer == nil {
			continue
		}
		msg, parseErr := icmp.ParseMessage(icmpProtocolIPv4, buf[:n])
		if parseErr != nil {
			continue
		}
		switch body := msg.Body.(type) {
		case *icmp.TimeExceeded:
			if matchesProbe(body.Data, k) {
				return peer, false, nil
			}
		case *icmp.DstUnreach:
			// The target answering our UDP probe on a closed port means the
			// probe arrived: the path is complete.
			if matchesProbe(body.Data, k) {
				return peer, true, nil
			}
		case *icmp.Echo:
			if !k.udp && msg.Type == ipv4.ICMPTypeEchoReply &&
				body.ID == k.echoID && body.Seq == k.echoSeq {
				return peer, true, nil
			}
		}
	}
}

// HopResult is the latency measurement for a single traceroute hop.
type HopResult struct {
	Index   int
	Address string
	RTT     time.Duration

	// TimedOut is true when the hop did not answer within the timeout. RTT is
	// meaningless for such a hop (it only reflects how long we waited), so
	// callers must not report it as a latency.
	TimedOut bool
}

// TraceResult is the outcome of a single traceroute run. It carries the path
// itself plus the context needed to tell an incomplete path from a complete
// one: whether the destination actually answered, and whether the trace gave up
// early after a run of silent hops. Metrics only consume Hops; the remaining
// fields exist because a log record describes the trace as a whole.
type TraceResult struct {
	Hops []HopResult

	// DestIP is the address the hostname resolved to for this run. A hostname
	// with several A records can resolve differently between runs.
	DestIP string

	// Method is the probe mechanism actually used: "udp", "icmp", or "native".
	Method string

	// MaxHops is the TTL ceiling this run was bounded by.
	MaxHops int

	// Reached is true when the destination itself answered.
	Reached bool

	// AbortedEarly is true when the trace stopped after maxConsecutiveTimeouts
	// silent hops rather than reaching the destination or the TTL ceiling.
	// Without this, a truncated path is indistinguishable from a short one.
	AbortedEarly bool
}

// tracerouter performs traceroute probes for a single host.
type tracerouter struct {
	cfg  TracerouteConfig
	host string
}

func newTracerouter(cfg TracerouteConfig, endpoint string) *tracerouter {
	return &tracerouter{cfg: cfg, host: hostFromEndpoint(endpoint)}
}

// hostFromEndpoint extracts the bare hostname from an endpoint that may be a
// full URL (e.g. "https://example.com/path") or a plain host/IP. The port is
// stripped so the result can be passed to net.LookupHost.
func hostFromEndpoint(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		h, _, err := net.SplitHostPort(u.Host)
		if err != nil {
			return u.Host
		}
		return h
	}
	h, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	return h
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
func (t *tracerouter) trace(ctx context.Context) (TraceResult, error) {
	method := strings.ToLower(t.cfg.Method)
	if method == "" {
		method = "udp"
	}
	maxHops := t.cfg.MaxHops
	if maxHops <= 0 {
		maxHops = 30
	}

	// Resolve target to an IP address.
	addrs, err := net.LookupHost(t.host)
	if err != nil || len(addrs) == 0 {
		return TraceResult{Method: method, MaxHops: maxHops}, fmt.Errorf("resolving %s: %w", t.host, err)
	}
	dest := addrs[0]

	// Some platforms cannot map a path with raw sockets and need a native API
	// instead. Windows is the case that matters today: it does not deliver
	// unsolicited inbound ICMP time-exceeded messages to a raw socket, so both
	// the UDP and ICMP methods below time out on every hop there regardless of
	// privileges. traceNative reports handled=false everywhere else.
	if hops, handled, nativeErr := t.traceNative(ctx, dest); handled {
		return TraceResult{
			Hops:         hops,
			DestIP:       dest,
			Method:       "native",
			MaxHops:      maxHops,
			Reached:      hopsReachedDest(hops, dest),
			AbortedEarly: hopsAbortedEarly(hops, maxHops),
		}, nativeErr
	}

	var res TraceResult
	var traceErr error
	switch method {
	case "icmp":
		res, traceErr = t.traceICMP(ctx, dest)
	default:
		res, traceErr = t.traceUDP(ctx, dest)
	}
	res.DestIP = dest
	res.Method = method
	res.MaxHops = maxHops
	return res, traceErr
}

// hopsReachedDest reports whether the last answering hop was the destination.
// Used for the native path, which reports hops without saying how it finished.
func hopsReachedDest(hops []HopResult, dest string) bool {
	for i := len(hops) - 1; i >= 0; i-- {
		if !hops[i].TimedOut {
			return hops[i].Address == dest
		}
	}
	return false
}

// hopsAbortedEarly reports whether a path ends in the run of silent hops that
// triggers the maxConsecutiveTimeouts bail-out, rather than ending because the
// destination answered or the TTL ceiling was hit.
func hopsAbortedEarly(hops []HopResult, maxHops int) bool {
	if len(hops) == 0 || len(hops) >= maxHops {
		return false
	}
	trailing := 0
	for i := len(hops) - 1; i >= 0 && hops[i].TimedOut; i-- {
		trailing++
	}
	return trailing >= maxConsecutiveTimeouts
}

// traceUDP sends UDP packets with incrementing TTL and listens for ICMP
// time-exceeded responses to map the path.
func (t *tracerouter) traceUDP(ctx context.Context, dest string) (TraceResult, error) {
	destAddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(dest, "33434"))
	if err != nil {
		return TraceResult{}, fmt.Errorf("resolving UDP dest: %w", err)
	}

	// Open raw ICMP socket to receive time-exceeded responses.
	icmpConn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return TraceResult{}, fmt.Errorf("opening ICMP listener for traceroute: %w", err)
	}
	defer icmpConn.Close()

	var hops []HopResult
	maxHops := t.cfg.MaxHops
	if maxHops <= 0 {
		maxHops = 30
	}
	consecutiveTimeouts := 0
	var reached, aborted bool

	for ttl := 1; ttl <= maxHops; ttl++ {
		if ctx.Err() != nil {
			break
		}

		// Send a UDP packet with the given TTL.
		udpConn, err := net.DialUDP("udp4", nil, destAddr)
		if err != nil {
			return TraceResult{Hops: hops, Reached: reached, AbortedEarly: aborted}, fmt.Errorf("dialing UDP: %w", err)
		}
		ipConn := ipv4.NewConn(udpConn)
		if err := ipConn.SetTTL(ttl); err != nil {
			_ = udpConn.Close()
			return TraceResult{Hops: hops, Reached: reached, AbortedEarly: aborted}, fmt.Errorf("setting TTL %d: %w", ttl, err)
		}

		// The source port identifies this probe in the ICMP error that comes
		// back, so it must be read before the socket is closed.
		localPort := 0
		if la, ok := udpConn.LocalAddr().(*net.UDPAddr); ok {
			localPort = la.Port
		}

		sent := time.Now()
		if _, err := udpConn.Write([]byte("ping")); err != nil {
			_ = udpConn.Close()
			return TraceResult{Hops: hops, Reached: reached, AbortedEarly: aborted}, fmt.Errorf("sending UDP probe: %w", err)
		}
		_ = udpConn.Close()

		// Wait for ICMP time-exceeded.
		hopTimeout := t.cfg.Timeout
		if hopTimeout == 0 {
			hopTimeout = 3 * time.Second
		}
		from, reachedDest, err := awaitProbeReply(icmpConn, time.Now().Add(hopTimeout), probeKey{
			udp:     true,
			srcPort: localPort,
			dstPort: destAddr.Port,
		})
		rtt := time.Since(sent)

		if err != nil || from == nil {
			hops = append(hops, HopResult{
				Index:    ttl,
				Address:  unansweredHopAddress,
				TimedOut: true,
			})
			consecutiveTimeouts++
			if consecutiveTimeouts >= maxConsecutiveTimeouts {
				aborted = true
				break
			}
			continue
		}
		consecutiveTimeouts = 0

		hops = append(hops, HopResult{
			Index:   ttl,
			Address: from.String(),
			RTT:     rtt,
		})

		// Stop when we reach the destination.
		fromHost, _, splitErr := net.SplitHostPort(from.String())
		if splitErr != nil {
			fromHost = from.String()
		}
		if reachedDest || fromHost == dest {
			reached = true
			break
		}
	}

	return TraceResult{Hops: hops, Reached: reached, AbortedEarly: aborted}, nil
}

// traceICMP sends ICMP echo requests with incrementing TTL values and collects
// ICMP time-exceeded responses. Requires root / CAP_NET_RAW.
func (t *tracerouter) traceICMP(ctx context.Context, dest string) (TraceResult, error) {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return TraceResult{}, fmt.Errorf("opening raw ICMP socket for traceroute: %w", err)
	}
	defer conn.Close()

	destAddr, err := net.ResolveIPAddr("ip4", dest)
	if err != nil {
		return TraceResult{}, fmt.Errorf("resolving ICMP dest: %w", err)
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
	consecutiveTimeouts := 0
	var reached, aborted bool

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
			return TraceResult{Hops: hops, Reached: reached, AbortedEarly: aborted}, fmt.Errorf("marshaling ICMP echo: %w", err)
		}

		// Use the connection's own IPv4 accessor rather than
		// ipv4.NewPacketConn: that constructor type-asserts to net.Conn
		// without a comma-ok, and *icmp.PacketConn implements net.PacketConn
		// but not net.Conn, so passing one panics.
		p4 := conn.IPv4PacketConn()
		if p4 == nil {
			return TraceResult{Hops: hops, Reached: reached, AbortedEarly: aborted}, fmt.Errorf("ICMP traceroute requires an IPv4 connection")
		}
		if err := p4.SetTTL(ttl); err != nil {
			return TraceResult{Hops: hops, Reached: reached, AbortedEarly: aborted}, fmt.Errorf("setting ICMP TTL %d: %w", ttl, err)
		}

		sent := time.Now()
		if _, err := conn.WriteTo(wb, destAddr); err != nil {
			return TraceResult{Hops: hops, Reached: reached, AbortedEarly: aborted}, fmt.Errorf("sending ICMP probe: %w", err)
		}

		from, reachedDest, err := awaitProbeReply(conn, time.Now().Add(hopTimeout), probeKey{
			echoID:  ttl,
			echoSeq: ttl,
		})
		rtt := time.Since(sent)

		if err != nil || from == nil {
			hops = append(hops, HopResult{
				Index:    ttl,
				Address:  unansweredHopAddress,
				TimedOut: true,
			})
			consecutiveTimeouts++
			if consecutiveTimeouts >= maxConsecutiveTimeouts {
				aborted = true
				break
			}
			continue
		}
		consecutiveTimeouts = 0

		hops = append(hops, HopResult{
			Index:   ttl,
			Address: from.String(),
			RTT:     rtt,
		})

		if reachedDest || from.String() == destAddr.String() {
			reached = true
			break
		}
	}

	return TraceResult{Hops: hops, Reached: reached, AbortedEarly: aborted}, nil
}
