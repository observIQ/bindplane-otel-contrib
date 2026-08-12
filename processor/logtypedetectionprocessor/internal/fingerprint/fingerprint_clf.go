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

var clfDelimChars [256]bool

func init() {
	for _, c := range []byte(" /?&=:.,;-") {
		clfDelimChars[c] = true
	}
}

// fingerprintCLF creates a hash based off a Common Log Format access log. CLF
// has the same structure regardless of which server emitted it, so the quoted
// request line is what identifies the log. We keep the request text but
// discard every other field, replacing it with a placeholder.
func fingerprintCLF(data string) uint64 {
	hash := uint64(fnvOffsetBasis)

	// host, identity and authuser.
	i := 0
	for range 3 {
		end := endOfToken(data, i)
		if end == i {
			return 0
		}
		hash = foldFNVHashString(hash, textPlaceholder)
		i = skipSpaces(data, end)
	}

	// Bracketed timestamp.
	if i >= len(data) || data[i] != '[' {
		return 0
	}
	end := indexAfter(data, i+1, "]")
	if end < 0 {
		return 0
	}
	hash = foldFNVHashString(hash, valuePlaceholder)
	i = skipSpaces(data, end)

	// Quoted request line, the only field kept in the hash.
	end = endOfQuotedField(data, i)
	if end < 0 {
		return 0
	}
	hash = foldNormalizedRequest(hash, data[i+1:end-1])
	i = skipSpaces(data, end)

	// Status and bytes.
	for range 2 {
		end = endOfNumberField(data, i)
		if end < 0 {
			return 0
		}
		hash = foldFNVHashString(hash, valuePlaceholder)
		i = skipSpaces(data, end)
	}

	if i == len(data) {
		return hash
	}

	// Quoted referer and user agent of the combined format.
	for range 2 {
		end = endOfQuotedField(data, i)
		if end < 0 {
			return 0
		}
		hash = foldFNVHashString(hash, textPlaceholder)
		i = skipSpaces(data, end)
	}

	if i != len(data) {
		return 0
	}

	return hash
}

// foldNormalizedRequest mixes the request line into hash. It replaces every
// segment containing a digit with a placeholder so paths with IDs get the
// same fingerprint.
func foldNormalizedRequest(hash uint64, request string) uint64 {
	for i := 0; i < len(request); {
		c := request[i]
		if clfDelimChars[c] {
			hash = foldFNVHashByte(hash, c)
			i++
			continue
		}

		digit := false
		j := i
		for j < len(request) && !clfDelimChars[request[j]] {
			digit = digit || (request[j] >= '0' && request[j] <= '9')
			j++
		}
		if digit {
			hash = foldFNVHashString(hash, valuePlaceholder)
		} else {
			hash = foldFNVHashString(hash, request[i:j])
		}
		i = j
	}

	return hash
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

// endOfQuotedField returns the index just past the quoted field starting at
// start, or -1 if the field is missing or unterminated.
func endOfQuotedField(data string, start int) int {
	if start >= len(data) || data[start] != '"' {
		return -1
	}

	end, ok := endOfString(data, start)
	if !ok {
		return -1
	}

	return end
}

// endOfNumberField returns the index just past the numeric field starting at
// start, or -1 if the field is missing or not a number. A lone '-' is allowed.
func endOfNumberField(data string, start int) int {
	end := endOfToken(data, start)
	if end == start {
		return -1
	}
	if data[start] == '-' {
		if end != start+1 {
			return -1
		}
		return end
	}
	for i := start; i < end; i++ {
		if data[i] < '0' || data[i] > '9' {
			return -1
		}
	}

	return end
}
