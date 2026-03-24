package taxii

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	exchanges "github.com/observiq/bindplane-otel-collector/exchange"
)

// Client performs TAXII 2.1 requests. Use NewClient to construct.
type Client struct {
	DiscoveryURL string
	HTTPClient   *http.Client
	Creds        exchanges.Credentials
}

// NewClient returns a TAXII client. discoveryURL should be the server discovery endpoint (e.g. https://example.com/taxii2/).
// creds may be nil for no auth; BasicAuth is supported for HTTP Basic.
func NewClient(discoveryURL string, creds exchanges.Credentials) *Client {
	u := discoveryURL
	if u != "" && !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return &Client{
		DiscoveryURL: u,
		HTTPClient:   http.DefaultClient,
		Creds:        creds,
	}
}

// SetHTTPClient sets the HTTP client (e.g. for TLS or timeouts). If nil, http.DefaultClient is used.
func (c *Client) SetHTTPClient(h *http.Client) {
	if h != nil {
		c.HTTPClient = h
	}
}

// Server holds discovery data and API root URLs. Use Client.GetServer to obtain.
type Server struct {
	client *Client
	Discovery
	apiRoots []*ApiRoot
	default_ *ApiRoot
}

// GetServer fetches the Server Discovery resource and returns a Server.
func (c *Client) GetServer() (*Server, error) {
	body, err := c.do(c.DiscoveryURL, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	var d Discovery
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("taxii: decode discovery: %w", err)
	}
	s := &Server{client: c, Discovery: d}
	for _, rootPath := range d.APIRoots {
		rootURL := c.DiscoveryURL
		if base, err := url.Parse(rootURL); err == nil {
			if ref, err := url.Parse(rootPath); err == nil {
				rootURL = base.ResolveReference(ref).String()
			} else {
				rootURL = strings.TrimSuffix(rootURL, "/") + "/" + strings.TrimPrefix(rootPath, "/")
			}
		}
		if !strings.HasSuffix(rootURL, "/") {
			rootURL += "/"
		}
		ar := &ApiRoot{client: c, url: rootURL}
		s.apiRoots = append(s.apiRoots, ar)
		if d.Default != "" && rootPath == d.Default {
			s.default_ = ar
		}
	}
	if s.default_ == nil && len(s.apiRoots) > 0 {
		s.default_ = s.apiRoots[0]
	}
	return s, nil
}

// APIRoots returns the list of API roots for this server.
func (s *Server) APIRoots() []*ApiRoot { return s.apiRoots }

// Default returns the default API root, or the first if none is marked default.
func (s *Server) Default() *ApiRoot { return s.default_ }

// ApiRoot is an API root endpoint. Call Refresh to load info and Collections to list collections.
type ApiRoot struct {
	client *Client
	url    string
	info   *APIRootInfo
	cols   []*Collection
}

// URL returns the API root URL.
func (a *ApiRoot) URL() string { return a.url }

// Refresh fetches API root information and collections list.
func (a *ApiRoot) Refresh() error {
	body, err := a.client.do(a.url, http.MethodGet, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, &a.info); err != nil {
		return fmt.Errorf("taxii: decode api root: %w", err)
	}
	body, err = a.client.do(a.url+"collections/", http.MethodGet, nil)
	if err != nil {
		return err
	}
	var list CollectionsList
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("taxii: decode collections: %w", err)
	}
	a.cols = nil
	for _, ci := range list.Collections {
		a.cols = append(a.cols, &Collection{client: a.client, url: a.url + "collections/" + ci.ID + "/", info: &ci})
	}
	return nil
}

// Info returns API root information. It calls Refresh if not yet loaded.
func (a *ApiRoot) Info() (*APIRootInfo, error) {
	if a.info == nil {
		if err := a.Refresh(); err != nil {
			return nil, err
		}
	}
	return a.info, nil
}

// Collections returns the list of collections. It calls Refresh if not yet loaded.
func (a *ApiRoot) Collections() ([]*Collection, error) {
	if a.cols == nil {
		if err := a.Refresh(); err != nil {
			return nil, err
		}
	}
	return a.cols, nil
}

// GetStatus fetches a status resource by ID.
func (a *ApiRoot) GetStatus(statusID string) (*StatusResource, error) {
	body, err := a.client.do(a.url+"status/"+statusID+"/", http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	var s StatusResource
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("taxii: decode status: %w", err)
	}
	return &s, nil
}

