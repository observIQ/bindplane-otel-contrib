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

//go:build windows

package networkcheckreceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver"

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows does not deliver unsolicited inbound ICMP time-exceeded messages to a
// raw socket, so the portable UDP and ICMP traceroute methods time out on every
// hop there even when running with Administrator rights. The IP Helper API
// correlates the replies in the kernel and hands them back directly, which is
// how the built-in tracert.exe works. We use the same API here.
var (
	modIphlpapi         = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = modIphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = modIphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = modIphlpapi.NewProc("IcmpSendEcho")
)

// Status values returned in ICMP_ECHO_REPLY.Status. See IP_STATUS in ipexport.h.
const (
	ipSuccess           = 0
	ipReqTimedOut       = 11010
	ipTTLExpiredTransit = 11013
)

// ipOptionInformation mirrors IP_OPTION_INFORMATION from ipexport.h. On 64-bit
// Windows OptionsData is a pointer, so it is 8-byte aligned and the struct is
// 16 bytes wide; the explicit padding field keeps the Go layout identical.
type ipOptionInformation struct {
	TTL         uint8
	TOS         uint8
	Flags       uint8
	OptionsSize uint8
	_           [4]byte
	OptionsData uintptr
}

// icmpEchoReply mirrors ICMP_ECHO_REPLY from ipexport.h (64-bit layout, 40
// bytes). Data is a pointer into the reply buffer, which we never dereference.
type icmpEchoReply struct {
	Address       uint32
	Status        uint32
	RoundTripTime uint32
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
	Options       ipOptionInformation
}

// traceNative maps the path to dest using IcmpSendEcho with an incrementing
// TTL. handled is always true on Windows.
func (t *tracerouter) traceNative(ctx context.Context, dest string) (hops []HopResult, handled bool, err error) {
	destIP := net.ParseIP(dest).To4()
	if destIP == nil {
		return nil, true, fmt.Errorf("traceroute requires an IPv4 destination, got %q", dest)
	}
	// IPAddr is a DWORD holding the octets in network byte order, which is the
	// same as reading the 4 bytes in memory order on a little-endian host.
	destAddr := binary.LittleEndian.Uint32(destIP)

	handle, _, createErr := procIcmpCreateFile.Call()
	if windows.Handle(handle) == windows.InvalidHandle {
		return nil, true, fmt.Errorf("IcmpCreateFile: %w", createErr)
	}
	defer procIcmpCloseHandle.Call(handle)

	hopTimeout := t.cfg.Timeout
	if hopTimeout == 0 {
		hopTimeout = 3 * time.Second
	}
	maxHops := t.cfg.MaxHops
	if maxHops <= 0 {
		maxHops = 30
	}

	// The reply buffer must hold an ICMP_ECHO_REPLY plus the echoed request
	// data and any ICMP error payload. The API requires at least
	// sizeof(ICMP_ECHO_REPLY) + 8; oversize it so a hop that returns options
	// or a larger error body still fits.
	payload := []byte("bindplane-networkcheck")
	replyBuf := make([]byte, int(unsafe.Sizeof(icmpEchoReply{}))+len(payload)+256)

	consecutiveTimeouts := 0
	for ttl := 1; ttl <= maxHops; ttl++ {
		if ctx.Err() != nil {
			break
		}

		opts := ipOptionInformation{TTL: uint8(ttl)}
		sent := time.Now()
		n, _, sendErr := procIcmpSendEcho.Call(
			handle,
			uintptr(destAddr),
			uintptr(unsafe.Pointer(&payload[0])),
			uintptr(len(payload)),
			uintptr(unsafe.Pointer(&opts)),
			uintptr(unsafe.Pointer(&replyBuf[0])),
			uintptr(len(replyBuf)),
			uintptr(hopTimeout.Milliseconds()),
		)
		elapsed := time.Since(sent)

		// A zero reply count means no usable answer. The common case is the
		// hop staying silent, which surfaces as IP_REQ_TIMED_OUT.
		if n == 0 {
			if errno, ok := sendErr.(windows.Errno); ok && uint32(errno) != ipReqTimedOut && uint32(errno) != 0 {
				// Anything other than a timeout is a real failure worth
				// surfacing rather than recording as a silent hop.
				return hops, true, fmt.Errorf("IcmpSendEcho (ttl %d): %w", ttl, sendErr)
			}
			hops = append(hops, HopResult{Index: ttl, Address: unansweredHopAddress, TimedOut: true})
			consecutiveTimeouts++
			if consecutiveTimeouts >= maxConsecutiveTimeouts {
				break
			}
			continue
		}

		reply := (*icmpEchoReply)(unsafe.Pointer(&replyBuf[0]))
		if reply.Status != ipSuccess && reply.Status != ipTTLExpiredTransit {
			// Unreachable and similar errors identify a real router, but the
			// path cannot continue past it.
			hops = append(hops, HopResult{Index: ttl, Address: unansweredHopAddress, TimedOut: true})
			break
		}
		consecutiveTimeouts = 0

		// RoundTripTime is whole milliseconds and is frequently reported as 0
		// for time-exceeded replies, so fall back to the measured elapsed time
		// to avoid publishing a stream of zero-latency hops.
		rtt := time.Duration(reply.RoundTripTime) * time.Millisecond
		if rtt == 0 {
			rtt = elapsed
		}

		var octets [4]byte
		binary.LittleEndian.PutUint32(octets[:], reply.Address)
		hops = append(hops, HopResult{
			Index:   ttl,
			Address: net.IPv4(octets[0], octets[1], octets[2], octets[3]).String(),
			RTT:     rtt,
		})

		if reply.Status == ipSuccess {
			break
		}
	}

	return hops, true, nil
}
