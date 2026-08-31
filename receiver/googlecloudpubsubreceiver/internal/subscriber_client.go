// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package internal // import "github.com/observiq/bindplane-otel-contrib/receiver/googlecloudpubsubreceiver/internal"

import (
	"context"

	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/googleapis/gax-go/v2"
)

// SubscriberClient is the subset of `pubsub.SubscriberClient` used by the stream handler.
type SubscriberClient interface {
	Close() error
	StreamingPull(ctx context.Context, opts ...gax.CallOption) (pubsubpb.Subscriber_StreamingPullClient, error)
	Acknowledge(ctx context.Context, req *pubsubpb.AcknowledgeRequest, opts ...gax.CallOption) error
	ModifyAckDeadline(ctx context.Context, req *pubsubpb.ModifyAckDeadlineRequest, opts ...gax.CallOption) error
}
