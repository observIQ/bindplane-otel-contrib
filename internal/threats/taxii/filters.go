package taxii

import (
	"fmt"
	"net/url"
)

// Filter holds query parameters for Get Objects, Get Manifest, etc.
// See TAXII 2.1 spec: added_after, limit, next, and match[<prop>] filters.
type Filter struct {
	Limit      int               // max items per page; 0 = server default
	Next       string            // pagination token from previous response
	AddedAfter string            // ISO8601 timestamp; only objects added after this time
	Match      map[string]string // match[id], match[type], match[version], etc.; use single value per key
}

// Query returns url.Values for the filter. Omitted/zero values are not set.
func (f Filter) Query() url.Values {
	q := url.Values{}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", f.Limit))
	}
	if f.Next != "" {
		q.Set("next", f.Next)
	}
	if f.AddedAfter != "" {
		q.Set("added_after", f.AddedAfter)
	}
	for k, v := range f.Match {
		if v != "" {
			q.Set("match["+k+"]", v)
		}
	}
	return q
}
