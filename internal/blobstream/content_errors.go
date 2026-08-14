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

// IsUnsupportedContent reports that this package can never parse the object. The
// content type is unknown, the archive structure failed to decode, or the archive
// exceeded a bomb-safety limit.
//
// A retry gives the same result, so callers send the object to the dead-letter queue.
// Callers classify their own cloud provider errors.
func IsUnsupportedContent(err error) bool {
	if isUnusableContent(err) {
		return true
	}
	// A bomb limit applies to a whole archive, so it never ends a single entry.
	var archiveLimit ErrArchiveLimitExceeded
	return errors.As(err, &archiveLimit)
}

// isUnusableContent reports content that reads the same way on a retry. Both the
// object-level and the entry-level decisions rest on it, so the two stay in step as
// new error types arrive.
func isUnusableContent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotArrayOrKnownObject) || IsTruncatedObject(err) {
		return true
	}
	var unsupported ErrUnsupportedContent
	if errors.As(err, &unsupported) {
		return true
	}
	var corruptArchive ErrCorruptArchive
	if errors.As(err, &corruptArchive) {
		return true
	}
	var corruptContainer ErrCorruptContainer
	return errors.As(err, &corruptContainer)
}

// ErrCorruptContainer indicates an object matched a known format but its structure
// would not decode. A retry reads the same bytes, so the object goes to the dead-letter
// queue instead of being redelivered.
type ErrCorruptContainer struct {
	Format string
	Err    error
}

func (e ErrCorruptContainer) Error() string {
	return fmt.Sprintf("corrupt %s object: %v", e.Format, e.Err)
}

func (e ErrCorruptContainer) Unwrap() error { return e.Err }
