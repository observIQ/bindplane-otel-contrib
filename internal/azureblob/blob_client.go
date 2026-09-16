// Copyright observIQ, Inc.
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

// Package azureblob contains client interfaces and implementations for accessing Blob storage
package azureblob //import "github.com/observiq/bindplane-otel-contrib/internal/azureblob"

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"go.uber.org/zap"
)

// IsRangeNotSatisfiable reports whether err is an Azure "invalid range" response,
// which a range read returns when its start offset is at or past the blob's current
// length. Callers tailing an append-growable blob use this to treat "offset already
// at end" as "no new bytes" rather than a failure.
func IsRangeNotSatisfiable(err error) bool {
	return bloberror.HasCode(err, bloberror.InvalidRange)
}

// IsBlobNotFound reports whether err is an Azure "blob not found" response: BlobNotFound, or the
// ResourceNotFound some endpoints (e.g. HNS/dfs) return for a missing blob. Callers treat a
// vanished blob as "nothing to do" rather than retrying.
func IsBlobNotFound(err error) bool {
	return bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ResourceNotFound)
}

// IsPermanentError reports whether err is an Azure failure that will not clear on retry: an
// authentication, authorization, or account-configuration problem (e.g. a 403 after an ACL
// change). Callers stop retrying such an operation, while treating throttling, network, and
// unknown errors as transient so a temporary outage does not quarantine a still-readable blob.
func IsPermanentError(err error) bool {
	return bloberror.HasCode(err,
		bloberror.AuthenticationFailed,
		bloberror.AuthorizationFailure,
		bloberror.AuthorizationPermissionMismatch,
		bloberror.AuthorizationProtocolMismatch,
		bloberror.AuthorizationResourceTypeMismatch,
		bloberror.AuthorizationServiceMismatch,
		bloberror.AuthorizationSourceIPMismatch,
		bloberror.InsufficientAccountPermissions,
		bloberror.InvalidAuthenticationInfo,
		bloberror.AccountIsDisabled,
		// Missing/misnamed container: a per-blob read can't create it, so it can never succeed
		// (near-unreachable per-blob since listing fails first, but classified here so it
		// quarantines rather than retrying every poll if it ever does surface). A missing blob
		// (ResourceNotFound) is handled as not-found, not permanent, so it ages out silently.
		bloberror.ContainerNotFound,
		bloberror.InvalidResourceName,
	)
}

// BlobInfo contains the necessary info to process a blob
type BlobInfo struct {
	Name         string
	Size         int64
	LastModified time.Time
}

// BlobClient provides a client for Blob operations
//
//go:generate mockery --name BlobClient --inpackage --with-expecter --filename mock_blob_client.go --structname MockBlobClient
type BlobClient interface {
	// DownloadBlob downloads the contents of the blob into the supplied buffer.
	// It will return the count of bytes used in the buffer.
	DownloadBlob(ctx context.Context, container, blobPath string, buf []byte) (int64, error)

	// DownloadBlobStream downloads the full current contents of the blob and
	// returns them. Unlike DownloadBlob it does not require a pre-sized buffer,
	// so it is safe for blobs whose listed size is stale (e.g. Azure flow-log
	// blobs that are grown in place all hour via PutBlock).
	DownloadBlobStream(ctx context.Context, container, blobPath string) ([]byte, error)

	// DownloadBlobRange downloads count bytes of the blob starting at the given
	// byte offset and returns them. A count of 0 reads from the offset to the
	// blob's current end. It is used for incremental reads of append-growable
	// blobs (offset>0, count 0) and for reading a leading fingerprint (offset 0,
	// count N). total is the blob's full current size (-1 if unknown), which the
	// identity read uses to spot a replacement shorter than the stored offset.
	DownloadBlobRange(ctx context.Context, container, blobPath string, offset, count int64) (data []byte, total int64, err error)

	// DeleteBlob deletes the blob in the specified container
	DeleteBlob(ctx context.Context, container, blobPath string) error

	// StreamBlobs will stream BlobInfo to the blobChan and errors to the errChan, generally if an errChan gets an item
	// then the stream should be stopped
	StreamBlobs(ctx context.Context, container string, prefix *string, errChan chan error, blobChan chan []*BlobInfo, doneChan chan struct{})

	// ListPrefixes lists virtual directory prefixes under the given prefix in a container.
	// It uses Azure's hierarchy listing API with "/" as delimiter to discover immediate subdirectories.
	ListPrefixes(ctx context.Context, containerName string, prefix string) ([]string, error)
}

