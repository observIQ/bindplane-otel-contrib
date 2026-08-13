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

var alnumChars [256]bool

func init() {
	for c := byte('0'); c <= '9'; c++ {
		alnumChars[c] = true
	}
	for c := byte('a'); c <= 'z'; c++ {
		alnumChars[c] = true
	}
	for c := byte('A'); c <= 'Z'; c++ {
		alnumChars[c] = true
	}
}

// fingerprintSyslog creates a hash based off an RFC 3164 or RFC 5424 syslog
// message. Header values become placeholders, the app name, msgid and
// structured data names are kept, and message words containing a digit become
// placeholders.
func fingerprintSyslog(data string) uint64 {
	i := skipSyslogPriority(data)
	if i < 0 {
		return 0
	}
	if strings.HasPrefix(data[i:], "1 ") {
		return fingerprintSyslog5424(data[i+2:])
	}

	return fingerprintSyslog3164(data[i:])
}

// skipSyslogPriority returns the index just past the "<pri>" prefix, or -1 if
// the prefix is missing or the priority is out of range.
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

// fingerprintSyslog3164 hashes the message after the priority. The timestamp
// and hostname become placeholders and the remainder, tag included, is
// normalized.
func fingerprintSyslog3164(data string) uint64 {
	if len(data) < 17 || !isSyslog3164Timestamp(data) {
		return 0
	}
	hash := foldFNVHashString(fnvOffsetBasis, valuePlaceholder)

	i := skipSpaces(data, 16)
	end := endOfToken(data, i)
	if end == i {
		return 0
	}
	hash = foldFNVHashString(hash, textPlaceholder)

	i = skipSpaces(data, end)
	if i == len(data) {
		return 0
	}

	return foldNormalizedMessage(hash, data[i:])
}

// isSyslog3164Timestamp reports whether data starts with a "Jan  2 15:04:05 "
// timestamp.
func isSyslog3164Timestamp(data string) bool {
	if data[3] != ' ' || data[6] != ' ' || data[9] != ':' || data[12] != ':' || data[15] != ' ' {
		return false
	}
	for _, i := range [3]int{0, 1, 2} {
		if !alnumChars[data[i]] || isDigit(data[i]) {
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

// fingerprintSyslog5424 hashes the message after the "<pri>1 " prefix. The
// timestamp, hostname and procid become placeholders while the app name and
// msgid are kept. A missing structured data field is tolerated.
func fingerprintSyslog5424(data string) uint64 {
	hash := uint64(fnvOffsetBasis)

	end := endOfToken(data, 0)
	if !isSyslog5424Timestamp(data[:end]) {
		return 0
	}
	hash = foldFNVHashString(hash, valuePlaceholder)

	// Hostname.
	i := skipSpaces(data, end)
	if end = endOfToken(data, i); end == i {
		return 0
	}
	hash = foldFNVHashString(hash, textPlaceholder)

	// App name, kept in the hash.
	i = skipSpaces(data, end)
	if end = endOfToken(data, i); end == i {
		return 0
	}
	hash = foldFNVHashString(hash, data[i:end])

	// Procid.
	i = skipSpaces(data, end)
	if end = endOfToken(data, i); end == i {
		return 0
	}
	hash = foldFNVHashString(hash, valuePlaceholder)

	// Msgid, kept in the hash.
	i = skipSpaces(data, end)
	if end = endOfToken(data, i); end == i {
		return 0
	}
	hash = foldFNVHashString(hash, data[i:end])

	i = skipSpaces(data, end)
	switch {
	case i >= len(data):
		return 0
	case data[i] == '[':
		if hash, i = foldStructuredData(hash, data, i); i < 0 {
			return 0
		}
		i = skipSpaces(data, i)
	case data[i] == '-' && (i+1 == len(data) || spaceChars[data[i+1]]):
		i = skipSpaces(data, i+1)
	}

	return foldNormalizedMessage(hash, data[i:])
}

// isSyslog5424Timestamp reports whether token is a nil value or looks like an
// RFC 3339 timestamp.
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

// foldStructuredData mixes the structured data ids and param names into hash,
// discarding param values. It returns the index just past the last element,
// or -1 if an element is malformed.
func foldStructuredData(hash uint64, data string, start int) (uint64, int) {
	i := start
	for i < len(data) && data[i] == '[' {
		j := i + 1
		for j < len(data) && data[j] != ' ' && data[j] != ']' {
			j++
		}
		if j == i+1 || j >= len(data) {
			return hash, -1
		}
		hash = foldFNVHashString(hash, data[i+1:j])
		i = j

		for {
			i = skipSpaces(data, i)
			if i >= len(data) {
				return hash, -1
			}
			if data[i] == ']' {
				i++
				break
			}

			j = i
			for j < len(data) && data[j] != '=' && data[j] != ' ' && data[j] != ']' {
				j++
			}
			if j == i || j >= len(data) || data[j] != '=' {
				return hash, -1
			}
			hash = foldFNVHashString(hash, data[i:j])
			hash = foldFNVHashString(hash, valuePlaceholder)

			j++
			if j >= len(data) || data[j] != '"' {
				return hash, -1
			}
			for j++; j < len(data) && data[j] != '"'; j++ {
				if data[j] == '\\' {
					j++
				}
			}
			if j >= len(data) {
				return hash, -1
			}
			i = j + 1
		}
	}

	return hash, i
}

// foldNormalizedMessage mixes the message into hash. Words containing a digit
// become placeholders and whitespace runs collapse to a single space.
func foldNormalizedMessage(hash uint64, msg string) uint64 {
	for i := 0; i < len(msg); {
		c := msg[i]
		if spaceChars[c] {
			hash = foldFNVHashByte(hash, ' ')
			i = skipSpaces(msg, i)
			continue
		}
		if !alnumChars[c] {
			hash = foldFNVHashByte(hash, c)
			i++
			continue
		}

		digit := false
		j := i
		for ; j < len(msg) && alnumChars[msg[j]]; j++ {
			digit = digit || isDigit(msg[j])
		}
		if digit {
			hash = foldFNVHashString(hash, valuePlaceholder)
		} else {
			hash = foldFNVHashString(hash, msg[i:j])
		}
		i = j
	}

	return hash
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// endOfToken returns the index just past the whitespace-delimited token
// starting at start.
func endOfToken(data string, start int) int {
	i := start
	for i < len(data) && !spaceChars[data[i]] {
		i++
	}

	return i
}

// skipSpaces returns the index of the first non-whitespace character at or
// after start.
func skipSpaces(data string, start int) int {
	i := start
	for i < len(data) && spaceChars[data[i]] {
		i++
	}

	return i
}
