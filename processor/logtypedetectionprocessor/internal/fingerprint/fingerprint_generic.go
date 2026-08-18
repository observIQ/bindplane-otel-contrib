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

// fingerprintGeneric hashes key=value, CSV and plain text logs. It is a best guess as formats
// like key=value, CSV and plain text are not always consistent.
func fingerprintGeneric(data string) uint64 {
	if res := fingerprintKV(data); res != 0 {
		return res
	}
	if res := fingerprintCSV(data); res != 0 {
		return res
	}

	return fingerprintText(data)
}

// fingerprintKV hashes a log made entirely of key=value pairs, keeping only
// the keys.
func fingerprintKV(data string) uint64 {
	hash := uint64(fnvOffsetBasis)

	for i := 0; i < len(data); {
		if spaceChars[data[i]] || data[i] == ',' {
			i++
			continue
		}
		start := i
		for i < len(data) && data[i] != '=' && data[i] != ',' && !spaceChars[data[i]] {
			i++
		}
		if i == start || i >= len(data) || data[i] != '=' {
			return 0
		}
		hash = foldFNVHashString(hash, data[start:i+1])
		i = endOfValue(data, i+1)
	}

	return hash
}

// fingerprintCSV hashes a log with at least four comma separated fields,
// replacing each field with its type.
func fingerprintCSV(data string) uint64 {
	hash := uint64(fnvOffsetBasis)
	fields := 0

	for i := 0; ; {
		digit := false
		if i < len(data) && data[i] == '"' {
			if end, ok := endOfString(data, i); ok {
				i = end
			}
		}
		for i < len(data) && data[i] != ',' {
			digit = digit || isDigit(data[i])
			i++
		}
		if digit {
			hash = foldFNVHashString(hash, valuePlaceholder)
		} else {
			hash = foldFNVHashString(hash, textPlaceholder)
		}
		fields++
		if i >= len(data) {
			break
		}
		hash = foldFNVHashByte(hash, ',')
		i++
	}

	if fields < 4 {
		return 0
	}

	return hash
}

// fingerprintText hashes a plain text log, keeping raw text but replacing
// digit-bearing tokens, quoted strings and key=value values with placeholders.
func fingerprintText(data string) uint64 {
	hash := uint64(fnvOffsetBasis)

	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case spaceChars[c]:
			hash = foldFNVHashByte(hash, ' ')
			i = skipSpaces(data, i)
		case c == ',':
			hash = foldFNVHashByte(hash, c)
			i++
		case c == '"':
			end, ok := endOfString(data, i)
			if !ok {
				hash, i = foldTextToken(hash, data, i)
				continue
			}
			hash = foldFNVHashString(hash, textPlaceholder)
			i = end
		default:
			hash, i = foldTextToken(hash, data, i)
		}
	}

	return hash
}

// foldTextToken mixes the token starting at start into hash and returns the
// index just past it. Keys of key=value pairs and digit-free text are kept
// as-is; everything else becomes a placeholder.
func foldTextToken(hash uint64, data string, start int) (uint64, int) {
	i := start
	for i < len(data) && !spaceChars[data[i]] && data[i] != ',' {
		if data[i] == '=' {
			hash = foldFNVHashString(hash, data[start:i+1])
			end := endOfValue(data, i+1)
			return foldFNVHashString(hash, valuePlaceholder), end
		}
		i++
	}

	digit := false
	for j := start; j < i; j++ {
		if isDigit(data[j]) {
			digit = true
			break
		}
	}
	if digit {
		hash = foldFNVHashString(hash, valuePlaceholder)
	} else {
		hash = foldFNVHashString(hash, data[start:i])
	}

	return hash, i
}

// endOfValue returns the index just past the possibly quoted value starting at
// start.
func endOfValue(data string, start int) int {
	if start < len(data) && data[start] == '"' {
		if end, ok := endOfString(data, start); ok {
			return end
		}
	}

	i := start
	for i < len(data) && !spaceChars[data[i]] && data[i] != ',' {
		i++
	}

	return i
}
