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
	"github.com/bits-and-blooms/bloom/v3"
)

// Compile-time check that BloomFilter implements Filter.
var _ Filter = (*BloomFilter)(nil)

// BloomFilter is the Bloom-filter implementation of Filter. Use NewBloomFilterFromOptions
// or NewBloomFilter to create one.
type BloomFilter struct {
	inner *bloom.BloomFilter
}

// NewBloomFilterFromOptions creates a Bloom filter from options. MaxEstimatedCount
// is the element count passed to sizing (callers should validate it is > 0).
func NewBloomFilterFromOptions(o BloomOptions) *BloomFilter {
	n := o.MaxEstimatedCount
	fpr := o.FalsePositiveRate
	if fpr <= 0 {
		fpr = 0.01
	}
	return &BloomFilter{
		inner: bloom.NewWithEstimates(n, fpr),
	}
}

// NewBloomFilter creates a Bloom filter sized for approximately maxEstimatedCount
// elements with the given falsePositiveRate (e.g. 0.01 for 1%). MayContain
// returns false only when the value is definitely not in the set; true means
// the value may be in the set (possibly a false positive).
func NewBloomFilter(maxEstimatedCount uint, falsePositiveRate float64) *BloomFilter {
	return NewBloomFilterFromOptions(BloomOptions{
		MaxEstimatedCount: maxEstimatedCount,
		FalsePositiveRate: falsePositiveRate,
	})
}

// Add adds value to the filter. It is idempotent.
func (f *BloomFilter) Add(value []byte) error {
	f.inner.Add(value)
	return nil
}

// AddString adds the string to the filter. Convenient for IPs, domains, etc.
func (f *BloomFilter) AddString(s string) error {
	f.inner.AddString(s)
	return nil
}

// MayContain returns false if value is definitely not in the set, and true if
// it may be in the set (true positive or false positive). Do not treat true
// as definitive membership.
func (f *BloomFilter) MayContain(value []byte) bool {
	return f.inner.Test(value)
}

// MayContainString returns false if the string is definitely not in the set,
// and true if it may be in the set.
func (f *BloomFilter) MayContainString(s string) bool {
	return f.inner.TestString(s)
}

// Cap returns the capacity of the filter in bits (the size m of the underlying
// bit array). Use for sizing, serialization, or reporting; size in bytes is
// approximately Cap()/8.
func (f *BloomFilter) Cap() uint {
	return f.inner.Cap()
}