type blobClient interface {
	NewListBlobsFlatPager(containerName string, options *azblob.ListBlobsFlatOptions) *runtime.Pager[azblob.ListBlobsFlatResponse]
	DownloadBuffer(ctx context.Context, containerName string, blobPath string, buffer []byte, options *azblob.DownloadBufferOptions) (int64, error)
	DownloadStream(ctx context.Context, containerName string, blobPath string, options *azblob.DownloadStreamOptions) (azblob.DownloadStreamResponse, error)
	DeleteBlob(ctx context.Context, containerName string, blobPath string, options *azblob.DeleteBlobOptions) (azblob.DeleteBlobResponse, error)
}

var _ blobClient = &azblob.Client{}

// containerLister abstracts the hierarchy listing capability of a container client for testability.
type containerLister interface {
	NewListBlobsHierarchyPager(delimiter string, o *container.ListBlobsHierarchyOptions) *runtime.Pager[container.ListBlobsHierarchyResponse]
}

// containerListerFactory creates a containerLister for the given container name.
type containerListerFactory func(containerName string) containerLister

// AzureClient is an implementation of the BlobClient for Azure
type AzureClient struct {
	azClient          blobClient
	containerListerFn containerListerFactory
	logger            *zap.Logger
	batchSize         int
	pageSize          int32
}

// NewAzureBlobClient creates a new azureBlobClient with the given connection string
func NewAzureBlobClient(connectionString string, batchSize, pageSize int, logger *zap.Logger) (BlobClient, error) {
	azClient, err := azblob.NewClientFromConnectionString(connectionString, nil)
	if err != nil {
		return nil, err
	}
	return &AzureClient{
		azClient:  azClient,
		logger:    logger,
		batchSize: batchSize,
		pageSize:  int32(pageSize),
		containerListerFn: func(containerName string) containerLister {
			return azClient.ServiceClient().NewContainerClient(containerName)
		},
	}, nil
}

// StreamBlobs will stream blobs to the blobChan and errors to the errChan, generally if an errChan gets an item
// then the stream should be stopped
func (a *AzureClient) StreamBlobs(ctx context.Context, container string, prefix *string, errChan chan error, blobChan chan []*BlobInfo, doneChan chan struct{}) {
	var marker *string

	pager := a.azClient.NewListBlobsFlatPager(container, &azblob.ListBlobsFlatOptions{
		Marker:     marker,
		Prefix:     prefix,
		MaxResults: &a.pageSize,
	})

	pageNumber := 0
	totalStreamed := 0
	for pager.More() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		resp, err := pager.NextPage(ctx)
		if err != nil {
			// Select on ctx.Done so a cancelled poll (whose consumer has stopped draining)
			// doesn't block this send forever.
			select {
			case errChan <- fmt.Errorf("error streaming blobs: %w", err):
			case <-ctx.Done():
			}
			return
		}

		pageNumber++
		blobsInPage := len(resp.Segment.BlobItems)
		totalStreamed += blobsInPage
		a.logger.Debug("Azure API page received",
			zap.Int("page_number", pageNumber),
			zap.Int("blobs_in_page", blobsInPage),
			zap.Int("total_streamed", totalStreamed))

		batch := []*BlobInfo{}
		for _, blob := range resp.Segment.BlobItems {
			if blob.Deleted != nil && *blob.Deleted {
				continue
			}
			if blob.Name == nil || blob.Properties == nil || blob.Properties.ContentLength == nil {
				continue
			}

			var lastModified time.Time
			if blob.Properties.LastModified != nil {
				lastModified = *blob.Properties.LastModified
			}

			info := &BlobInfo{
				Name:         *blob.Name,
				Size:         *blob.Properties.ContentLength,
				LastModified: lastModified,
			}
			batch = append(batch, info)
			if len(batch) == int(a.batchSize) {
				select {
				case blobChan <- batch:
				case <-ctx.Done():
					return
				}
				batch = []*BlobInfo{}
			}
		}

		select {
		case blobChan <- batch:
		case <-ctx.Done():
			return
		}
	}

	close(doneChan)
}

