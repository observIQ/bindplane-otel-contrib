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
	// Raw emits the original text as the body, not a parsed structure. It skips
	// parser selection, but keeps content detection, so binary content still fails.
	//
	// Avro OCF is the exception. It holds no original text, so raw mode emits the
	// JSON encoding of each record instead.
	Raw bool

	// IncludeLogRecordOriginal also writes the original text to the
	// log.record.original attribute. The body does not change.
	IncludeLogRecordOriginal bool
}

// setOriginal records original on lr when the option is enabled.
func (o BodyOptions) setOriginal(lr plog.LogRecord, original string) {
	if o.IncludeLogRecordOriginal {
		lr.Attributes().PutStr(logRecordOriginalAttribute, original)
	}
}
