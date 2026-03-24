package taxii

// Discovery is the Server Discovery resource (TAXII 2.1 section 4.1.1).
type Discovery struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Contact     string   `json:"contact,omitempty"`
	APIRoots    []string `json:"api_roots,omitempty"`
	Default     string   `json:"default,omitempty"`
}

// APIRootInfo is the API Root Information resource (section 4.2.1).
type APIRootInfo struct {
	Title            string   `json:"title"`
	Description      string   `json:"description,omitempty"`
	Versions         []string `json:"versions"`
	MaxContentLength int64    `json:"max_content_length"`
}

// CollectionInfo is a Collection resource (section 5.2.1).
type CollectionInfo struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Alias       string   `json:"alias,omitempty"`
	CanRead     bool     `json:"can_read"`
	CanWrite    bool     `json:"can_write"`
	MediaTypes  []string `json:"media_types,omitempty"`
}

// CollectionsList is the response from Get Collections (section 5.1); the key is "collections".
type CollectionsList struct {
	Collections []CollectionInfo `json:"collections,omitempty"`
}

// StatusResource is the Status resource (section 4.3.1).
// Successes, Failures, and Pendings are per-spec arrays of StatusDetail (id, version, optional message).
type StatusResource struct {
	ID               string         `json:"id"`
	Status           string         `json:"status"`
	RequestTimestamp string         `json:"request_timestamp,omitempty"`
	TotalCount       int            `json:"total_count"`
	SuccessCount     int            `json:"success_count"`
	FailureCount     int            `json:"failure_count"`
	PendingCount     int            `json:"pending_count"`
	Successes        []StatusDetail `json:"successes,omitempty"`
	Failures         []StatusDetail `json:"failures,omitempty"`
	Pendings         []StatusDetail `json:"pendings,omitempty"`
}

// StatusDetail is a single success/failure/pending entry in a status resource (spec 4.3.1).
type StatusDetail struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Message string `json:"message,omitempty"`
}

// Envelope is the Get Objects response: objects array plus pagination (section 5.4).
type Envelope struct {
	Objects []map[string]interface{} `json:"objects,omitempty"`
	More    bool                     `json:"more"`
	Next    string                   `json:"next,omitempty"`
}

// ManifestResource is a single manifest entry (section 5.3). Spec uses "media_type" (singular).
type ManifestResource struct {
	ID         string   `json:"id"`
	DateAdded  string   `json:"date_added"`
	Version    string   `json:"version"`
	MediaType  string   `json:"media_type,omitempty"`
	MediaTypes []string `json:"media_types,omitempty"` // optional; some servers use array
}

// ManifestList is the Get Object Manifests response (section 5.3).
type ManifestList struct {
	Objects []ManifestResource `json:"objects,omitempty"`
	More    bool               `json:"more"`
	Next    string             `json:"next,omitempty"`
}
