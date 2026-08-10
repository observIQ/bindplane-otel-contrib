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
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no /etc/resolv.conf, so the resolver configuration comes from
// GetAdaptersAddresses, the same source ipconfig and Get-DnsClientServerAddress
// read.

// initialAdaptersBufferSize is the working buffer for GetAdaptersAddresses.
// Microsoft recommends starting at 15KB to avoid the near-certain overflow
// retry that a smaller buffer causes.
const initialAdaptersBufferSize = 15 * 1024

// maxAdaptersBufferAttempts bounds the grow-and-retry loop. The required size
// is returned on overflow, so one retry normally suffices; the extra attempts
// cover an adapter appearing between calls.
const maxAdaptersBufferAttempts = 4

// detectSystemDNS returns the first DNS server configured on an operational,
// non-loopback adapter, preferring IPv4. It returns an empty string when none
// can be determined, in which case the dns.server attribute is left blank.
func detectSystemDNS() string {
	adapters, err := adapterAddresses()
	if err != nil {
		return ""
	}

	var firstIPv6 string
	for a := adapters; a != nil; a = a.Next {
		// Skip adapters that are not up (disconnected NICs, disabled virtual
		// adapters) and the loopback pseudo-adapter. Their DNS entries are not
		// what queries actually use.
		if a.OperStatus != windows.IfOperStatusUp || a.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}

		for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
			ip := d.Address.IP()
			if ip == nil || ip.IsUnspecified() {
				continue
			}
			// Windows adds site-local anycast DNS addresses (fec0:0:0:ffff::1
			// and friends) to every adapter whether or not a resolver is
			// listening there. Reporting one would name a server that never
			// answers a query.
			if ip.IsLinkLocalUnicast() || isSiteLocalIPv6(ip) {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
			if firstIPv6 == "" {
				firstIPv6 = ip.String()
			}
		}
	}

	// No IPv4 resolver anywhere; fall back to an IPv6 one if we saw it.
	return firstIPv6
}

// isSiteLocalIPv6 reports whether ip is in the deprecated fec0::/10 site-local
// range, which net.IP has no helper for.
func isSiteLocalIPv6(ip net.IP) bool {
	v6 := ip.To16()
	if v6 == nil || ip.To4() != nil {
		return false
	}
	return v6[0] == 0xfe && v6[1]&0xc0 == 0xc0
}

// adapterAddresses calls GetAdaptersAddresses, growing the buffer until the
// result fits.
func adapterAddresses() (*windows.IpAdapterAddresses, error) {
	// Unicast, anycast and multicast addresses are not used here; skipping them
	// keeps the returned structure smaller. DNS server addresses are included
	// by default and must not be skipped.
	const flags = windows.GAA_FLAG_SKIP_UNICAST |
		windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST |
		windows.GAA_FLAG_SKIP_FRIENDLY_NAME

	size := uint32(initialAdaptersBufferSize)
	for range maxAdaptersBufferAttempts {
		// The buffer is addressed as a linked list of variable-length
		// structures, so it is allocated as []byte and reinterpreted. Using a
		// []windows.IpAdapterAddresses would not give the right layout.
		buf := make([]byte, size)
		addresses := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))

		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, addresses, &size)
		if err == nil {
			return addresses, nil
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil, err
		}
		// On overflow size holds the required length; loop and retry with it.
	}
	return nil, windows.ERROR_BUFFER_OVERFLOW
}
