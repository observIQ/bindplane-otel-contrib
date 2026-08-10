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
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The detected value becomes the dns.server attribute, so whatever comes back
// must be a bare IP. The concrete address is environment-specific, but a
// malformed one (a port, a stray NUL from the Windows socket structures, an
// unspecified or link-local address) is a defect anywhere.
func TestDetectSystemDNSReturnsBareIPOrEmpty(t *testing.T) {
	got := detectSystemDNS()
	if got == "" {
		t.Skip("no system DNS server configured in this environment")
	}

	require.Equal(t, strings.TrimSpace(got), got, "must not carry surrounding whitespace")
	require.NotContains(t, got, "\x00", "must not carry a NUL from the platform structures")
	require.NotContains(t, got, " ", "must be a single address")

	ip := net.ParseIP(got)
	require.NotNil(t, ip, "detectSystemDNS returned %q, which is not a valid IP", got)
	require.False(t, ip.IsUnspecified(), "0.0.0.0 / :: is not a usable resolver")
	require.False(t, ip.IsLinkLocalUnicast(), "a link-local address is not a usable resolver")

	_, _, err := net.SplitHostPort(got)
	require.Error(t, err, "must be a bare IP with no port, got %q", got)
}
