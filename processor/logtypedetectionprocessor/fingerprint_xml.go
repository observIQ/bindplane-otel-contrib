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

import "strings"

var nameEnd [256]bool

func init() {
	for _, c := range []byte(" \t\n\r/>=") {
		nameEnd[c] = true
	}
}

// fingerprintXML creates a hash based off the structure of the log by walking
// the XML tree. We keep element and attribute names but discard text content
// and attribute values, replacing them with placeholders.
func fingerprintXML(data string) uint64 {
	hash := uint64(fnvOffsetBasis)
	elems := 0

	// Keeps track of open element names to ensure the tags are balanced.
	var open [maxLogDepth]string
	depth := 0

	for i := 0; i < len(data); {
		// Character data between tags.
		if data[i] != '<' {
			end, text := endOfText(data, i)
			if text {
				if depth == 0 {
					return 0
				}
				hash = foldFNVHashString(hash, textPlaceholder)
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
			if i, cdata = skipMarkup(data, i); cdata {
				if depth == 0 {
					return 0
				}
				hash = foldFNVHashString(hash, textPlaceholder)
			}
		// Closing tag, the name must match the element we opened.
		case '/':
			name, end := elementName(data, i+2)
			if depth == 0 || open[depth-1] != name {
				return 0
			}
			depth--
			hash = foldFNVHashString(hash, `</`)
			i = endOfTag(data, end)
		default:
			name, end := elementName(data, i+1)
			if name == "" {
				return 0
			}
			hash = foldFNVHashByte(hash, '<')
			hash = foldFNVHashString(hash, name)
			elems++

			var selfClosing bool
			hash, i, selfClosing = hashAttributes(hash, data, end)
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

	return hash
}

// endOfText returns the index of the next tag at or after start, and whether
// the character data before it was more than whitespace.
func endOfText(data string, start int) (int, bool) {
	text := false
	i := start
	for ; i < len(data) && data[i] != '<'; i++ {
		text = text || !space[data[i]]
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
	for i < len(data) && !nameEnd[data[i]] {
		i++
	}

	return data[start:i], i
}

// hashAttributes folds every attribute name, and a placeholder for its value,
// into hash. It returns the index just past the tag and whether the tag was
// self closing, or a negative index if the tag is malformed.
func hashAttributes(hash uint64, data string, start int) (uint64, int, bool) {
	for i := start; ; {
		for i < len(data) && space[data[i]] {
			i++
		}
		if i >= len(data) {
			return hash, -1, false
		}

		switch data[i] {
		case '>':
			return hash, i + 1, false
		case '/':
			if i+1 >= len(data) || data[i+1] != '>' {
				return hash, -1, false
			}
			return foldFNVHashString(hash, `/>`), i + 2, true
		}

		name, end := elementName(data, i)
		if name == "" {
			return hash, -1, false
		}
		hash = foldFNVHashString(hash, name)

		for end < len(data) && space[data[end]] {
			end++
		}
		if end < len(data) && data[end] == '=' {
			if end = endOfAttrValue(data, end+1); end < 0 {
				return hash, -1, false
			}
			hash = foldFNVHashString(hash, valuePlaceholder)
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
	for i < len(data) && space[data[i]] {
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
