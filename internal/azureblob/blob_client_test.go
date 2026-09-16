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

package azureblob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestIsRangeNotSatisfiable(t *testing.T) {
	require.True(t, IsRangeNotSatisfiable(&azcore.ResponseError{ErrorCode: string(bloberror.InvalidRange)}),
		"an Azure InvalidRange response is recognized")
	require.False(t, IsRangeNotSatisfiable(&azcore.ResponseError{ErrorCode: string(bloberror.BlobNotFound)}),
		"a different Azure error is not treated as invalid-range")
	require.False(t, IsRangeNotSatisfiable(errors.New("plain error")))
	require.False(t, IsRangeNotSatisfiable(nil))
}

func TestIsBlobNotFound(t *testing.T) {
	require.True(t, IsBlobNotFound(&azcore.ResponseError{ErrorCode: string(bloberror.BlobNotFound)}),
		"an Azure BlobNotFound response is recognized")
	require.True(t, IsBlobNotFound(&azcore.ResponseError{ErrorCode: string(bloberror.ResourceNotFound)}),
		"ResourceNotFound (HNS/dfs missing blob) is also treated as not-found")
	require.False(t, IsBlobNotFound(&azcore.ResponseError{ErrorCode: string(bloberror.InvalidRange)}),
		"a different Azure error is not treated as not-found")
	require.False(t, IsBlobNotFound(errors.New("plain error")))
	require.False(t, IsBlobNotFound(nil))
}

func TestIsPermanentError(t *testing.T) {
	// Auth / account-config failures are permanent: they will not clear on retry.
	for _, code := range []bloberror.Code{
		bloberror.AuthenticationFailed,
		bloberror.AuthorizationFailure,
		bloberror.AuthorizationPermissionMismatch,
		bloberror.InsufficientAccountPermissions,
		bloberror.InvalidAuthenticationInfo,
		bloberror.AccountIsDisabled,
		bloberror.ContainerNotFound,
		bloberror.InvalidResourceName,
	} {
		require.True(t, IsPermanentError(&azcore.ResponseError{ErrorCode: string(code)}),
			"auth/config code %q is permanent", code)
	}
	// Throttling, transient service errors, plain errors, and nil are not permanent, so the
	// caller keeps retrying instead of quarantining a still-readable blob.
	require.False(t, IsPermanentError(&azcore.ResponseError{ErrorCode: string(bloberror.ResourceNotFound)}),
		"a missing blob is not-found, not permanent, so it ages out silently")
	require.False(t, IsPermanentError(&azcore.ResponseError{ErrorCode: string(bloberror.ServerBusy)}),
		"throttling is transient, not permanent")
	require.False(t, IsPermanentError(&azcore.ResponseError{ErrorCode: string(bloberror.InternalError)}),
		"a transient service error is not permanent")
	require.False(t, IsPermanentError(errors.New("plain error")))
	require.False(t, IsPermanentError(nil))
}

func TestNewAzureBlobClient(t *testing.T) {
	tests := []struct {
		name          string
		connectionStr string
		batchSize     int
		pageSize      int
		expectedError bool
	}{
		{
			name:          "Invalid connection string",
			connectionStr: "invalid",
			batchSize:     100,
			pageSize:      1000,
			expectedError: true,
		},
		{
			name:          "Valid connection string",
			connectionStr: "DefaultEndpointsProtocol=https;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;",
			batchSize:     100,
			pageSize:      1000,
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewAzureBlobClient(tt.connectionStr, tt.batchSize, tt.pageSize, zap.NewNop())
			if tt.expectedError {
				require.Error(t, err)
				require.Nil(t, client)
			} else {
				require.NoError(t, err)
				require.NotNil(t, client)
			}
		})
	}
}

func TestDownloadBlob(t *testing.T) {
	// Create a mock Azure client using testify/mock
	mockClient := &mockAzureClient{}

	client := &AzureClient{
		azClient:  mockClient,
		logger:    zap.NewNop(),
		batchSize: 100,
		pageSize:  1000,
	}

	ctx := context.Background()
	container := "testcontainer"
	blobPath := "test/blob.txt"
	testData := []byte("test data content")
	buf := make([]byte, 1024)

	mockClient.On("DownloadBuffer", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		buf := args.Get(3).([]byte)
		copy(buf, testData)
	}).Return(int64(len(testData)), nil)

	t.Run("successful download", func(t *testing.T) {
		bytesDownloaded, err := client.DownloadBlob(ctx, container, blobPath, buf)
		require.NoError(t, err)
		require.Equal(t, int64(len(testData)), bytesDownloaded)
		require.Equal(t, string(testData), string(buf[:len(testData)]))
	})
}

