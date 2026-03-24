package taxii

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryBackend is an in-memory Backend. Optional initial state can be loaded from JSON.
type MemoryBackend struct {
	mu          sync.RWMutex
	discovery   *Discovery
	roots       map[string]*apiRootData
	nextCursors map[string]*cursorEntry
}

type apiRootData struct {
	Info        APIRootInfo
	Collections []*collectionData
	Statuses    []*StatusResource
}

type collectionData struct {
	Info     CollectionInfo
	Objects  []map[string]interface{}
	Manifest []ManifestResource
}

type cursorEntry struct {
	Filter   FilterArgs
	Created  time.Time
	Objects  []map[string]interface{}
	Manifest []ManifestResource
	Versions []string
}

// NewMemoryBackend returns an empty in-memory backend.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		roots:       make(map[string]*apiRootData),
		nextCursors: make(map[string]*cursorEntry),
	}
}

// NewMemoryBackendFromJSON loads initial state from a medallion-style JSON (discovery under "/discovery", api roots by name with information, collections, status, objects, manifest).
func NewMemoryBackendFromJSON(data []byte) (*MemoryBackend, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	b := NewMemoryBackend()
	if d, ok := raw["/discovery"].(map[string]interface{}); ok {
		b.discovery = &Discovery{}
		if v, _ := d["title"].(string); v != "" {
			b.discovery.Title = v
		}
		if v, _ := d["description"].(string); v != "" {
			b.discovery.Description = v
		}
		if v, _ := d["contact"].(string); v != "" {
			b.discovery.Contact = v
		}
		if v, _ := d["default"].(string); v != "" {
			b.discovery.Default = v
		}
		if arr, ok := d["api_roots"].([]interface{}); ok {
			for _, x := range arr {
				if s, _ := x.(string); s != "" {
					b.discovery.APIRoots = append(b.discovery.APIRoots, s)
				}
			}
		}
	}
	for key, val := range raw {
		if key == "/discovery" {
			continue
		}
		rootMap, ok := val.(map[string]interface{})
		if !ok {
			continue
		}
		root := &apiRootData{Statuses: []*StatusResource{}}
		if info, ok := rootMap["information"].(map[string]interface{}); ok {
			root.Info.Title, _ = info["title"].(string)
			root.Info.Description, _ = info["description"].(string)
			root.Info.MaxContentLength = 9765625
			if m, _ := info["max_content_length"].(float64); m > 0 {
				root.Info.MaxContentLength = int64(m)
			}
			root.Info.Versions = []string{MediaTypeTAXII21}
			if arr, ok := info["versions"].([]interface{}); ok && len(arr) > 0 {
				root.Info.Versions = nil
				for _, x := range arr {
					if s, _ := x.(string); s != "" {
						root.Info.Versions = append(root.Info.Versions, s)
					}
				}
			}
		}
		if arr, ok := rootMap["collections"].([]interface{}); ok {
			for _, c := range arr {
				cm, _ := c.(map[string]interface{})
				if cm == nil {
					continue
				}
				col := &collectionData{}
				col.Info.ID, _ = cm["id"].(string)
				col.Info.Title, _ = cm["title"].(string)
				col.Info.Description, _ = cm["description"].(string)
				col.Info.CanRead, _ = cm["can_read"].(bool)
				col.Info.CanWrite, _ = cm["can_write"].(bool)
				if mt, ok := cm["media_types"].([]interface{}); ok {
					for _, x := range mt {
						if s, _ := x.(string); s != "" {
							col.Info.MediaTypes = append(col.Info.MediaTypes, s)
						}
					}
				}
				if objs, ok := cm["objects"].([]interface{}); ok {
					for _, o := range objs {
						if m, _ := o.(map[string]interface{}); m != nil {
							col.Objects = append(col.Objects, m)
						}
					}
				}
				if man, ok := cm["manifest"].([]interface{}); ok {
					for _, m := range man {
						mm, _ := m.(map[string]interface{})
						if mm == nil {
							continue
						}
						col.Manifest = append(col.Manifest, ManifestResource{
							ID:        getStr(mm, "id"),
							DateAdded: getStr(mm, "date_added"),
							Version:   getStr(mm, "version"),
							MediaType: getStr(mm, "media_type"),
						})
					}
				}
				root.Collections = append(root.Collections, col)
			}
		}
		b.roots[key] = root
	}
	return b, nil
}

