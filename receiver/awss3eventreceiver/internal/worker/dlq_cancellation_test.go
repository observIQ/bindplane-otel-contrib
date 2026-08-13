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

// Internal test file — uses package worker to access unexported symbols.
package worker

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsDLQConditionError_CancellationIsNeverDLQ(t *testing.T) {
	t.Parallel()

	// A config push cancels the context mid-object. Routing that to the dead-letter
	// queue would send good data to the DLQ on every config change. The check matters
	// here in particular because the classifiers fall back to substring matching on the
	// error text.
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("read object: %w", context.Canceled),
		fmt.Errorf("read object: %w", context.DeadlineExceeded),
	} {
		require.Nil(t, isDLQConditionError(err), "err: %v", err)
	}
}
