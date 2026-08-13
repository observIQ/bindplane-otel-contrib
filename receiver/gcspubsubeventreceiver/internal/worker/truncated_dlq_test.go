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

// TestTruncatedObjectRoutesToDLQ asserts a truncated object is a dead-letter condition.
// Redelivering it reads the same bytes, so retrying never drains the queue.
func TestTruncatedObjectRoutesToDLQ(t *testing.T) {
	t.Parallel()

	err := error(blobstream.ErrTruncatedObject{Err: io.ErrUnexpectedEOF})
	require.True(t, isDLQConditionError(err), "a truncated object must be a DLQ condition")
	require.True(t, isDLQConditionError(fmt.Errorf("parse logs: %w", err)),
		"the condition must survive wrapping")
}
