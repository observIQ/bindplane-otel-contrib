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

package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestDownstreamConnectionCloseBeforeStart is a regression test for PIPE-1237. A downstream
// connection can be closed in the window between being registered and its start goroutine
// running. Because ctx and cancel are created at construction, close() must be safe and must
// cancel the connection context without depending on start() having run.
func TestDownstreamConnectionCloseBeforeStart(t *testing.T) {
	c := newDownstreamConnection(context.Background(), nil, nil, nil, "test", zap.NewNop())

	require.NoError(t, c.close())

	select {
	case <-c.ctx.Done():
	default:
		t.Fatal("expected connection context to be cancelled after close before start")
	}
}
