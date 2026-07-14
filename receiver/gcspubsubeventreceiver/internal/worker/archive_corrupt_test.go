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

package worker

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestErrCorruptArchive_IsDLQCondition verifies a corrupt-archive error is
// classified as the unsupported-file DLQ condition (not a generic transient
// failure) and unwraps to its cause.
func TestErrCorruptArchive_IsDLQCondition(t *testing.T) {
	t.Parallel()

	cause := errors.New("bad header")
	err := ErrCorruptArchive{Type: "tar", Err: cause}

	require.Equal(t, dlqErrorKindUnsupportedFile, dlqConditionKind(err))
	require.True(t, isDLQConditionError(err))
	require.ErrorIs(t, err, cause)

	// Still classified when wrapped by the producer's "open archive" context.
	wrapped := fmt.Errorf("open archive: %w", err)
	require.Equal(t, dlqErrorKindUnsupportedFile, dlqConditionKind(wrapped))
}
