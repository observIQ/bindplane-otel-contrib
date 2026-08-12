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

package fingerprint

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFingerprintCLFInvalid(t *testing.T) {
	cases := []string{
		"",
		"just some plain text",
		`127.0.0.1 - -`,
		`127.0.0.1 - - 04/Aug/2026 "GET / HTTP/1.1" 200 10`,
		`127.0.0.1 - - [04/Aug/2026:09:12:41 +0000 "GET / HTTP/1.1" 200 10`,
		`127.0.0.1 - - [04/Aug/2026:09:12:41 +0000] GET / HTTP/1.1 200 10`,
		`127.0.0.1 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/1.1" OK 10`,
		`127.0.0.1 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/1.1" 200`,
		`127.0.0.1 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/1.1" 200 10 "ref"`,
		`127.0.0.1 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/1.1" 200 10 "ref" "ua" extra`,
		`127.0.0.1 - - [04/Aug/2026:09:12:41 +0000] "unterminated 200 10`,
	}

	for _, c := range cases {
		require.Zero(t, fingerprintCLF(c), "expected no fingerprint for %q", c)
	}
}

func TestFingerprintCLFRequest(t *testing.T) {
	cases := []struct {
		title       string
		logA, logB  string
		shouldEqual bool
	}{
		{
			title:       "different timestamp",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /index.html HTTP/1.1" 200 10432`,
			logB:        `192.168.14.77 - - [05/Aug/2026:17:03:09 -0400] "GET /index.html HTTP/1.1" 200 10432`,
			shouldEqual: true,
		},
		{
			title:       "different client and user",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /index.html HTTP/1.1" 200 10432`,
			logB:        `10.4.22.19 - jsmith [04/Aug/2026:09:12:41 +0000] "GET /index.html HTTP/1.1" 200 10432`,
			shouldEqual: true,
		},
		{
			title:       "different status and bytes",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /index.html HTTP/1.1" 200 10432`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /index.html HTTP/1.1" 404 -`,
			shouldEqual: true,
		},
		{
			title:       "different referer and user agent",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/1.1" 200 10 "-" "curl/8.7.1"`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/1.1" 200 10 "http://example.com/" "Mozilla/5.0"`,
			shouldEqual: true,
		},
		{
			title:       "different http version",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/1.1" 200 10`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/2.0" 200 10`,
			shouldEqual: true,
		},
		{
			title:       "different id in path",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /orders/4471 HTTP/1.1" 200 10`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /orders/8812 HTTP/1.1" 200 10`,
			shouldEqual: true,
		},
		{
			title:       "different hashed asset name",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /static/css/main.7f3a1c.css HTTP/1.1" 200 10`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /static/css/main.9b2e4d.css HTTP/1.1" 200 10`,
			shouldEqual: true,
		},
		{
			title:       "different query value",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /products/sale?page=4 HTTP/1.1" 200 10`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /products/sale?page=9 HTTP/1.1" 200 10`,
			shouldEqual: true,
		},
		{
			title:       "different query key",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /search?q=pans HTTP/1.1" 200 10`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /search?user=pans HTTP/1.1" 200 10`,
			shouldEqual: false,
		},
		{
			title:       "different request path",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /index.html HTTP/1.1" 200 10432`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /login.html HTTP/1.1" 200 10432`,
			shouldEqual: false,
		},
		{
			title:       "different method",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET /orders HTTP/1.1" 200 10`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "POST /orders HTTP/1.1" 200 10`,
			shouldEqual: false,
		},
		{
			title:       "common vs combined",
			logA:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/1.1" 200 10`,
			logB:        `192.168.14.77 - - [04/Aug/2026:09:12:41 +0000] "GET / HTTP/1.1" 200 10 "-" "curl/8.7.1"`,
			shouldEqual: false,
		},
	}

	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			fingerprintA := fingerprintCLF(c.logA)
			fingerprintB := fingerprintCLF(c.logB)
			require.NotZero(t, fingerprintA)
			require.NotZero(t, fingerprintB)
			if c.shouldEqual {
				require.Equal(t, fingerprintA, fingerprintB)
			} else {
				require.NotEqual(t, fingerprintA, fingerprintB)
			}
		})
	}
}
