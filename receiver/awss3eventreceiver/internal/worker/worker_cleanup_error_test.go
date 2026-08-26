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
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
)

// deleteErrStorage fails every offset delete, so the delete-error path in deleteMessage runs.
type deleteErrStorage struct{ err error }

func (s deleteErrStorage) SaveStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (s deleteErrStorage) LoadStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (s deleteErrStorage) DeleteStorageData(context.Context, string) error { return s.err }
func (s deleteErrStorage) Close(context.Context) error                     { return nil }

// TestDeleteMessage_LogsOffsetDeleteFailure asserts that when the ack succeeds but deleting
// a processed key's offset fails, the failure is logged rather than dropped. The stale
// offset is harmless (the object is already acked), so the delete error must not escalate.
func TestDeleteMessage_LogsOffsetDeleteFailure(t *testing.T) {
	t.Parallel()

	mockSQS := &mocks.MockSQSClient{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockSQS.EXPECT().DeleteMessage(mock.Anything, mock.Anything).Return(&sqs.DeleteMessageOutput{}, nil)

	core, logs := observer.New(zap.ErrorLevel)
	w := &Worker{
		client:        mockClient,
		offsetStorage: deleteErrStorage{err: errors.New("storage extension unavailable")},
	}

	w.deleteMessage(context.Background(), types.Message{ReceiptHandle: aws.String("rh")},
		"https://sqs.example/q", []string{"mykey"}, zap.New(core))

	entries := logs.FilterMessage("Failed to delete offset").All()
	require.Len(t, entries, 1, "a failed offset delete is logged once")
	require.Equal(t, OffsetStorageKey+"_mykey", entries[0].ContextMap()["offset_storage_key"])
}

// TestHandleProcessingError_LogsNackFailureOnCancellation asserts that when processing is
// cancelled and the follow-up nack (visibility reset) also fails, the nack failure is
// logged. The message stays invisible until its timeout lapses, then redelivers.
func TestHandleProcessingError_LogsNackFailureOnCancellation(t *testing.T) {
	t.Parallel()

	nackErr := errors.New("visibility reset failed")
	mockSQS := &mocks.MockSQSClient{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().SQS().Return(mockSQS)
	mockSQS.EXPECT().ChangeMessageVisibility(mock.Anything, mock.Anything).
		Return(&sqs.ChangeMessageVisibilityOutput{}, nackErr)

	core, logs := observer.New(zap.ErrorLevel)
	w := &Worker{client: mockClient}

	w.handleProcessingError(context.Background(), types.Message{ReceiptHandle: aws.String("rh")},
		"https://sqs.example/q", context.Canceled, zap.New(core))

	require.Equal(t, 1, logs.FilterMessage("failed to nack message after cancellation").Len(),
		"a nack failure after cancellation is logged")
}
