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

//go:build !windows

package networkcheckreceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver"

import (
	"bufio"
	"os"
	"strings"
)

// detectSystemDNS reads the first nameserver entry from /etc/resolv.conf.
// It returns an empty string when no nameserver can be determined, in which
// case the dns.server attribute is left blank.
func detectSystemDNS() string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "nameserver") {
			if parts := strings.Fields(line); len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}
