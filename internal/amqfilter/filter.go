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

// Package amqfilter provides a pluggable abstraction for probabilistic (or exact)
// set membership, used for threat intelligence and other filtering. Implementations
// include Bloom, Cuckoo, and Scalable Cuckoo filters; create them via
// NewFilterFromConfig with the appropriate options type.
//
// A Filter answers: "might this value be in the set?" MayContain returns false
// only when the value is definitely not in the set. When MayContain returns true,
// the value may be in the set (true positive) or may be a false positive; callers
// must not treat true as definitive membership.
package amqfilter

import (
	"errors"
	"fmt"
)

// Filter is the common interface for set-membership filters. Values are []byte
// (or strings via AddString/MayContainString) so any observable type (IPs,
// domains, hashes, etc.) can be used.
type Filter interface {
	// Add adds a value to the filter.
	// For some probabilistic filters, insertion may fail (e.g. cuckoo filters
	// when the structure cannot accommodate the fingerprint), so callers should
	// handle returned errors.
	Add(value []byte) error
	// AddString adds a string to the filter.
	AddString(s string) error
	MayContain(value []byte) bool
	MayContainString(s string) bool
	Cap() uint
}

// FilterConfig is the dependency-injection interface for filter configuration.
// Each filter kind has its own options struct implementing FilterConfig; pass
// one to NewFilterFromConfig to create a filter. Only our option types implement it.
type FilterConfig interface {
	FilterKind() Kind
}

// Kind identifies the filter algorithm (bloom, cuckoo, scalable cuckoo).
type Kind string

// FilterKind constants name the algorithm for NewFilterFromConfig / NewFilter.
const (
	KindBloom          Kind = "bloom"
	KindCuckoo         Kind = "cuckoo"
	KindScalableCuckoo Kind = "scalable_cuckoo"
)

var (
	// ErrFilterAddStringFailed is returned when AddString cannot insert into the filter.
	ErrFilterAddStringFailed = errors.New("failed to add string to filter")
	// ErrFilterAddFailed is returned when Add cannot insert into the filter.
	ErrFilterAddFailed = errors.New("failed to add value to filter")
	// ErrFilterMayContainFailed is returned when a byte-slice membership check cannot be completed.
	ErrFilterMayContainFailed = errors.New("failed to check if value may contain in filter")
	// ErrFilterMayContainStringFailed is returned when a string membership check cannot be completed.
	ErrFilterMayContainStringFailed = errors.New("failed to check if string may contain in filter")
	// ErrFilterCapFailed is returned when the filter capacity cannot be read.
	ErrFilterCapFailed = errors.New("failed to get filter capacity")
	// ErrFilterKindFailed is returned when the filter kind cannot be determined.
	ErrFilterKindFailed = errors.New("failed to get filter kind")
	// ErrFilterNewFailed is returned when a filter instance cannot be constructed.
	ErrFilterNewFailed = errors.New("failed to create filter")
	// ErrFilterUnknownKind is returned when the requested filter kind is not recognized.
	ErrFilterUnknownKind = errors.New("unknown filter kind")
)

// BloomOptions configures a Bloom filter. Used with NewFilterFromConfig or
// NewFilter(KindBloom, opts). MaxEstimatedCount is the element count used for sizing
// at the given false positive rate.
type BloomOptions struct {
	MaxEstimatedCount uint
	FalsePositiveRate float64
}

// FilterKind implements FilterConfig.
func (BloomOptions) FilterKind() Kind { return KindBloom }

// CuckooOptions configures a Cuckoo filter. Capacity is the expected number of elements.
type CuckooOptions struct {
	Capacity uint
}

// FilterKind implements FilterConfig.
func (CuckooOptions) FilterKind() Kind { return KindCuckoo }

// ScalableCuckooOptions is an empty config type for scalable_cuckoo. The
// github.com/seiflotfy/cuckoofilter scalable implementation does not expose
// public tuning knobs; the filter always uses that package's defaults.
type ScalableCuckooOptions struct{}

// FilterKind implements FilterConfig.
func (ScalableCuckooOptions) FilterKind() Kind { return KindScalableCuckoo }

// NewFilterFromConfig creates a filter from a config
// The concrete type of c determines the filter implementation.
func NewFilterFromConfig(c FilterConfig) (Filter, error) {
	switch c.FilterKind() {
	case KindBloom:
		o, ok := c.(BloomOptions)
		if !ok {
			return nil, fmt.Errorf("filter: Bloom requires BloomOptions, got %T", c)
		}
		return NewBloomFilterFromOptions(o), nil
	case KindCuckoo:
		o, ok := c.(CuckooOptions)
		if !ok {
			return nil, fmt.Errorf("filter: Cuckoo requires CuckooOptions, got %T", c)
		}
		return NewCuckooFilterFromOptions(o), nil
	case KindScalableCuckoo:
		o, ok := c.(ScalableCuckooOptions)
		if !ok {
			return nil, fmt.Errorf("filter: ScalableCuckoo requires ScalableCuckooOptions, got %T", c)
		}
		return NewScalableCuckooFilterFromOptions(o), nil

	default:
		return nil, fmt.Errorf("filter: unknown kind %q", c.FilterKind())
	}
}

// NewFilter creates a filter of the given kind with the provided options.
// opts must be the matching options type (BloomOptions, CuckooOptions, or ScalableCuckooOptions).
// Prefer NewFilterFromConfig for dependency injection.
func NewFilter(kind Kind, opts interface{}) (Filter, error) {
	switch kind {
	case KindBloom:
		o, ok := opts.(BloomOptions)
		if !ok {
			return nil, fmt.Errorf("filter: Bloom requires BloomOptions, got %T", opts)
		}
		return NewFilterFromConfig(o)
	case KindCuckoo:
		o, ok := opts.(CuckooOptions)
		if !ok {
			return nil, fmt.Errorf("filter: Cuckoo requires CuckooOptions, got %T", opts)
		}
		return NewFilterFromConfig(o)
	case KindScalableCuckoo:
		o, ok := opts.(ScalableCuckooOptions)
		if !ok {
			return nil, fmt.Errorf("filter: ScalableCuckoo requires ScalableCuckooOptions, got %T", opts)
		}
		return NewFilterFromConfig(o)
	default:
		return nil, fmt.Errorf("filter: unknown kind %q", kind)
	}
}