func getStr(m map[string]interface{}, k string) string {
	if v, _ := m[k].(string); v != "" {
		return v
	}
	return ""
}

func (b *MemoryBackend) ServerDiscovery() (*Discovery, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.discovery == nil {
		return nil, fmt.Errorf("taxii: discovery not configured")
	}
	d := *b.discovery
	return &d, nil
}

func (b *MemoryBackend) GetAPIRootInformation(apiRoot string) (*APIRootInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	root, ok := b.roots[apiRoot]
	if !ok || (len(root.Collections) == 0 && root.Info.Title == "") {
		return nil, nil
	}
	info := root.Info
	return &info, nil
}

func (b *MemoryBackend) GetCollections(apiRoot string) (*CollectionsList, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	root, ok := b.roots[apiRoot]
	if !ok {
		return nil, nil
	}
	list := &CollectionsList{}
	for _, c := range root.Collections {
		list.Collections = append(list.Collections, c.Info)
	}
	sort.Slice(list.Collections, func(i, j int) bool { return list.Collections[i].ID < list.Collections[j].ID })
	return list, nil
}

func (b *MemoryBackend) GetCollection(apiRoot, collectionID string) (*CollectionInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	col := b.getCollectionLocked(apiRoot, collectionID)
	if col == nil {
		return nil, nil
	}
	info := col.Info
	return &info, nil
}

func (b *MemoryBackend) getCollectionLocked(apiRoot, collectionID string) *collectionData {
	root, ok := b.roots[apiRoot]
	if !ok {
		return nil
	}
	for _, c := range root.Collections {
		if c.Info.ID == collectionID {
			return c
		}
	}
	return nil
}

func objectVersion(obj map[string]interface{}, requestTime time.Time) string {
	if v, _ := obj["modified"].(string); v != "" {
		return v
	}
	if v, _ := obj["created"].(string); v != "" {
		return v
	}
	return requestTime.UTC().Format("2006-01-02T15:04:05.000000Z")
}

