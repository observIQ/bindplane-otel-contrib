// Copyright  observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package throughputmeasurementprocessor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeliveryTracker_NilSafe(t *testing.T) {
	var tracker *deliveryTracker
	require.NotPanics(t, tracker.markDelivered)
	require.False(t, tracker.wasDelivered())
}

func TestDeliveryTracker_Mark(t *testing.T) {
	tracker := &deliveryTracker{}
	require.False(t, tracker.wasDelivered())

	tracker.markDelivered()
	require.True(t, tracker.wasDelivered())
}

func TestDeliveryTracker_Context(t *testing.T) {
	require.Nil(t, trackerFromContext(context.Background()))

	tracker := &deliveryTracker{}
	ctx := contextWithTracker(context.Background(), tracker)
	require.Same(t, tracker, trackerFromContext(ctx))

	// A nested tracker hides the outer one.
	inner := &deliveryTracker{}
	require.Same(t, inner, trackerFromContext(contextWithTracker(ctx, inner)))
}
