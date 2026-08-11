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

package logtypedetectionprocessor

var jsonStructuralChars [256]bool

func init() {
	for _, c := range []byte("{}[],:") {
		jsonStructuralChars[c] = true
	}
}

// fingerprintJSON creates a hash based off the structure of the log by
// walking the JSON tree. We want to keep the keys but discard the values,
// replacing them with placeholders.
func fingerprintJSON(data string) uint64 {
	hash := uint64(fnvOffsetBasis)
	curTokens := 0

	// Keeps track of brackets to ensure we
	// have valid JSON
	var open [maxLogDepth]byte
	depth := 0

	for i := 0; i < len(data); {
		c := data[i]
		switch {
		// Skip whitespace
		case spaceChars[c]:
			i++
		// Starting an array inside the json
		// curTokens >0 means we are not at the root of the json
		case c == '[' && curTokens > 0:
			end := endOfArray(data, i)
			if end < 0 {
				return 0
			}
			i = end
			hash = foldFNVHashString(hash, arrayPlaceholder)
			curTokens++
		case c == '{' || c == '[':
			if depth == maxLogDepth {
				return 0
			}
			open[depth] = c
			depth++
			hash = foldFNVHashByte(hash, c)
			curTokens++
			i++
		case c == '}' || c == ']':
			// If we're at the root of the document, we're not in a valid JSON document.
			if depth == 0 || (c == '}') != (open[depth-1] == '{') {
				return 0
			}
			depth--
			hash = foldFNVHashByte(hash, c)
			i++
		case jsonStructuralChars[c]:
			hash = foldFNVHashByte(hash, c)
			curTokens++
			i++
		// Start of a string.
		case c == '"':
			end, ok := endOfString(data, i)
			if !ok {
				return 0
			}
			// Jump to the end of the string
			j := end
			for j < len(data) && spaceChars[data[j]] {
				j++
			}
			if j < len(data) && data[j] == ':' {
				hash = foldFNVHashString(hash, data[i:end])
			} else {
				hash = foldFNVHashString(hash, textPlaceholder)
			}
			curTokens++
			i = end
		// This is some arbitrary value, boolean, number, etc.
		default:
			// Consume all the characters that aren't whitespace, structural characters, or quotes.
			for i < len(data) && !spaceChars[data[i]] && !jsonStructuralChars[data[i]] && data[i] != '"' {
				i++
			}
			hash = foldFNVHashString(hash, valuePlaceholder)
			curTokens++
		}
	}

	// If we haven't seen a closing bracket, we're not in a valid JSON document.
	if depth != 0 {
		return 0
	}

	return hash
}

// endOfString returns the index just past the '"' closing the string opening at
// start, reporting false if the string is unterminated.
func endOfString(data string, start int) (int, bool) {
	for i := start + 1; i < len(data); i++ {
		switch data[i] {
		// Escaped character in a string.
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}

	return 0, false
}

// endOfArray returns the index just past the ']' closing the array opening at
// start, or -1 if the array is never closed. This function assumes the
// array is valid json for simplicity and speed.
func endOfArray(data string, start int) int {
	depth := 0
	for i := start; i < len(data); i++ {
		switch data[i] {
		// Handle a string inside the array, this is used to ignore brackets inside strings.
		case '"':
			end, ok := endOfString(data, i)
			if !ok {
				return -1
			}
			i = end - 1
		case '[':
			depth++
		case ']':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}
