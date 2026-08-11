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

var xmlEndingChars [256]bool

func init() {
	for _, c := range []byte(" \t\n\r/>=") {
		xmlEndingChars[c] = true
	}
}

// fingerprintXML creates a hash based off the structure of the log by walking
// the XML tree. We keep element and attribute names but discard text content and
// attribute values, and skip a sibling repeating the tag we just saw.
func fingerprintXML(data string) uint64 {
	var lastElem uint64
	hash := uint64(fnvOffsetBasis)
	elems := 0

	// Keeps track of open element names to ensure the tags are balanced.
	var open [maxLogDepth]string
	depth := 0

	for i := 0; i < len(data); {
		// Character data between tags.
		if data[i] != '<' {
			end, text := endOfText(data, i)
			if text && depth == 0 {
				return 0
			}
			i = end
			continue
		}
		if i+1 >= len(data) {
			return 0
		}

		switch data[i+1] {
		// Declaration or processing instruction.
		case '?':
			i = indexAfter(data, i+2, "?>")
		// Comment, CDATA section or doctype.
		case '!':
			var cdata bool
			if i, cdata = skipMarkup(data, i); cdata && depth == 0 {
				return 0
			}
		// Closing tag, the name must match the element we opened.
		case '/':
			name, end := elementName(data, i+2)
			if depth == 0 || open[depth-1] != name {
				return 0
			}
			depth--
			i = endOfTag(data, end)
		default:
			name, end := elementName(data, i+1)
			if name == "" {
				return 0
			}
			elems++

			var elem uint64
			var selfClosing bool
			elem, i, selfClosing = hashElement(data, end, name)
			if elem != lastElem {
				hash = foldFNVHashUint(hash, elem)
				lastElem = elem
			}
			if !selfClosing {
				if depth == maxLogDepth {
					return 0
				}
				open[depth] = name
				depth++
			}
		}

		// Every branch above reports a malformed tag as a negative index.
		if i < 0 {
			return 0
		}
	}

	if depth != 0 || elems == 0 {
		return 0
	}
	if hash == 0 {
		return fnvOffsetBasis
	}

	return hash
}

// endOfText returns the index of the next tag at or after start, and whether
// the character data before it was more than whitespace.
func endOfText(data string, start int) (int, bool) {
	text := false
	i := start
	for ; i < len(data) && data[i] != '<'; i++ {
		text = text || !spaceChars[data[i]]
	}

	return i, text
}

// skipMarkup returns the index just past the comment, CDATA section or doctype
// opening at start, and whether it held character data.
func skipMarkup(data string, start int) (int, bool) {
	switch {
	case strings.HasPrefix(data[start:], "<!--"):
		return indexAfter(data, start+4, "-->"), false
	case strings.HasPrefix(data[start:], "<![CDATA["):
		return indexAfter(data, start+9, "]]>"), true
	default:
		return indexAfter(data, start+2, ">"), false
	}
}

// elementName returns the name starting at start and the index just past it.
func elementName(data string, start int) (string, int) {
	i := start
	for i < len(data) && !xmlEndingChars[data[i]] {
		i++
	}

	return data[start:i], i
}

// hashElement hashes the element name together with every attribute name on it. It
// returns the index just past the tag and whether the tag was self closing, or a
// negative index if the tag is malformed.
func hashElement(data string, start int, name string) (uint64, int, bool) {
	elem := foldFNVHashString(fnvOffsetBasis, name)

	for i := start; ; {
		for i < len(data) && spaceChars[data[i]] {
			i++
		}
		if i >= len(data) {
			return elem, -1, false
		}

		switch data[i] {
		case '>':
			return elem, i + 1, false
		case '/':
			if i+1 >= len(data) || data[i+1] != '>' {
				return elem, -1, false
			}
			return elem, i + 2, true
		}

		attr, end := elementName(data, i)
		if attr == "" {
			return elem, -1, false
		}
		elem = foldFNVHashString(elem, attr)

		for end < len(data) && spaceChars[data[end]] {
			end++
		}
		if end < len(data) && data[end] == '=' {
			if end = endOfAttrValue(data, end+1); end < 0 {
				return elem, -1, false
			}
		}
		i = end
	}
}

// endOfTag returns the index just past the '>' closing the tag, or -1 if the
// tag is never closed.
func endOfTag(data string, start int) int {
	for i := start; i < len(data); i++ {
		if data[i] == '>' {
			return i + 1
		}
	}

	return -1
}

// endOfAttrValue returns the index just past the closing quote of the value
// starting at start, or -1 if the value is unquoted or unterminated.
func endOfAttrValue(data string, start int) int {
	i := start
	for i < len(data) && spaceChars[data[i]] {
		i++
	}
	if i >= len(data) || (data[i] != '"' && data[i] != '\'') {
		return -1
	}

	end := strings.IndexByte(data[i+1:], data[i])
	if end < 0 {
		return -1
	}

	return i + end + 2
}

// indexAfter returns the index just past the next occurrence of sep at or after
// start, or -1 if sep never occurs.
func indexAfter(data string, start int, sep string) int {
	end := strings.Index(data[start:], sep)
	if end < 0 {
		return -1
	}

	return start + end + len(sep)
}