// DownloadBlob downloads the contents of the blob into the supplied buffer.
// It will return the count of bytes used in the buffer.
func (a *AzureClient) DownloadBlob(ctx context.Context, container, blobPath string, buf []byte) (int64, error) {
	bytesDownloaded, err := a.azClient.DownloadBuffer(ctx, container, blobPath, buf, nil)
	if err != nil {
		return 0, fmt.Errorf("download: %w", err)
	}

	return bytesDownloaded, nil
}

// DownloadBlobStream reads the whole blob via DownloadBlobRange(offset 0, count 0). See the
// interface method for why the stale-listed-size case needs it.
func (a *AzureClient) DownloadBlobStream(ctx context.Context, container, blobPath string) ([]byte, error) {
	data, _, err := a.DownloadBlobRange(ctx, container, blobPath, 0, 0)
	return data, err
}

// DownloadBlobRange reads count bytes from offset (count 0 reads to the blob's end). See the
// interface method for the delta and fingerprint use cases and what total reports.
func (a *AzureClient) DownloadBlobRange(ctx context.Context, container, blobPath string, offset, count int64) (data []byte, total int64, err error) {
	opts := &azblob.DownloadStreamOptions{
		Range: blob.HTTPRange{Offset: offset, Count: count},
	}
	resp, err := a.azClient.DownloadStream(ctx, container, blobPath, opts)
	if err != nil {
		return nil, 0, fmt.Errorf("download stream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read blob stream: %w", err)
	}
	return data, blobTotalSize(resp.ContentRange, resp.ContentLength), nil
}

// blobTotalSize returns the blob's full size (not the returned byte count): a ranged read's
// "Content-Range: bytes a-b/total", else a full read's Content-Length, else -1 for unknown.
func blobTotalSize(contentRange *string, contentLength *int64) int64 {
	if contentRange != nil {
		if slash := strings.LastIndex(*contentRange, "/"); slash >= 0 {
			if total, err := strconv.ParseInt(strings.TrimSpace((*contentRange)[slash+1:]), 10, 64); err == nil {
				return total
			}
		}
	}
	if contentLength != nil {
		return *contentLength
	}
	return -1
}

// DeleteBlob deletes the blob in the specified container
func (a *AzureClient) DeleteBlob(ctx context.Context, container, blobPath string) error {
	_, err := a.azClient.DeleteBlob(ctx, container, blobPath, nil)
	return err
}

// ListPrefixes lists virtual directory prefixes under the given prefix in a container.
func (a *AzureClient) ListPrefixes(ctx context.Context, containerName string, prefix string) ([]string, error) {
	lister := a.containerListerFn(containerName)

	pager := lister.NewListBlobsHierarchyPager("/", &container.ListBlobsHierarchyOptions{
		Prefix: &prefix,
	})

	var prefixes []string
	for pager.More() {
		resp, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list prefixes: %w", err)
		}

		if resp.Segment != nil {
			for _, bp := range resp.Segment.BlobPrefixes {
				if bp.Name != nil {
					prefixes = append(prefixes, strings.TrimSuffix(*bp.Name, "/"))
				}
			}
		}
	}

	return prefixes, nil
}
