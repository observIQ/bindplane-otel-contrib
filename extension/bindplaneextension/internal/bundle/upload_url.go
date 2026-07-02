// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bundle

import (
	"fmt"
	"net/url"
	"strings"
)

// UploadURL derives the support bundle REST endpoint from the OpAMP WebSocket endpoint and agent ID.
//
// Example: wss://example.com/opamp + "abc" → https://example.com/v1/agents/abc/support-bundle
//
// Returns an empty string if the endpoint scheme is not ws:// or wss://.
func UploadURL(opampEndpoint, agentID string) string {
	base, err := httpOrigin(opampEndpoint)
	if err != nil {
		return ""
	}
	return base + "/v1/agents/" + url.PathEscape(agentID) + "/support-bundle"
}

// httpOrigin converts a WebSocket URL to its HTTP origin (scheme + host, no path).
func httpOrigin(endpoint string) (string, error) {
	var httpEndpoint string
	switch {
	case strings.HasPrefix(endpoint, "wss://"):
		httpEndpoint = "https://" + endpoint[6:]
	case strings.HasPrefix(endpoint, "ws://"):
		httpEndpoint = "http://" + endpoint[5:]
	default:
		return "", fmt.Errorf("unsupported OpAMP endpoint scheme: %s", endpoint)
	}

	parsed, err := url.Parse(httpEndpoint)
	if err != nil {
		return "", err
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}
