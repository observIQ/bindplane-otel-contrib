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

package blobstream

import "go.opentelemetry.io/collector/pdata/plog"

// logRecordOriginalAttribute holds the original text. It matches the attribute
// Bindplane generates for stanza-based sources.
const logRecordOriginalAttribute = "log.record.original"

// BodyOptions control how a parsed record becomes a log record body. Both default to
// false. The parser then parses what it can, and adds nothing.
type BodyOptions struct {
	// Raw emits each record's original text as the body instead of a parsed structure.
	// It uses the same parser selection and content detection as a normal read, so
	// records are split the same way and binary content still fails; only the body
	// rendering differs.
	//
	// Avro OCF is the exception. It holds no original text, so raw mode emits the
	// JSON encoding of each record instead.
	Raw bool

	// IncludeLogRecordOriginal also writes the original text to the
	// log.record.original attribute. The body does not change. With Raw also set, the body
	// and this attribute intentionally hold the same original text: the two options are
	// orthogonal, and duplicating the payload is the established pattern (it matches the
	// attribute stanza-based sources generate).
	IncludeLogRecordOriginal bool
}

// setOriginal records original on lr when the option is enabled.
func (o BodyOptions) setOriginal(lr plog.LogRecord, original string) {
	if o.IncludeLogRecordOriginal {
		lr.Attributes().PutStr(logRecordOriginalAttribute, original)
	}
}