// Collection represents a TAXII collection. Use GetObjects, GetManifest, AddObjects, etc.
type Collection struct {
	client *Client
	url    string
	info   *CollectionInfo
}

// URL returns the collection URL.
func (col *Collection) URL() string { return col.url }

// Info returns collection metadata (id, title, can_read, can_write, etc.).
func (col *Collection) Info() *CollectionInfo { return col.info }

// GetObjects returns a page of objects. Use Filter.Limit and Filter.Next for pagination.
func (col *Collection) GetObjects(f Filter) (*Envelope, error) {
	if col.info != nil && !col.info.CanRead {
		return nil, fmt.Errorf("taxii: collection %s does not allow reading", col.info.ID)
	}
	u := col.url + "objects/"
	if q := f.Query(); len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, err := col.client.do(u, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("taxii: decode objects: %w", err)
	}
	return &env, nil
}

// GetManifest returns a page of manifest entries.
func (col *Collection) GetManifest(f Filter) (*ManifestList, error) {
	if col.info != nil && !col.info.CanRead {
		return nil, fmt.Errorf("taxii: collection %s does not allow reading", col.info.ID)
	}
	u := col.url + "manifest/"
	if q := f.Query(); len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, err := col.client.do(u, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	var list ManifestList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("taxii: decode manifest: %w", err)
	}
	return &list, nil
}

// AddObjects posts a TAXII envelope (JSON object with "objects" array) and returns the status resource.
func (col *Collection) AddObjects(envelope interface{}) (*StatusResource, error) {
	if col.info != nil && !col.info.CanWrite {
		return nil, fmt.Errorf("taxii: collection %s does not allow writing", col.info.ID)
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	respBody, err := col.client.do(col.url+"objects/", http.MethodPost, body)
	if err != nil {
		return nil, err
	}
	var s StatusResource
	if err := json.Unmarshal(respBody, &s); err != nil {
		return nil, fmt.Errorf("taxii: decode add-objects status: %w", err)
	}
	return &s, nil
}

// GetObject fetches a single object by ID.
func (col *Collection) GetObject(objectID string, f Filter) (*Envelope, error) {
	if col.info != nil && !col.info.CanRead {
		return nil, fmt.Errorf("taxii: collection %s does not allow reading", col.info.ID)
	}
	u := col.url + "objects/" + objectID + "/"
	if q := f.Query(); len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, err := col.client.do(u, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("taxii: decode object: %w", err)
	}
	return &env, nil
}

// DeleteObject deletes an object by ID (TAXII 2.1).
func (col *Collection) DeleteObject(objectID string, f Filter) error {
	if col.info != nil && !col.info.CanWrite {
		return fmt.Errorf("taxii: collection %s does not allow writing", col.info.ID)
	}
	u := col.url + "objects/" + objectID + "/"
	if q := f.Query(); len(q) > 0 {
		u += "?" + q.Encode()
	}
	_, err := col.client.do(u, http.MethodDelete, nil)
	return err
}

// GetObjectVersions returns version history for an object (TAXII 2.1).
func (col *Collection) GetObjectVersions(objectID string, f Filter) (*Envelope, error) {
	if col.info != nil && !col.info.CanRead {
		return nil, fmt.Errorf("taxii: collection %s does not allow reading", col.info.ID)
	}
	u := col.url + "objects/" + objectID + "/versions/"
	if q := f.Query(); len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, err := col.client.do(u, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("taxii: decode object versions: %w", err)
	}
	return &env, nil
}

// do performs an HTTP request with TAXII 2.1 Accept and optional Content-Type, and optional Basic auth.
func (c *Client) do(rawURL, method string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", MediaTypeTAXII21)
	}
	req.Header.Set("Accept", MediaTypeTAXII21)
	if c.Creds != nil {
		switch basic := c.Creds.(type) {
		case *exchanges.BasicAuth:
			req.SetBasicAuth(basic.Username, basic.Password)
		case exchanges.BasicAuth:
			req.SetBasicAuth(basic.Username, basic.Password)
		}
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: respBody}
	}
	return respBody, nil
}

// HTTPError is returned when the server responds with a non-2xx status.
type HTTPError struct {
	StatusCode int
	Body       []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("taxii: HTTP %d: %s", e.StatusCode, bytes.TrimSpace(e.Body))
}