func TestDownloadBlobStream(t *testing.T) {
	ctx := context.Background()
	container := "testcontainer"
	blobPath := "flow/PT1H.json"
	testData := []byte(`{"records":[{"a":1},{"b":2}]}`)

	t.Run("successful stream download", func(t *testing.T) {
		mockClient := &mockAzureClient{}
		client := &AzureClient{azClient: mockClient, logger: zap.NewNop()}

		resp := azblob.DownloadStreamResponse{}
		resp.Body = io.NopCloser(bytes.NewReader(testData))
		mockClient.On("DownloadStream", mock.Anything, container, blobPath, mock.Anything).Return(resp, nil)

		data, err := client.DownloadBlobStream(ctx, container, blobPath)
		require.NoError(t, err)
		require.Equal(t, testData, data)
	})

	t.Run("download error", func(t *testing.T) {
		mockClient := &mockAzureClient{}
		client := &AzureClient{azClient: mockClient, logger: zap.NewNop()}

		mockClient.On("DownloadStream", mock.Anything, container, blobPath, mock.Anything).
			Return(azblob.DownloadStreamResponse{}, errors.New("network error"))

		data, err := client.DownloadBlobStream(ctx, container, blobPath)
		require.Error(t, err)
		require.Nil(t, data)
		require.Contains(t, err.Error(), "network error")
	})

	t.Run("read error", func(t *testing.T) {
		mockClient := &mockAzureClient{}
		client := &AzureClient{azClient: mockClient, logger: zap.NewNop()}

		resp := azblob.DownloadStreamResponse{}
		resp.Body = io.NopCloser(errReader{})
		mockClient.On("DownloadStream", mock.Anything, container, blobPath, mock.Anything).Return(resp, nil)

		data, err := client.DownloadBlobStream(ctx, container, blobPath)
		require.Error(t, err)
		require.Nil(t, data)
		require.Contains(t, err.Error(), "read blob stream")
	})
}

func TestDownloadBlobRange(t *testing.T) {
	ctx := context.Background()
	container := "testcontainer"
	blobPath := "flow/PT1H.json"
	tail := []byte(`,{"c":3}]}`)

	t.Run("passes offset as HTTPRange and returns bytes", func(t *testing.T) {
		mockClient := &mockAzureClient{}
		client := &AzureClient{azClient: mockClient, logger: zap.NewNop()}

		var gotOffset int64 = -1
		var gotCount int64 = -1
		resp := azblob.DownloadStreamResponse{}
		resp.Body = io.NopCloser(bytes.NewReader(tail))
		cr := "bytes 42-51/2048"
		resp.ContentRange = &cr
		mockClient.On("DownloadStream", mock.Anything, container, blobPath, mock.Anything).
			Run(func(args mock.Arguments) {
				opts := args.Get(3).(*azblob.DownloadStreamOptions)
				gotOffset = opts.Range.Offset
				gotCount = opts.Range.Count
			}).
			Return(resp, nil)

		data, total, err := client.DownloadBlobRange(ctx, container, blobPath, 42, 0)
		require.NoError(t, err)
		require.Equal(t, tail, data)
		require.Equal(t, int64(2048), total, "total is the Content-Range full size, not the returned byte count")
		require.Equal(t, int64(42), gotOffset)
		require.Equal(t, int64(0), gotCount, "count 0 means read from offset to end (CountToEnd)")
	})

	t.Run("reads a leading fingerprint with a bounded count", func(t *testing.T) {
		mockClient := &mockAzureClient{}
		client := &AzureClient{azClient: mockClient, logger: zap.NewNop()}

		var gotOffset, gotCount int64 = -1, -1
		resp := azblob.DownloadStreamResponse{}
		resp.Body = io.NopCloser(bytes.NewReader([]byte("head")))
		mockClient.On("DownloadStream", mock.Anything, container, blobPath, mock.Anything).
			Run(func(args mock.Arguments) {
				opts := args.Get(3).(*azblob.DownloadStreamOptions)
				gotOffset = opts.Range.Offset
				gotCount = opts.Range.Count
			}).
			Return(resp, nil)

		_, _, err := client.DownloadBlobRange(ctx, container, blobPath, 0, 256)
		require.NoError(t, err)
		require.Equal(t, int64(0), gotOffset)
		require.Equal(t, int64(256), gotCount)
	})

	t.Run("download error", func(t *testing.T) {
		mockClient := &mockAzureClient{}
		client := &AzureClient{azClient: mockClient, logger: zap.NewNop()}

		mockClient.On("DownloadStream", mock.Anything, container, blobPath, mock.Anything).
			Return(azblob.DownloadStreamResponse{}, errors.New("range error"))

		data, _, err := client.DownloadBlobRange(ctx, container, blobPath, 10, 0)
		require.Error(t, err)
		require.Nil(t, data)
		require.Contains(t, err.Error(), "range error")
	})
}