func specVersion(obj map[string]interface{}) string {
	if v, _ := obj["spec_version"].(string); v != "" {
		return v
	}
	if _, hasModified := obj["modified"]; hasModified {
		return "2.0"
	}
	if _, hasCreated := obj["created"]; hasCreated {
		return "2.0"
	}
	return "2.1"
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (b *MemoryBackend) GetObjectManifest(apiRoot, collectionID string, filter FilterArgs, limit int) (*ManifestResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	col := b.getCollectionLocked(apiRoot, collectionID)
	if col == nil {
		return nil, nil
	}
	if filter.Next != "" {
		cur, ok := b.nextCursors[filter.Next]
		if !ok {
			return nil, fmt.Errorf("taxii: invalid next token")
		}
		return b.applyManifestCursor(cur, limit)
	}
	manifest := make([]ManifestResource, len(col.Manifest))
	copy(manifest, col.Manifest)
	sort.Slice(manifest, func(i, j int) bool { return manifest[i].DateAdded < manifest[j].DateAdded })
	if filter.AddedAfter != "" {
		var filtered []ManifestResource
		for _, m := range manifest {
			if m.DateAdded > filter.AddedAfter {
				filtered = append(filtered, m)
			}
		}
		manifest = filtered
	}
	if ids := filter.Match["id"]; ids != "" {
		allowed := make(map[string]bool)
		for _, id := range splitComma(ids) {
			allowed[id] = true
		}
		var filtered []ManifestResource
		for _, m := range manifest {
			if allowed[m.ID] {
				filtered = append(filtered, m)
			}
		}
		manifest = filtered
	}
	res := &ManifestResult{Headers: make(map[string]string)}
	if limit <= 0 {
		limit = 100
	}
	if len(manifest) > limit {
		nextID := uuid.New().String()
		b.nextCursors[nextID] = &cursorEntry{Filter: filter, Created: time.Now(), Manifest: manifest[limit:]}
		res.More = true
		res.Next = nextID
		manifest = manifest[:limit]
	}
	if len(manifest) > 0 {
		res.Objects = manifest
		res.Headers["X-TAXII-Date-Added-First"] = manifest[0].DateAdded
		res.Headers["X-TAXII-Date-Added-Last"] = manifest[len(manifest)-1].DateAdded
	}
	return res, nil
}

func (b *MemoryBackend) applyManifestCursor(cur *cursorEntry, limit int) (*ManifestResult, error) {
	manifest := cur.Manifest
	if limit <= 0 {
		limit = 100
	}
	res := &ManifestResult{Headers: make(map[string]string)}
	if len(manifest) > limit {
		nextID := uuid.New().String()
		b.nextCursors[nextID] = &cursorEntry{Filter: cur.Filter, Created: time.Now(), Manifest: manifest[limit:]}
		res.More = true
		res.Next = nextID
		manifest = manifest[:limit]
	}
	res.Objects = manifest
	if len(manifest) > 0 {
		res.Headers["X-TAXII-Date-Added-First"] = manifest[0].DateAdded
		res.Headers["X-TAXII-Date-Added-Last"] = manifest[len(manifest)-1].DateAdded
	}
	delete(b.nextCursors, cur.Filter.Next)
	return res, nil
}

func (b *MemoryBackend) GetObjects(apiRoot, collectionID string, filter FilterArgs, limit int) (*ObjectsResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	col := b.getCollectionLocked(apiRoot, collectionID)
	if col == nil {
		return nil, nil
	}
	if filter.Next != "" {
		cur, ok := b.nextCursors[filter.Next]
		if !ok {
			return nil, fmt.Errorf("taxii: invalid next token")
		}
		return b.applyObjectsCursor(cur, limit)
	}
	objects := make([]map[string]interface{}, 0, len(col.Objects))
	for _, o := range col.Objects {
		obj := make(map[string]interface{})
		for k, v := range o {
			if k == "_date_added" {
				continue
			}
			obj[k] = v
		}
		objects = append(objects, obj)
	}
	manifestByID := make(map[string]string)
	for _, m := range col.Manifest {
		key := m.ID + "\t" + m.Version
		manifestByID[key] = m.DateAdded
	}
	sort.Slice(objects, func(i, j int) bool {
		vi := objectVersion(objects[i], time.Time{})
		vj := objectVersion(objects[j], time.Time{})
		idi, _ := objects[i]["id"].(string)
		idj, _ := objects[j]["id"].(string)
		di := manifestByID[idi+"\t"+vi]
		dj := manifestByID[idj+"\t"+vj]
		return di < dj
	})
	if filter.AddedAfter != "" {
		var filtered []map[string]interface{}
		for _, o := range objects {
			ver := objectVersion(o, time.Time{})
			id, _ := o["id"].(string)
			for _, m := range col.Manifest {
				if m.ID == id && m.Version == ver && m.DateAdded > filter.AddedAfter {
					filtered = append(filtered, o)
					break
				}
			}
		}
		objects = filtered
	}
	if filter.Match["id"] != "" {
		allowed := make(map[string]bool)
		for _, id := range splitComma(filter.Match["id"]) {
			allowed[id] = true
		}
		var filtered []map[string]interface{}
		for _, o := range objects {
			if id, _ := o["id"].(string); allowed[id] {
				filtered = append(filtered, o)
			}
		}
		objects = filtered
	}
	res := &ObjectsResult{Headers: make(map[string]string)}
	if limit <= 0 {
		limit = 100
	}
	if len(objects) > limit {
		nextID := uuid.New().String()
		b.nextCursors[nextID] = &cursorEntry{Filter: filter, Created: time.Now(), Objects: objects[limit:]}
		res.More = true
		res.Next = nextID
		objects = objects[:limit]
	}
	res.Objects = objects
	if len(objects) > 0 {
		idVer := make(map[string]string)
		for _, m := range col.Manifest {
			idVer[m.ID+"\t"+m.Version] = m.DateAdded
		}
		var first, last string
		for _, o := range objects {
			id, _ := o["id"].(string)
			ver := objectVersion(o, time.Time{})
			d := idVer[id+"\t"+ver]
			if first == "" {
				first = d
			}
			last = d
		}
		if first != "" {
			res.Headers["X-TAXII-Date-Added-First"] = first
			res.Headers["X-TAXII-Date-Added-Last"] = last
		}
	}
	return res, nil
}

func (b *MemoryBackend) applyObjectsCursor(cur *cursorEntry, limit int) (*ObjectsResult, error) {
	objects := cur.Objects
	if limit <= 0 {
		limit = 100
	}
	res := &ObjectsResult{Headers: make(map[string]string)}
	if len(objects) > limit {
		nextID := uuid.New().String()
		b.nextCursors[nextID] = &cursorEntry{Filter: cur.Filter, Created: time.Now(), Objects: objects[limit:]}
		res.More = true
		res.Next = nextID
		objects = objects[:limit]
	}
	res.Objects = objects
	delete(b.nextCursors, cur.Filter.Next)
	return res, nil
}

func (b *MemoryBackend) AddObjects(apiRoot, collectionID string, envelope map[string]interface{}, requestTime time.Time) (*StatusResource, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	col := b.getCollectionLocked(apiRoot, collectionID)
	if col == nil {
		return nil, nil
	}
	objs, _ := envelope["objects"].([]interface{})
	if objs == nil {
		return nil, fmt.Errorf("taxii: envelope has no objects array")
	}
	requestTimeStr := requestTime.UTC().Format("2006-01-02T15:04:05.000000Z")
	var successes []StatusDetail
	var failures []StatusDetail
	for _, o := range objs {
		obj, ok := o.(map[string]interface{})
		if !ok {
			failures = append(failures, StatusDetail{Message: "invalid object"})
			continue
		}
		id, _ := obj["id"].(string)
		version := objectVersion(obj, requestTime)
		message := ""
		dup := false
		for _, existing := range col.Objects {
			eid, _ := existing["id"].(string)
			if eid != id {
				continue
			}
			ev := objectVersion(existing, time.Time{})
			if ev == version {
				dup = true
				message = "Object already added"
				break
			}
		}
		if dup {
			successes = append(successes, StatusDetail{ID: id, Version: version, Message: message})
			continue
		}
		col.Objects = append(col.Objects, obj)
		specVer := specVersion(obj)
		mediaType := "application/stix+json;version=" + specVer
		col.Manifest = append(col.Manifest, ManifestResource{
			ID:        id,
			DateAdded: requestTimeStr,
			Version:   version,
			MediaType: mediaType,
		})
		added := false
		for _, mt := range col.Info.MediaTypes {
			if mt == mediaType {
				added = true
				break
			}
		}
		if !added {
			col.Info.MediaTypes = append(col.Info.MediaTypes, mediaType)
		}
		successes = append(successes, StatusDetail{ID: id, Version: version})
	}
	root := b.roots[apiRoot]
	status := &StatusResource{
		ID:               uuid.New().String(),
		Status:           "complete",
		RequestTimestamp: requestTimeStr,
		TotalCount:       len(successes) + len(failures),
		SuccessCount:     len(successes),
		FailureCount:     len(failures),
		PendingCount:     0,
		Successes:        successes,
		Failures:         failures,
	}
	root.Statuses = append(root.Statuses, status)
	return status, nil
}

func (b *MemoryBackend) GetObject(apiRoot, collectionID, objectID string, filter FilterArgs, limit int) (*ObjectsResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	col := b.getCollectionLocked(apiRoot, collectionID)
	if col == nil {
		return nil, nil
	}
	var objects []map[string]interface{}
	for _, o := range col.Objects {
		id, _ := o["id"].(string)
		if id != objectID {
			continue
		}
		obj := make(map[string]interface{})
		for k, v := range o {
			if k == "_date_added" {
				continue
			}
			obj[k] = v
		}
		objects = append(objects, obj)
	}
	if len(objects) == 0 {
		return nil, nil
	}
	res := &ObjectsResult{Headers: make(map[string]string)}
	if limit <= 0 {
		limit = 100
	}
	if len(objects) > limit {
		nextID := uuid.New().String()
		b.nextCursors[nextID] = &cursorEntry{Filter: filter, Created: time.Now(), Objects: objects[limit:]}
		res.More = true
		res.Next = nextID
		objects = objects[:limit]
	}
	res.Objects = objects
	if len(objects) > 0 {
		idVer := make(map[string]string)
		for _, m := range col.Manifest {
			if m.ID == objectID {
				idVer[m.Version] = m.DateAdded
			}
		}
		var first, last string
		for _, o := range objects {
			ver := objectVersion(o, time.Time{})
			if d := idVer[ver]; d != "" {
				if first == "" {
					first = d
				}
				last = d
			}
		}
		if first != "" {
			res.Headers["X-TAXII-Date-Added-First"] = first
			res.Headers["X-TAXII-Date-Added-Last"] = last
		}
	}
	return res, nil
}

func (b *MemoryBackend) DeleteObject(apiRoot, collectionID, objectID string, filter FilterArgs) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	col := b.getCollectionLocked(apiRoot, collectionID)
	if col == nil {
		return nil
	}
	var toRemove []map[string]interface{}
	for _, o := range col.Objects {
		id, _ := o["id"].(string)
		if id == objectID {
			toRemove = append(toRemove, o)
		}
	}
	if len(toRemove) == 0 {
		return fmt.Errorf("taxii: object not found")
	}
	for _, obj := range toRemove {
		ver := objectVersion(obj, time.Time{})
		id, _ := obj["id"].(string)
		for i := 0; i < len(col.Objects); i++ {
			o := col.Objects[i]
			eid, _ := o["id"].(string)
			if eid == id && objectVersion(o, time.Time{}) == ver {
				col.Objects = append(col.Objects[:i], col.Objects[i+1:]...)
				break
			}
		}
		for i := 0; i < len(col.Manifest); i++ {
			if col.Manifest[i].ID == objectID && col.Manifest[i].Version == ver {
				col.Manifest = append(col.Manifest[:i], col.Manifest[i+1:]...)
				break
			}
		}
	}
	return nil
}

