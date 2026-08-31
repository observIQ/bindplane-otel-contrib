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
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/icmp"
)

func TestHostFromEndpoint(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://apps.mypurecloud.com/", "apps.mypurecloud.com"},
		{"https://apps.mypurecloud.com/some/path", "apps.mypurecloud.com"},
		{"http://example.com:8080/health", "example.com"},
		{"8.8.8.8", "8.8.8.8"},
		{"example.com", "example.com"},
		{"example.com:443", "example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			require.Equal(t, tc.want, hostFromEndpoint(tc.input))
		})
	}
}

// TestICMPPacketConnIsNotNetConn pins the interface fact behind a crash in
// traceICMP: ipv4.NewPacketConn type-asserts its argument to net.Conn without a
// comma-ok, so handing it an *icmp.PacketConn panics. traceICMP must use
// conn.IPv4PacketConn() instead. If this test ever fails, x/net has widened
// *icmp.PacketConn and the workaround can be revisited.
func TestICMPPacketConnIsNotNetConn(t *testing.T) {
	var conn *icmp.PacketConn
	var v any = conn

	_, isPacketConn := v.(net.PacketConn)
	require.True(t, isPacketConn, "*icmp.PacketConn should satisfy net.PacketConn")

	_, isConn := v.(net.Conn)
	require.False(t, isConn,
		"*icmp.PacketConn must not satisfy net.Conn; ipv4.NewPacketConn would panic on it")
}

// TestSetTTLViaIPv4PacketConn exercises the accessor traceICMP relies on, which
// is the call that previously panicked.
func TestSetTTLViaIPv4PacketConn(t *testing.T) {
	// "udp4" is the unprivileged ICMP socket flavor; raw "ip4:icmp" needs root.
	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		t.Skipf("ICMP socket unavailable in this environment: %v", err)
	}
	defer conn.Close()

	p4 := conn.IPv4PacketConn()
	require.NotNil(t, p4, "IPv4PacketConn should be non-nil for an IPv4 listener")
	require.NotPanics(t, func() {
		_ = p4.SetTTL(5)
	})
}

// TestTraceUnansweredHopsAreMarkedAndBounded checks the two reporting rules for
// a path that never answers: every silent hop is flagged TimedOut (so no bogus
// latency reaches the metrics builder), and the walk stops after
// maxConsecutiveTimeouts instead of running the full max_hops range.
func TestTraceUnansweredHopsAreMarkedAndBounded(t *testing.T) {
	// 192.0.2.0/24 is TEST-NET-1 (RFC 5737) and is not routed, so probes to it
	// go unanswered without depending on any particular network.
	const blackhole = "192.0.2.1"

	tr := newTracerouter(TracerouteConfig{
		Enabled: true,
		Method:  "udp",
		MaxHops: 30,
		Timeout: 50 * time.Millisecond,
	}, blackhole)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hops, handled, err := tr.traceNative(ctx, blackhole)
	if handled {
		require.NoError(t, err)
	} else {
		var res TraceResult
		res, err = tr.traceUDP(ctx, blackhole)
		if err != nil {
			t.Skipf("raw ICMP socket unavailable in this environment: %v", err)
		}
		hops = res.Hops
	}

	// Early hops toward the blackhole are real routers that do answer, so the
	// bound is on the run of consecutive silent hops, not the total.
	longestRun, run := 0, 0
	for _, hop := range hops {
		if hop.TimedOut {
			run++
			if run > longestRun {
				longestRun = run
			}
			require.Equal(t, unansweredHopAddress, hop.Address,
				"a timed-out hop should carry the unanswered address")
			require.Zero(t, hop.RTT,
				"hop %d timed out; its RTT must not carry the probe timeout as a latency",
				hop.Index)
			continue
		}
		run = 0
		require.NotEqual(t, unansweredHopAddress, hop.Address,
			"a hop that answered should carry a real address")
	}

	require.LessOrEqual(t, longestRun, maxConsecutiveTimeouts,
		"the walk must abandon the path after %d consecutive timeouts", maxConsecutiveTimeouts)
	require.Less(t, len(hops), 30,
		"the walk must stop early on an unanswered path rather than reaching max_hops")
}