func TestBlobTotalSize(t *testing.T) {
	s := func(v string) *string { return &v }
	i := func(v int64) *int64 { return &v }
	cases := []struct {
		name         string
		contentRange *string
		contentLen   *int64
		want         int64
	}{
		{"ranged read parses total after slash", s("bytes 0-511/2048"), i(512), 2048},
		{"clamped short blob", s("bytes 0-99/100"), i(100), 100},
		{"full read has no range, uses content length", nil, i(512), 512},
		{"unparseable total falls back to content length", s("bytes */*"), i(512), 512},
		{"unparseable total and no length is unknown", s("bytes */*"), nil, -1},
		{"no headers is unknown", nil, nil, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, blobTotalSize(c.contentRange, c.contentLen))
		})
	}
}

func TestDeleteBlobSuccess(t *testing.T) {
	// Create a mock Azure client using testify/mock
	mockClient := &mockAzureClient{}
	client := &AzureClient{
		azClient:  mockClient,
		logger:    zap.NewNop(),
		batchSize: 100,
		pageSize:  1000,
	}

	ctx := context.Background()
	container := "testcontainer"
	blobPath := "test/blob.txt"

	mockClient.On("DeleteBlob", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(azblob.DeleteBlobResponse{}, nil)
	err := client.DeleteBlob(ctx, container, blobPath)
	require.NoError(t, err)

}

func TestDeleteBlobFailure(t *testing.T) {
	// Create a mock Azure client using testify/mock
	mockClient := &mockAzureClient{}
	client := &AzureClient{
		azClient:  mockClient,
		logger:    zap.NewNop(),
		batchSize: 100,
		pageSize:  1000,
	}

	ctx := context.Background()
	container := "testcontainer"
	blobPath := "test/blob.txt"

	mockClient.On("DeleteBlob", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(azblob.DeleteBlobResponse{}, errors.New("failed to delete"))
	err := client.DeleteBlob(ctx, container, blobPath)
	require.Error(t, err)
	require.Equal(t, "failed to delete", err.Error())
}

func TestListPrefixes(t *testing.T) {
	t.Run("returns prefixes from hierarchy listing", func(t *testing.T) {
		mockLister := &mockContainerLister{}
		client := &AzureClient{
			containerListerFn: func(_ string) containerLister {
				return mockLister
			},
		}

		prefix1 := "linux/auditd/"
		prefix2 := "linux/logb/"
		prefix3 := "linux/logc/"

		pager := runtime.NewPager(runtime.PagingHandler[container.ListBlobsHierarchyResponse]{
			More: func(_ container.ListBlobsHierarchyResponse) bool { return false },
			Fetcher: func(_ context.Context, _ *container.ListBlobsHierarchyResponse) (container.ListBlobsHierarchyResponse, error) {
				return container.ListBlobsHierarchyResponse{
					ListBlobsHierarchySegmentResponse: container.ListBlobsHierarchySegmentResponse{
						Segment: &container.BlobHierarchyListSegment{
							BlobPrefixes: []*container.BlobPrefix{
								{Name: &prefix1},
								{Name: &prefix2},
								{Name: &prefix3},
							},
						},
					},
				}, nil
			},
		})

		mockLister.On("NewListBlobsHierarchyPager", "/", mock.Anything).Return(pager)

		ctx := context.Background()
		prefixes, err := client.ListPrefixes(ctx, "test-container", "linux/")
		require.NoError(t, err)
		require.Equal(t, []string{"linux/auditd", "linux/logb", "linux/logc"}, prefixes)
	})

	t.Run("returns empty on no prefixes", func(t *testing.T) {
		mockLister := &mockContainerLister{}
		client := &AzureClient{
			containerListerFn: func(_ string) containerLister {
				return mockLister
			},
		}

		pager := runtime.NewPager(runtime.PagingHandler[container.ListBlobsHierarchyResponse]{
			More: func(_ container.ListBlobsHierarchyResponse) bool { return false },
			Fetcher: func(_ context.Context, _ *container.ListBlobsHierarchyResponse) (container.ListBlobsHierarchyResponse, error) {
				return container.ListBlobsHierarchyResponse{
					ListBlobsHierarchySegmentResponse: container.ListBlobsHierarchySegmentResponse{
						Segment: &container.BlobHierarchyListSegment{},
					},
				}, nil
			},
		})

		mockLister.On("NewListBlobsHierarchyPager", "/", mock.Anything).Return(pager)

		ctx := context.Background()
		prefixes, err := client.ListPrefixes(ctx, "test-container", "linux/")
		require.NoError(t, err)
		require.Empty(t, prefixes)
	})

	t.Run("returns error on API failure", func(t *testing.T) {
		mockLister := &mockContainerLister{}
		client := &AzureClient{
			containerListerFn: func(_ string) containerLister {
				return mockLister
			},
		}

		pager := runtime.NewPager(runtime.PagingHandler[container.ListBlobsHierarchyResponse]{
			More: func(_ container.ListBlobsHierarchyResponse) bool { return false },
			Fetcher: func(_ context.Context, _ *container.ListBlobsHierarchyResponse) (container.ListBlobsHierarchyResponse, error) {
				return container.ListBlobsHierarchyResponse{}, errors.New("network error")
			},
		})

		mockLister.On("NewListBlobsHierarchyPager", "/", mock.Anything).Return(pager)

		ctx := context.Background()
		prefixes, err := client.ListPrefixes(ctx, "test-container", "linux/")
		require.Error(t, err)
		require.Nil(t, prefixes)
		require.Contains(t, err.Error(), "network error")
	})
}

func blobItem(name string, size int64, lm time.Time) *container.BlobItem {
	return &container.BlobItem{
		Name: &name,
		Properties: &container.BlobProperties{
			ContentLength: &size,
			LastModified:  &lm,
		},
	}
}

func flatResponse(items []*container.BlobItem) azblob.ListBlobsFlatResponse {
	return azblob.ListBlobsFlatResponse{
		ListBlobsFlatSegmentResponse: azblob.ListBlobsFlatSegmentResponse{
			Segment: &container.BlobFlatListSegment{BlobItems: items},
		},
	}
}

// onePageFlatPager returns a pager that yields items as a single page.
func onePageFlatPager(items []*container.BlobItem) *runtime.Pager[azblob.ListBlobsFlatResponse] {
	fetched := false
	return runtime.NewPager(runtime.PagingHandler[azblob.ListBlobsFlatResponse]{
		More: func(azblob.ListBlobsFlatResponse) bool { return !fetched },
		Fetcher: func(context.Context, *azblob.ListBlobsFlatResponse) (azblob.ListBlobsFlatResponse, error) {
			fetched = true
			return flatResponse(items), nil
		},
	})
}

func TestStreamBlobs(t *testing.T) {
	lm := time.Unix(1000, 0)

	t.Run("streams blobs in batches and closes the done channel", func(t *testing.T) {
		mc := &mockAzureClient{}
		client := &AzureClient{azClient: mc, logger: zap.NewNop(), batchSize: 2, pageSize: 1000}
		mc.On("NewListBlobsFlatPager", "cont", mock.Anything).
			Return(onePageFlatPager([]*container.BlobItem{blobItem("a", 1, lm), blobItem("b", 2, lm), blobItem("c", 3, lm)}))

		blobChan := make(chan []*BlobInfo, 8)
		errChan := make(chan error, 1)
		doneChan := make(chan struct{})
		client.StreamBlobs(context.Background(), "cont", nil, errChan, blobChan, doneChan)

		<-doneChan // closed on clean completion, so every batch is already buffered
		var got []string
		for len(blobChan) > 0 { // drain the buffer without closing from the consumer side
			for _, b := range <-blobChan {
				got = append(got, b.Name)
			}
		}
		require.Empty(t, errChan)
		require.ElementsMatch(t, []string{"a", "b", "c"}, got)
	})

	t.Run("a pager fetch error is sent to errChan", func(t *testing.T) {
		mc := &mockAzureClient{}
		client := &AzureClient{azClient: mc, logger: zap.NewNop(), batchSize: 2, pageSize: 1000}
		pager := runtime.NewPager(runtime.PagingHandler[azblob.ListBlobsFlatResponse]{
			More: func(azblob.ListBlobsFlatResponse) bool { return true },
			Fetcher: func(context.Context, *azblob.ListBlobsFlatResponse) (azblob.ListBlobsFlatResponse, error) {
				return azblob.ListBlobsFlatResponse{}, errors.New("pager boom")
			},
		})
		mc.On("NewListBlobsFlatPager", "cont", mock.Anything).Return(pager)

		errChan := make(chan error, 1)
		client.StreamBlobs(context.Background(), "cont", nil, errChan, make(chan []*BlobInfo, 1), make(chan struct{}))
		require.Len(t, errChan, 1)
		require.ErrorContains(t, <-errChan, "error streaming blobs")
	})

	// returnsAfterCancel asserts StreamBlobs exits promptly once ctx is cancelled.
	returnsAfterCancel := func(t *testing.T, done <-chan struct{}) {
		require.Eventually(t, func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		}, time.Second, time.Millisecond, "StreamBlobs returns rather than blocking on the undrained send")
	}

	t.Run("a context cancelled during the fetch unblocks the pending send", func(t *testing.T) {
		// The consumer never drains blobChan. Without the ctx-aware send, StreamBlobs would
		// block on it forever; the cancellation must let it return instead.
		mc := &mockAzureClient{}
		client := &AzureClient{azClient: mc, logger: zap.NewNop(), batchSize: 1, pageSize: 1000}
		ctx, cancel := context.WithCancel(context.Background())
		pager := runtime.NewPager(runtime.PagingHandler[azblob.ListBlobsFlatResponse]{
			More: func(azblob.ListBlobsFlatResponse) bool { return true },
			Fetcher: func(context.Context, *azblob.ListBlobsFlatResponse) (azblob.ListBlobsFlatResponse, error) {
				cancel() // cancel before the page reaches the send
				return flatResponse([]*container.BlobItem{blobItem("a", 1, lm)}), nil
			},
		})
		mc.On("NewListBlobsFlatPager", "cont", mock.Anything).Return(pager)

		done := make(chan struct{})
		go func() {
			client.StreamBlobs(ctx, "cont", nil, make(chan error, 1), make(chan []*BlobInfo), make(chan struct{}))
			close(done)
		}()
		returnsAfterCancel(t, done)
	})

	t.Run("a cancelled context unblocks a mid-batch send", func(t *testing.T) {
		// batchSize 1 over a two-item page: receive the first batch to prove the loop is past
		// the first send, then cancel so the second (mid-batch) send blocks and its ctx.Done wins.
		mc := &mockAzureClient{}
		client := &AzureClient{azClient: mc, logger: zap.NewNop(), batchSize: 1, pageSize: 1000}
		mc.On("NewListBlobsFlatPager", "cont", mock.Anything).
			Return(onePageFlatPager([]*container.BlobItem{blobItem("a", 1, lm), blobItem("b", 2, lm)}))
		ctx, cancel := context.WithCancel(context.Background())
		blobChan := make(chan []*BlobInfo) // unbuffered, drained once below
		done := make(chan struct{})
		go func() {
			client.StreamBlobs(ctx, "cont", nil, make(chan error, 1), blobChan, make(chan struct{}))
			close(done)
		}()
		<-blobChan // first batch delivered; the next mid-batch send now blocks (we stop reading)
		cancel()
		returnsAfterCancel(t, done)
	})

	t.Run("a cancelled context unblocks the end-of-page send", func(t *testing.T) {
		// batchSize 2 over a two-item page: the mid-batch send delivers [a,b]; receive it, then
		// cancel so the trailing end-of-page send (empty batch) blocks and its ctx.Done wins.
		mc := &mockAzureClient{}
		client := &AzureClient{azClient: mc, logger: zap.NewNop(), batchSize: 2, pageSize: 1000}
		mc.On("NewListBlobsFlatPager", "cont", mock.Anything).
			Return(onePageFlatPager([]*container.BlobItem{blobItem("a", 1, lm), blobItem("b", 2, lm)}))
		ctx, cancel := context.WithCancel(context.Background())
		blobChan := make(chan []*BlobInfo)
		done := make(chan struct{})
		go func() {
			client.StreamBlobs(ctx, "cont", nil, make(chan error, 1), blobChan, make(chan struct{}))
			close(done)
		}()
		<-blobChan // [a,b] delivered; the end-of-page send now blocks
		cancel()
		returnsAfterCancel(t, done)
	})

	t.Run("a cancelled context unblocks the error send", func(t *testing.T) {
		// The pager errors and the consumer never drains errChan; the ctx-aware error send must
		// let StreamBlobs return instead of blocking on the undrained errChan forever.
		mc := &mockAzureClient{}
		client := &AzureClient{azClient: mc, logger: zap.NewNop(), batchSize: 2, pageSize: 1000}
		ctx, cancel := context.WithCancel(context.Background())
		pager := runtime.NewPager(runtime.PagingHandler[azblob.ListBlobsFlatResponse]{
			More: func(azblob.ListBlobsFlatResponse) bool { return true },
			Fetcher: func(context.Context, *azblob.ListBlobsFlatResponse) (azblob.ListBlobsFlatResponse, error) {
				cancel() // cancel before the error reaches the send
				return azblob.ListBlobsFlatResponse{}, errors.New("pager boom")
			},
		})
		mc.On("NewListBlobsFlatPager", "cont", mock.Anything).Return(pager)

		done := make(chan struct{})
		go func() {
			client.StreamBlobs(ctx, "cont", nil, make(chan error), make(chan []*BlobInfo, 1), make(chan struct{}))
			close(done)
		}()
		returnsAfterCancel(t, done)
	})
}

// mockAzureClient is a mock implementation of the Azure blob client
type mockAzureClient struct {
	mock.Mock
}

func (m *mockAzureClient) NewListBlobsFlatPager(containerName string, options *azblob.ListBlobsFlatOptions) *runtime.Pager[azblob.ListBlobsFlatResponse] {
	args := m.Called(containerName, options)
	return args.Get(0).(*runtime.Pager[azblob.ListBlobsFlatResponse])
}

func (m *mockAzureClient) DownloadBuffer(ctx context.Context, containerName string, blobPath string, buffer []byte, options *azblob.DownloadBufferOptions) (int64, error) {
	args := m.Called(ctx, containerName, blobPath, buffer, options)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockAzureClient) DownloadStream(ctx context.Context, containerName string, blobPath string, options *azblob.DownloadStreamOptions) (azblob.DownloadStreamResponse, error) {
	args := m.Called(ctx, containerName, blobPath, options)
	return args.Get(0).(azblob.DownloadStreamResponse), args.Error(1)
}

// errReader is an io.Reader that always fails, used to exercise the read-error path.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func (m *mockAzureClient) DeleteBlob(ctx context.Context, containerName string, blobPath string, options *azblob.DeleteBlobOptions) (azblob.DeleteBlobResponse, error) {
	args := m.Called(ctx, containerName, blobPath, options)
	return args.Get(0).(azblob.DeleteBlobResponse), args.Error(1)
}

// mockContainerLister mocks the containerLister interface
type mockContainerLister struct {
	mock.Mock
}

func (m *mockContainerLister) NewListBlobsHierarchyPager(delimiter string, o *container.ListBlobsHierarchyOptions) *runtime.Pager[container.ListBlobsHierarchyResponse] {
	args := m.Called(delimiter, o)
	return args.Get(0).(*runtime.Pager[container.ListBlobsHierarchyResponse])
}
