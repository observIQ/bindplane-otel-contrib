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
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/observiq/bindplane-otel-contrib/internal/blobstream"
)

// TestTruncatedObjectIsNotADLQCondition asserts a truncated object is not a dead-letter
// condition. The records read before the cut are delivered and the object is acked, so it
// must not be routed to the DLQ.
func TestTruncatedObjectIsNotADLQCondition(t *testing.T) {
	t.Parallel()

	err := error(blobstream.ErrTruncatedObject{Err: io.ErrUnexpectedEOF})
	require.False(t, isDLQConditionError(err), "a truncated object is delivered and acked, not dead-lettered")
	require.False(t, isDLQConditionError(fmt.Errorf("parse logs: %w", err)),
		"the classification must survive wrapping")
}
