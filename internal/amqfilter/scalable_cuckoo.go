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

// Compile-time check that ScalableCuckooFilter implements Filter.
var _ Filter = (*ScalableCuckooFilter)(nil)

// ScalableCuckooFilter is the Scalable Cuckoo filter implementation of Filter.
type ScalableCuckooFilter struct {
	inner *cuckoolib.ScalableCuckooFilter
}

// NewScalableCuckooFilterFromOptions creates a Scalable Cuckoo filter. opts reserved for future use.
func NewScalableCuckooFilterFromOptions(o ScalableCuckooOptions) *ScalableCuckooFilter {
	_ = o
	return &ScalableCuckooFilter{inner: cuckoolib.NewScalableCuckooFilter()}
}

// Add adds value to the filter. Uses InsertUnique for idempotent set semantics.
func (f *ScalableCuckooFilter) Add(value []byte) {
	f.inner.InsertUnique(value)
}

// AddString adds the string to the filter.
func (f *ScalableCuckooFilter) AddString(s string) {
	f.inner.InsertUnique([]byte(s))
}

// MayContain returns false if value is definitely not in the set, and true if it may be.
func (f *ScalableCuckooFilter) MayContain(value []byte) bool {
	return f.inner.Lookup(value)
}

// MayContainString returns false if the string is definitely not in the set, and true if it may be.
func (f *ScalableCuckooFilter) MayContainString(s string) bool {
	return f.inner.Lookup([]byte(s))
}

// Cap returns 0; scalable filters have no single fixed capacity.
func (f *ScalableCuckooFilter) Cap() uint {
	return 0
}
