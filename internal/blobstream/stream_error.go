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

import (
	"errors"
	"fmt"
)

// ErrStreamRead marks a failure to read an object to completion. The bytes after the
// break never reached the consumer, and a later attempt can still read them.
//
// It separates a broken stream from a record the parser cannot use. A bad record is
// skipped, because retrying it gives the same result. A broken stream fails the whole
// object, so the message stays queued and resumes from the saved offset.
type ErrStreamRead struct {
	Err error
}

func (e ErrStreamRead) Error() string {
	return fmt.Sprintf("read object stream: %v", e.Err)
}

func (e ErrStreamRead) Unwrap() error { return e.Err }

// IsStreamRead reports that the object could not be read to completion.
func IsStreamRead(err error) bool {
	var streamRead ErrStreamRead
	return errors.As(err, &streamRead)
}

// ErrTruncatedObject marks an object whose bytes all arrived (a clean read to its known
// size) but whose content ends mid-record. A broken or short download is a retryable
// ErrStreamRead instead; only this residual case, where a retry reads the same bytes,
// becomes an ErrTruncatedObject. It delivers the records read so far and is acked.
type ErrTruncatedObject struct {
	Err error
}

func (e ErrTruncatedObject) Error() string {
	return fmt.Sprintf("object ends mid-record: %v", e.Err)
}

func (e ErrTruncatedObject) Unwrap() error { return e.Err }

// IsTruncatedObject reports that the object ends part way through a record.
func IsTruncatedObject(err error) bool {
	var truncated ErrTruncatedObject
	return errors.As(err, &truncated)
}
