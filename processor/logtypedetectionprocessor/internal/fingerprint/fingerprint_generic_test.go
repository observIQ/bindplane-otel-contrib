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

func TestFingerprintGenericStructure(t *testing.T) {
	cases := []struct {
		title      string
		logA, logB string
		equal      bool
	}{
		{
			title: "plain text same words different numbers",
			logA:  "Connection closed by 10.4.19.7 port 51422",
			logB:  "Connection closed by 192.168.1.10 port 22",
			equal: true,
		},
		{
			title: "plain text different words",
			logA:  "Connection closed by 10.4.19.7 port 51422",
			logB:  "Accepted publickey for deploy",
			equal: false,
		},
		{
			title: "plain text timestamps replaced",
			logA:  "2026-08-13T10:00:00Z task completed in 42ms",
			logB:  "2026-01-01T00:00:00Z task completed in 7ms",
			equal: true,
		},
		{
			title: "plain text whitespace runs normalized",
			logA:  "task   completed \t successfully",
			logB:  "task completed successfully",
			equal: true,
		},
		{
			title: "kv same keys different values",
			logA:  `date=2026-08-04 time=10:20:18 devname="fgt-1" action=login status=success`,
			logB:  `date=2026-12-25 time=23:59:59 devname="fgt-2 primary" action=logout status=failure`,
			equal: true,
		},
		{
			title: "kv different keys",
			logA:  "date=2026-08-04 action=login status=success",
			logB:  "date=2026-08-04 action=login result=success",
			equal: false,
		},
		{
			title: "kv quoted and unquoted values",
			logA:  `action=login user="alice liddell"`,
			logB:  "action=logout user=bob",
			equal: true,
		},
		{
			title: "csv same shape different values",
			logA:  "2026-08-13,GET,alice,200,1523",
			logB:  "2026-08-14,POST,bob,500,88",
			equal: true,
		},
		{
			title: "csv different field type",
			logA:  "2026-08-13,GET,alice,200,1523",
			logB:  "2026-08-13,GET,4217,200,1523",
			equal: false,
		},
		{
			title: "csv three fields hashes as text",
			logA:  "warn,disk,90",
			logB:  "info,memory,45",
			equal: false,
		},
		{
			title: "csv different field count",
			logA:  "2026-08-13,GET,alice,200",
			logB:  "2026-08-13,GET,alice,200,1523",
			equal: false,
		},
		{
			title: "quoted text replaced",
			logA:  `queued job "resize images"`,
			logB:  `queued job "send emails"`,
			equal: true,
		},
	}

	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			fingerprintA := fingerprintGeneric(c.logA)
			fingerprintB := fingerprintGeneric(c.logB)
			require.NotZero(t, fingerprintA)
			require.NotZero(t, fingerprintB)
			if c.equal {
				require.Equal(t, fingerprintA, fingerprintB)
			} else {
				require.NotEqual(t, fingerprintA, fingerprintB)
			}
		})
	}
}

func TestHashLogGenericFallback(t *testing.T) {
	require.NotZero(t, HashLog("plain text message", false))
	require.NotZero(t, HashLog("plain text message", true))
}
