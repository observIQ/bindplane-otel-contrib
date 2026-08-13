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

func TestFingerprintSyslogInvalid(t *testing.T) {
	cases := []string{
		"",
		"just some plain text",
		"<38>",
		"<>Aug  4 09:14:22 host sshd[1]: message",
		"<999>Aug  4 09:14:22 host sshd[1]: message",
		"<1234>Aug  4 09:14:22 host sshd[1]: message",
		"<38>Aug 4 09:14:22 host sshd[1]: message",
		"<38>Aug  4 09:14 host sshd[1]: message",
		"<38>04/Aug/2026 09:14:22 host sshd[1]: message",
		"<38>Aug  4 09:14:22 host",
		"<38>Aug  4 09:14:22",
		"<38>1 notatimestamp host app 123 ID47 - message",
		"<38>1 2026-03-14T08:22:41Z host",
		"<38>1 2026-03-14T08:22:41Z host app",
		"<38>1 2026-03-14T08:22:41Z host app 123",
		`<38>1 2026-03-14T08:22:41Z host app 123 ID47 [x foo="unterminated`,
		`<38>1 2026-03-14T08:22:41Z host app 123 ID47 [x foo=bar] message`,
		`<38>1 2026-03-14T08:22:41Z host app 123 ID47 [x foo] message`,
		"<38>2 2026-03-14T08:22:41Z host app 123 ID47 - message",
	}

	for _, c := range cases {
		require.Zero(t, fingerprintSyslog(c), "expected no fingerprint for %q", c)
	}
}

func TestFingerprintValidSyslog(t *testing.T) {
	cases := []string{
		"<38>Aug  4 09:14:22 web-prod-03 sshd[24417]: Accepted publickey for deploy",
		"<38>Aug 14 09:14:22 web-prod-03 sshd: no pid tag",
		"<187>Aug  4 10:18:12 nexus-spine-01 : 2026 Aug  4 10:18:12 EDT: %ETHPORT-5-IF_UP: up",
		"<188>Aug  4 10:20:18 fgt-branch-07 date=2026-08-04 time=10:20:18 devname=\"fgt\" no tag at all",
		"<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - - Failed password for invalid user",
		`<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - [origin ip="203.0.113.44"] Failed password`,
		`<38>1 - host app - - [meta seq="1"][origin ip="10.0.0.1"] two elements`,
		"<84>1 2026-03-15T03:14:38.229Z esg-01.example.com barracuda - - info CEF:0|Barracuda|ESG - missing structured data field",
		"<38>1 2026-03-14T08:22:41Z host app - - -",
	}

	for _, c := range cases {
		require.NotZero(t, fingerprintSyslog(c), "expected a fingerprint for %q", c)
	}
}

func TestFingerprintSyslogStructure(t *testing.T) {
	cases := []struct {
		title      string
		logA, logB string
		equal      bool
	}{
		{
			title: "3164 different timestamp",
			logA:  "<38>Aug  4 09:14:22 host sshd[24417]: Connection closed by 10.4.19.7 port 51422",
			logB:  "<38>Dec 21 23:59:01 host sshd[24417]: Connection closed by 10.4.19.7 port 51422",
			equal: true,
		},
		{
			title: "3164 different hostname",
			logA:  "<38>Aug  4 09:14:22 web-prod-03 sshd[24417]: Connection closed by 10.4.19.7 port 51422",
			logB:  "<38>Aug  4 09:14:22 bastion sshd[24417]: Connection closed by 10.4.19.7 port 51422",
			equal: true,
		},
		{
			title: "different priority",
			logA:  "<38>Aug  4 09:14:22 host sshd[24417]: Connection closed by 10.4.19.7 port 51422",
			logB:  "<30>Aug  4 09:14:22 host sshd[24417]: Connection closed by 10.4.19.7 port 51422",
			equal: true,
		},
		{
			title: "3164 different pid and values",
			logA:  "<38>Aug  4 09:14:22 host sshd[24417]: Connection closed by 10.4.19.7 port 51422",
			logB:  "<38>Aug  4 09:14:22 host sshd[8]: Connection closed by 192.168.0.1 port 22",
			equal: true,
		},
		{
			title: "3164 different hashed session id",
			logA:  "<22>Aug  4 09:57:40 imap-01 dovecot[2288]: Login: session=<9Xz2Kq1bT4mYaZ8P>",
			logB:  "<22>Aug  4 09:57:40 imap-01 dovecot[2288]: Login: session=<xM2kQd7dAaBmgZEU>",
			equal: true,
		},
		{
			title: "3164 different tag",
			logA:  "<38>Aug  4 09:14:22 host sshd[24417]: Connection closed",
			logB:  "<38>Aug  4 09:14:22 host sudo[24417]: Connection closed",
			equal: false,
		},
		{
			title: "3164 different message words",
			logA:  "<38>Aug  4 09:14:22 host sshd[24417]: Connection closed",
			logB:  "<38>Aug  4 09:14:22 host sshd[24417]: Connection opened",
			equal: false,
		},
		{
			title: "5424 different timestamp hostname and procid",
			logA:  "<38>1 2026-03-14T08:22:41.114Z bastion-01 sshd 24817 - - Failed password",
			logB:  "<38>1 2026-12-01T23:59:59.000Z vpn-gw-02 sshd 8843 - - Failed password",
			equal: true,
		},
		{
			title: "5424 different structured data values",
			logA:  `<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - [origin ip="203.0.113.44"] Failed password`,
			logB:  `<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - [origin ip="198.51.100.7"] Failed password`,
			equal: true,
		},
		{
			title: "5424 different structured data param name",
			logA:  `<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - [origin ip="203.0.113.44"] Failed password`,
			logB:  `<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - [origin host="203.0.113.44"] Failed password`,
			equal: false,
		},
		{
			title: "5424 different structured data id",
			logA:  `<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - [origin ip="203.0.113.44"] Failed password`,
			logB:  `<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - [meta ip="203.0.113.44"] Failed password`,
			equal: false,
		},
		{
			title: "5424 different app name",
			logA:  "<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - - Failed password",
			logB:  "<38>1 2026-03-14T08:22:41.114Z host sudo 24817 - - Failed password",
			equal: false,
		},
		{
			title: "5424 different msgid",
			logA:  "<84>1 2026-03-15T18:31:09.284Z host SentinelOne - Threat - Threat detected",
			logB:  "<84>1 2026-03-15T18:31:09.284Z host SentinelOne - Scan - Threat detected",
			equal: false,
		},
		{
			title: "5424 structured data vs nil",
			logA:  `<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - [origin ip="1.2.3.4"] Failed password`,
			logB:  "<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - - Failed password",
			equal: false,
		},
		{
			title: "3164 vs 5424",
			logA:  "<38>Aug  4 09:14:22 host sshd: Failed password",
			logB:  "<38>1 2026-03-14T08:22:41.114Z host sshd 24817 - - Failed password",
			equal: false,
		},
	}

	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			fingerprintA := fingerprintSyslog(c.logA)
			fingerprintB := fingerprintSyslog(c.logB)
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
