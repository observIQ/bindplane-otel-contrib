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

// Package blobstream reads log records from a cloud storage object as a stream. It
// keeps a resumable position, so a partly read object continues where it stopped.
//
// It is the streaming counterpart to internal/blobconsume, which takes the whole
// object as a []byte. Only streaming supports mid-object resume.
//
// NewRecordProducer inspects the leading bytes and returns a RecordProducer. Drive it
// with one loop:
//
//	producer, err := blobstream.NewRecordProducer(ctx, stream, reader, onParseError)
//	records, err := producer.Records(ctx, startOffset)
//	for record, err := range records {
//	    ...
//	    producer.AppendLogBody(ctx, logRecord, record)
//	    checkpoint(producer.Position())
//	}
//
// Detection reads content, not names or content-type headers. Customers store data
// under a wrong extension, and object stores report application/octet-stream.
package blobstream // import "github.com/observiq/bindplane-otel-contrib/internal/blobstream"

import "context"

// ParseErrorFunc runs when the parser skips a record or an archive entry. Each
// receiver passes its own counter. A nil func disables counting.
type ParseErrorFunc func(ctx context.Context)