func (b *MemoryBackend) GetObjectVersions(apiRoot, collectionID, objectID string, filter FilterArgs, limit int) (*VersionsResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	col := b.getCollectionLocked(apiRoot, collectionID)
	if col == nil {
		return nil, nil
	}
	var versions []string
	for _, m := range col.Manifest {
		if m.ID == objectID {
			versions = append(versions, m.Version)
		}
	}
	if len(versions) == 0 {
		return nil, nil
	}
	if filter.Next != "" {
		cur, ok := b.nextCursors[filter.Next]
		if !ok {
			return nil, fmt.Errorf("taxii: invalid next token")
		}
		return b.applyVersionsCursor(cur, limit)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })
	res := &VersionsResult{Headers: make(map[string]string)}
	if limit <= 0 {
		limit = 100
	}
	if len(versions) > limit {
		nextID := uuid.New().String()
		b.nextCursors[nextID] = &cursorEntry{Filter: filter, Created: time.Now(), Versions: versions[limit:]}
		res.More = true
		res.Next = nextID
		versions = versions[:limit]
	}
	res.Versions = versions
	return res, nil
}

func (b *MemoryBackend) applyVersionsCursor(cur *cursorEntry, limit int) (*VersionsResult, error) {
	versions := cur.Versions
	if limit <= 0 {
		limit = 100
	}
	res := &VersionsResult{Headers: make(map[string]string)}
	if len(versions) > limit {
		nextID := uuid.New().String()
		b.nextCursors[nextID] = &cursorEntry{Filter: cur.Filter, Created: time.Now(), Versions: versions[limit:]}
		res.More = true
		res.Next = nextID
		versions = versions[:limit]
	}
	res.Versions = versions
	delete(b.nextCursors, cur.Filter.Next)
	return res, nil
}

func (b *MemoryBackend) GetStatus(apiRoot, statusID string) (*StatusResource, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	root, ok := b.roots[apiRoot]
	if !ok {
		return nil, nil
	}
	for _, s := range root.Statuses {
		if s.ID == statusID {
			return s, nil
		}
	}
	return nil, nil
}

// SetDiscovery sets the server discovery resource (required for ServerDiscovery to succeed).
func (b *MemoryBackend) SetDiscovery(d *Discovery) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.discovery = d
}

// AddAPIRoot adds an API root with the given info and optional collections.
func (b *MemoryBackend) AddAPIRoot(name string, info APIRootInfo, collections []CollectionInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.roots[name] == nil {
		b.roots[name] = &apiRootData{Statuses: []*StatusResource{}}
	}
	b.roots[name].Info = info
	if len(collections) > 0 {
		b.roots[name].Collections = nil
		for _, c := range collections {
			col := &collectionData{Info: c}
			b.roots[name].Collections = append(b.roots[name].Collections, col)
		}
	}
}
