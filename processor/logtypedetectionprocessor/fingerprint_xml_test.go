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

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFingerprintXMLInvalid(t *testing.T) {
	cases := []string{
		"<",
		"<a>",
		"</a>",
		"<a></b>",
		"<a><b></a></b>",
		"<a>text",
		"text<a></a>",
		`<a b=unquoted></a>`,
		`<a b="unterminated></a>`,
		"<a><!-- unterminated <b></b></a>",
		"<a><![CDATA[unterminated]]<b></b></a>",
		"<?xml version='1.0'?",
		"<a>",
		"< a></a>",
		"<a/",
		strings.Repeat("<a>", maxLogDepth+1) + strings.Repeat("</a>", maxLogDepth+1),
	}

	for _, c := range cases {
		require.Zero(t, fingerprintXML(c), "expected no fingerprint for %q", c)
	}
}

func TestFingerprintValidXML(t *testing.T) {
	cases := []string{
		"<a/>",
		"<a></a>",
		"<a>text</a>",
		`<a b="1"/>`,
		`<a b="1" c='2'>text</a>`,
		`<?xml version="1.0"?><a><b>1</b></a>`,
		"<a><!-- comment --><b/></a>",
		"<a><![CDATA[<not a tag>]]></a>",
		"<!DOCTYPE a><a/>",
		"\n  <a>\n    <b/>\n  </a>\n  ",
		`<ns:a xmlns:ns="urn:x"><ns:b/></ns:a>`,
	}

	for _, c := range cases {
		require.NotZero(t, fingerprintXML(c), "expected a fingerprint for %q", c)
	}
}

func TestFingerprintXMLStructure(t *testing.T) {
	cases := []struct {
		title      string
		xmlA, xmlB string
		equal      bool
	}{
		{
			title: "different text and attribute values",
			xmlA:  `<e id="1"><v>abc</v></e>`,
			xmlB:  `<e id="99999"><v>totally different</v></e>`,
			equal: true,
		},
		{
			title: "whitespace",
			xmlA:  `<a><b>1</b></a>`,
			xmlB:  "<a>\n  <b>1</b>\n</a>",
			equal: true,
		},
		{
			title: "cdata and text",
			xmlA:  `<a><![CDATA[x]]></a>`,
			xmlB:  `<a>y</a>`,
			equal: true,
		},
		{
			title: "different element name",
			xmlA:  `<a><b/></a>`,
			xmlB:  `<a><c/></a>`,
			equal: false,
		},
		{
			title: "different attribute name",
			xmlA:  `<a b="1"/>`,
			xmlB:  `<a c="1"/>`,
			equal: false,
		},
		{
			title: "missing attribute",
			xmlA:  `<a b="1" c="2"/>`,
			xmlB:  `<a b="1"/>`,
			equal: false,
		},
		{
			title: "different nesting",
			xmlA:  `<a><b><c/></b></a>`,
			xmlB:  `<a><b/><c/></a>`,
			equal: true,
		},
		{
			title: "text vs empty",
			xmlA:  `<a>x</a>`,
			xmlB:  `<a></a>`,
			equal: true,
		},
		{
			title: "self closing vs empty",
			xmlA:  `<a><b/></a>`,
			xmlB:  `<a><b></b></a>`,
			equal: true,
		},
		{
			title: "populated and blank sibling fields",
			xmlA:  `<d><f n="a">x</f><f n="b"></f></d>`,
			xmlB:  `<d><f n="a"></f><f n="b">y</f></d>`,
			equal: true,
		},
		{
			title: "repeated sibling count",
			xmlA:  `<a><b>1</b></a>`,
			xmlB:  `<a><b>1</b><b>2</b><b>3</b></a>`,
			equal: true,
		},
		{
			title: "run of attributed siblings",
			xmlA:  `<d><f n="1"/><f n="2"/></d>`,
			xmlB:  `<d><f n="1"/></d>`,
			equal: true,
		},
		{
			title: "run of attributed siblings vs none",
			xmlA:  `<d><f n="1"/><f n="2"/></d>`,
			xmlB:  `<d/>`,
			equal: false,
		},
		{
			title: "same attribute name on different elements",
			xmlA:  `<r><a n="1"/><b/></r>`,
			xmlB:  `<r><a/><b n="1"/></r>`,
			equal: false,
		},
	}

	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			fingerprintA := fingerprintXML(c.xmlA)
			fingerprintB := fingerprintXML(c.xmlB)
			require.NotZero(t, fingerprintA)
			if c.equal {
				require.Equal(t, fingerprintA, fingerprintB)
			} else {
				require.NotEqual(t, fingerprintA, fingerprintB)
			}
		})
	}
}
