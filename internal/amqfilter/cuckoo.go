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

package amqfilter

import (
	cuckoolib "github.com/seiflotfy/cuckoofilter"
)

// Compile-time check that CuckooFilter implements Filter.
var _ Filter = (*CuckooFilter)(nil)

// CuckooFilter is the Cuckoo filter implementation of Filter.
type CuckooFilter struct {
	inner    *cuckoolib.Filter
	capacity uint
}

// NewCuckooFilterFromOptions creates a Cuckoo filter from options.
func NewCuckooFilterFromOptions(o CuckooOptions) *CuckooFilter {
	capacity := o.Capacity
	if capacity == 0 {
		capacity = 1000
	}
	return &CuckooFilter{
		inner:    cuckoolib.NewFilter(capacity),
		capacity: capacity,
	}
}

// Add adds value to the filter. Uses InsertUnique for idempotent set semantics.
func (f *CuckooFilter) Add(value []byte) {
	f.inner.InsertUnique(value)
}

// AddString adds the string to the filter.
func (f *CuckooFilter) AddString(s string) {
	f.inner.InsertUnique([]byte(s))
}

// MayContain returns false if value is definitely not in the set, and true if
// it may be in the set.
func (f *CuckooFilter) MayContain(value []byte) bool {
	return f.inner.Lookup(value)
}

// MayContainString returns false if the string is definitely not in the set,
// and true if it may be in the set.
func (f *CuckooFilter) MayContainString(s string) bool {
	return f.inner.Lookup([]byte(s))
}

// Cap returns the configured capacity in elements (implementation-defined;
// for Cuckoo this is the capacity passed at construction).
func (f *CuckooFilter) Cap() uint {
	return f.capacity
}
