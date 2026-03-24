package taxii

import "time"

// FilterArgs holds query parameters for server endpoints (limit, next, added_after, match[*]).
type FilterArgs struct {
	Limit      int
	Next       string
	AddedAfter string
	Match      map[string]string // match[id], match[type], match[version], match[spec_version]
}

// ManifestResult is the backend result for Get Object Manifests (objects + more + next + headers).
type ManifestResult struct {
	Objects []ManifestResource
	More    bool
	Next    string
	Headers map[string]string // X-TAXII-Date-Added-First, X-TAXII-Date-Added-Last
}

// ObjectsResult is the backend result for Get Objects / Get Object (objects + more + next + headers).
type ObjectsResult struct {
	Objects []map[string]interface{}
	More    bool
	Next    string
	Headers map[string]string
}

// VersionsResult is the backend result for Get Object Versions (versions + more + next).
type VersionsResult struct {
	Versions []string
	More     bool
	Next     string
	Headers  map[string]string
}

// todo: this is bad go. Way too many modules on this interface.
// Backend persists TAXII data. Implementations may use memory, a database, etc.
type Backend interface {
	ServerDiscovery() (*Discovery, error)
	GetAPIRootInformation(apiRoot string) (*APIRootInfo, error)
	GetCollections(apiRoot string) (*CollectionsList, error)
	GetCollection(apiRoot, collectionID string) (*CollectionInfo, error)
	GetObjectManifest(apiRoot, collectionID string, filter FilterArgs, limit int) (*ManifestResult, error)
	GetObjects(apiRoot, collectionID string, filter FilterArgs, limit int) (*ObjectsResult, error)
	AddObjects(apiRoot, collectionID string, envelope map[string]interface{}, requestTime time.Time) (*StatusResource, error)
	GetObject(apiRoot, collectionID, objectID string, filter FilterArgs, limit int) (*ObjectsResult, error)
	DeleteObject(apiRoot, collectionID, objectID string, filter FilterArgs) error
	GetObjectVersions(apiRoot, collectionID, objectID string, filter FilterArgs, limit int) (*VersionsResult, error)
	GetStatus(apiRoot, statusID string) (*StatusResource, error)
}
