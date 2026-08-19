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

import "strings"

var syslogAlphanumericChars [256]bool
var syslogTagChars [256]bool

func init() {
	for c := byte('0'); c <= '9'; c++ {
		syslogAlphanumericChars[c] = true
		syslogTagChars[c] = true
	}
	for c := byte('a'); c <= 'z'; c++ {
		syslogAlphanumericChars[c] = true
		syslogTagChars[c] = true
	}
	for c := byte('A'); c <= 'Z'; c++ {
		syslogAlphanumericChars[c] = true
		syslogTagChars[c] = true
	}
	for _, c := range []byte("-_./,") {
		syslogTagChars[c] = true
	}
}

// fingerprintSyslog creates a hash based off the service name of an RFC 3164
// or RFC 5424 syslog log, combined with the structural hash of the message
// body when it parses as a known format.
func fingerprintSyslog(data string) uint64 {
	i := skipSyslogPriority(data)
	if i < 0 {
		return 0
	}

	var service, msg string
	if strings.HasPrefix(data[i:], "1 ") {
		service, msg = syslog5424Message(data[i+2:])
	} else {
		service, msg = syslog3164Message(data[i:])
	}
	if msg == "" {
		return 0
	}

	hash := foldFNVHashString(fnvOffsetBasis, service)
	return foldFNVHashUint(hash, fingerprint(msg, true))
}

// syslog3164Tag returns the tag opening msg
func syslog3164Tag(msg string) string {
	i := 0
	for i < len(msg) && syslogTagChars[msg[i]] {
		i++
	}
	if i == 0 || i >= len(msg) || (msg[i] != ':' && msg[i] != '[') {
		return ""
	}

	return msg[:i]
}

// skipSyslogPriority returns the index after the "<pri>" prefix
func skipSyslogPriority(data string) int {
	if len(data) < 3 || data[0] != '<' {
		return -1
	}
	pri := 0
	i := 1
	for ; i < len(data) && isDigit(data[i]); i++ {
		if i > 3 {
			return -1
		}
		pri = pri*10 + int(data[i]-'0')
	}
	if i == 1 || i >= len(data) || data[i] != '>' || pri > 191 {
		return -1
	}

	return i + 1
}

func syslog3164Message(data string) (string, string) {
	if !isSyslog3164Timestamp(data) {
		return "", ""
	}

	i := skipSpaces(data, 16)
	end := endOfToken(data, i)
	if end == i {
		return "", ""
	}

	msg := data[skipSpaces(data, end):]
	service := syslog3164Tag(msg)
	if sep := strings.Index(msg, ": "); sep >= 0 {
		msg = msg[sep+2:]
	}

	return service, msg
}

func isSyslog3164Timestamp(data string) bool {
	if len(data) < 17 {
		return false
	}
	if data[3] != ' ' || data[6] != ' ' || data[9] != ':' || data[12] != ':' || data[15] != ' ' {
		return false
	}
	for _, i := range [3]int{0, 1, 2} {
		if !syslogAlphanumericChars[data[i]] || isDigit(data[i]) {
			return false
		}
	}
	if data[4] != ' ' && !isDigit(data[4]) {
		return false
	}
	for _, i := range [7]int{5, 7, 8, 10, 11, 13, 14} {
		if !isDigit(data[i]) {
			return false
		}
	}

	return true
}

func syslog5424Message(data string) (string, string) {
	end := endOfToken(data, 0)
	if !isSyslog5424Timestamp(data[:end]) {
		return "", ""
	}

	// Hostname, app name, procid and msgid.
	var service string
	i := end
	for field := range 4 {
		i = skipSpaces(data, i)
		if end = endOfToken(data, i); end == i {
			return "", ""
		}
		if field == 1 {
			service = data[i:end]
		}
		i = end
	}

	i = skipSpaces(data, i)
	switch {
	case i >= len(data):
		return "", ""
	case data[i] == '[':
		if i = skipStructuredSyslogData(data, i); i < 0 {
			return "", ""
		}
		i = skipSpaces(data, i)
	case data[i] == '-' && (i+1 == len(data) || spaceChars[data[i+1]]):
		i = skipSpaces(data, i+1)
	}

	return service, data[i:]
}

func isSyslog5424Timestamp(token string) bool {
	if token == "-" {
		return true
	}
	if len(token) < 19 || token[4] != '-' || token[7] != '-' || token[10] != 'T' {
		return false
	}
	for _, i := range [8]int{0, 1, 2, 3, 5, 6, 8, 9} {
		if !isDigit(token[i]) {
			return false
		}
	}

	return true
}

func skipStructuredSyslogData(data string, start int) int {
	i := start
	for i < len(data) && data[i] == '[' {
		for i++; i < len(data) && data[i] != ']'; i++ {
			if data[i] != '"' {
				continue
			}
			for i++; i < len(data) && data[i] != '"'; i++ {
				if data[i] == '\\' {
					i++
				}
			}
			if i >= len(data) {
				return -1
			}
		}
		if i >= len(data) {
			return -1
		}
		i++
	}

	return i
}
