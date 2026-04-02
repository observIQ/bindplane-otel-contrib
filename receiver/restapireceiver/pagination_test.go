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

package restapireceiver

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// testExtractData extracts data items from a response map for use in pagination tests.
func testExtractData(response map[string]any) []map[string]any {
	// Try common field names used in tests
	for _, fieldName := range []string{"data", "items", "results", "records"} {
		if dataVal, exists := response[fieldName]; exists {
			// Handle []any (from JSON unmarshal)
			if arr, ok := dataVal.([]any); ok {
				result := make([]map[string]any, 0, len(arr))
				for _, item := range arr {
					if m, ok := item.(map[string]any); ok {
						result = append(result, m)
					}
				}
				return result
			}
			// Handle []map[string]any (from test literals)
			if arr, ok := dataVal.([]map[string]any); ok {
				return arr
			}
		}
	}
	return nil
}

func TestBuildPaginationParams_OffsetLimit(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
				StartingOffset:  0,
			},
		},
	}

	state := &paginationState{
		CurrentOffset: 0,
		Limit:         10,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "0", params.Get("offset"))
	require.Equal(t, "10", params.Get("limit"))
}

func TestBuildPaginationParams_OffsetLimit_NonZero(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "skip",
				LimitFieldName:  "take",
				StartingOffset:  0,
			},
		},
	}

	state := &paginationState{
		CurrentOffset: 20,
		Limit:         50,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "20", params.Get("skip"))
	require.Equal(t, "50", params.Get("take"))
}

func TestBuildPaginationParams_PageSize_OneBased(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode:           paginationModePageSize,
			ZeroBasedIndex: false,
			PageSize: PageSizePagination{
				PageNumFieldName:  "page",
				PageSizeFieldName: "size",
				StartingPage:      1,
			},
		},
	}

	state := &paginationState{
		CurrentPage: 1,
		PageSize:    20,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1", params.Get("page"))
	require.Equal(t, "20", params.Get("size"))
}

func TestBuildPaginationParams_PageSize_ZeroBased(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode:           paginationModePageSize,
			ZeroBasedIndex: true,
			PageSize: PageSizePagination{
				PageNumFieldName:  "page",
				PageSizeFieldName: "size",
				StartingPage:      0,
			},
		},
	}

	state := &paginationState{
		CurrentPage: 0,
		PageSize:    20,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "0", params.Get("page"))
	require.Equal(t, "20", params.Get("size"))
}

func TestBuildPaginationParams_None(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeNone,
		},
	}

	state := &paginationState{}
	params := buildPaginationParams(cfg, state)
	require.Empty(t, params)
}

func TestParsePaginationResponse_OffsetLimit_HasMore(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
			},
			TotalRecordCountField: "total",
		},
	}

	// Response with 25 total records, we're at offset 0 with limit 10
	response := map[string]any{
		"data": []map[string]any{
			{"id": "1"}, {"id": "2"}, {"id": "3"}, {"id": "4"}, {"id": "5"},
			{"id": "6"}, {"id": "7"}, {"id": "8"}, {"id": "9"}, {"id": "10"},
		},
		"total": 25,
	}

	state := &paginationState{
		CurrentOffset: 0,
		Limit:         10,
	}

	data := response["data"].([]map[string]any)

	hasMore, err := parsePaginationResponse(cfg, response, data, state, zap.NewNop())
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Equal(t, 25, state.TotalRecords)
}

func TestParsePaginationResponse_OffsetLimit_NoMore(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
			},
			TotalRecordCountField: "total",
		},
	}

	// Response with 5 total records, we're at offset 0 with limit 10
	response := map[string]any{
		"data": []map[string]any{
			{"id": "1"}, {"id": "2"}, {"id": "3"}, {"id": "4"}, {"id": "5"},
		},
		"total": 5,
	}

	state := &paginationState{
		CurrentOffset: 0,
		Limit:         10,
	}

	data := response["data"].([]map[string]any)

	hasMore, err := parsePaginationResponse(cfg, response, data, state, zap.NewNop())
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, 5, state.TotalRecords)
}

func TestParsePaginationResponse_OffsetLimit_NoTotalField(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
			},
		},
	}

	// Response without total field - assume has more if we got a full page
	response := map[string]any{
		"data": []map[string]any{
			{"id": "1"}, {"id": "2"}, {"id": "3"}, {"id": "4"}, {"id": "5"},
			{"id": "6"}, {"id": "7"}, {"id": "8"}, {"id": "9"}, {"id": "10"},
		},
	}

	state := &paginationState{
		CurrentOffset: 0,
		Limit:         10,
	}

	data := response["data"].([]map[string]any)

	hasMore, err := parsePaginationResponse(cfg, response, data, state, zap.NewNop())
	require.NoError(t, err)
	require.True(t, hasMore) // Full page, assume more
}

func TestParsePaginationResponse_OffsetLimit_PartialPage(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
			},
		},
	}

	// Response with only 3 items when limit is 10 - no more pages
	response := map[string]any{
		"data": []map[string]any{
			{"id": "1"}, {"id": "2"}, {"id": "3"},
		},
	}

	state := &paginationState{
		CurrentOffset: 0,
		Limit:         10,
	}

	data := response["data"].([]map[string]any)

	hasMore, err := parsePaginationResponse(cfg, response, data, state, zap.NewNop())
	require.NoError(t, err)
	require.False(t, hasMore) // Partial page, no more
}

func TestParsePaginationResponse_PageSize_HasMore(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModePageSize,
			PageSize: PageSizePagination{
				PageNumFieldName:    "page",
				PageSizeFieldName:   "size",
				TotalPagesFieldName: "total_pages",
			},
		},
	}

	// Response with 5 total pages, we're on page 1
	response := map[string]any{
		"data": []map[string]any{
			{"id": "1"}, {"id": "2"},
		},
		"total_pages": 5,
	}

	state := &paginationState{
		CurrentPage: 1,
		PageSize:    20,
	}

	data := response["data"].([]map[string]any)

	hasMore, err := parsePaginationResponse(cfg, response, data, state, zap.NewNop())
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Equal(t, 5, state.TotalPages)
}

func TestParsePaginationResponse_PageSize_NoMore(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModePageSize,
			PageSize: PageSizePagination{
				PageNumFieldName:    "page",
				PageSizeFieldName:   "size",
				TotalPagesFieldName: "total_pages",
			},
		},
	}

	// Response with 1 total page, we're on page 1
	response := map[string]any{
		"data": []map[string]any{
			{"id": "1"}, {"id": "2"},
		},
		"total_pages": 1,
	}

	state := &paginationState{
		CurrentPage: 1,
		PageSize:    20,
	}

	data := response["data"].([]map[string]any)

	hasMore, err := parsePaginationResponse(cfg, response, data, state, zap.NewNop())
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, 1, state.TotalPages)
}

func TestParsePaginationResponse_PageSize_NoTotalPagesField(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModePageSize,
			PageSize: PageSizePagination{
				PageNumFieldName:  "page",
				PageSizeFieldName: "size",
			},
		},
	}

	// Response without total_pages - assume has more if we got a full page
	response := map[string]any{
		"data": []map[string]any{
			{"id": "1"}, {"id": "2"}, {"id": "3"}, {"id": "4"}, {"id": "5"},
		},
	}

	state := &paginationState{
		CurrentPage: 1,
		PageSize:    5,
	}

	data := response["data"].([]map[string]any)

	hasMore, err := parsePaginationResponse(cfg, response, data, state, zap.NewNop())
	require.NoError(t, err)
	require.True(t, hasMore) // Full page, assume more
}

func TestParsePaginationResponse_PageSize_PartialPage(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModePageSize,
			PageSize: PageSizePagination{
				PageNumFieldName:  "page",
				PageSizeFieldName: "size",
			},
		},
	}

	// Response with only 2 items when page size is 5 - no more pages
	response := map[string]any{
		"data": []map[string]any{
			{"id": "1"}, {"id": "2"},
		},
	}

	state := &paginationState{
		CurrentPage: 1,
		PageSize:    5,
	}

	data := response["data"].([]map[string]any)

	hasMore, err := parsePaginationResponse(cfg, response, data, state, zap.NewNop())
	require.NoError(t, err)
	require.False(t, hasMore) // Partial page, no more
}

func TestUpdatePaginationState_OffsetLimit(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				StartingOffset: 0,
			},
		},
	}

	state := newPaginationState(cfg)
	require.Equal(t, 0, state.CurrentOffset)
	require.Equal(t, 10, state.Limit) // default limit

	// Update after fetching a page
	state.CurrentOffset = 10
	state.Limit = 10

	// Next page should be at offset 20
	updatePaginationState(cfg, state)
	require.Equal(t, 20, state.CurrentOffset)
}

func TestUpdatePaginationState_PageSize_OneBased(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode:           paginationModePageSize,
			ZeroBasedIndex: false,
			PageSize: PageSizePagination{
				StartingPage: 1,
			},
		},
	}

	state := newPaginationState(cfg)
	require.Equal(t, 1, state.CurrentPage)

	// Update after fetching page 1
	state.CurrentPage = 1

	// Next page should be 2
	updatePaginationState(cfg, state)
	require.Equal(t, 2, state.CurrentPage)
}

func TestUpdatePaginationState_PageSize_ZeroBased(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode:           paginationModePageSize,
			ZeroBasedIndex: true,
			PageSize: PageSizePagination{
				StartingPage: 0,
			},
		},
	}

	state := newPaginationState(cfg)
	require.Equal(t, 0, state.CurrentPage)

	// Update after fetching page 0
	state.CurrentPage = 0

	// Next page should be 1
	updatePaginationState(cfg, state)
	require.Equal(t, 1, state.CurrentPage)
}

func TestCheckPageLimit_WithinLimit(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode:      paginationModePageSize,
			PageLimit: 10,
		},
	}

	state := &paginationState{
		CurrentPage:  5,
		PagesFetched: 5,
	}

	withinLimit := checkPageLimit(cfg, state)
	require.True(t, withinLimit)
}

func TestCheckPageLimit_ExceedsLimit(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode:      paginationModePageSize,
			PageLimit: 10,
		},
	}

	state := &paginationState{
		CurrentPage:  11,
		PagesFetched: 11,
	}

	withinLimit := checkPageLimit(cfg, state)
	require.False(t, withinLimit)
}

func TestCheckPageLimit_NoLimit(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode:      paginationModePageSize,
			PageLimit: 0, // 0 means no limit
		},
	}

	state := &paginationState{
		CurrentPage:  100,
		PagesFetched: 100,
	}

	withinLimit := checkPageLimit(cfg, state)
	require.True(t, withinLimit)
}

func TestNewPaginationState_OffsetLimit(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				StartingOffset: 5,
			},
		},
	}

	state := newPaginationState(cfg)
	require.Equal(t, 5, state.CurrentOffset)
	require.Equal(t, 10, state.Limit) // default
	require.Equal(t, 0, state.CurrentPage)
}

func TestNewPaginationState_PageSize_OneBased(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode:           paginationModePageSize,
			ZeroBasedIndex: false,
			PageSize: PageSizePagination{
				StartingPage: 1,
			},
		},
	}

	state := newPaginationState(cfg)
	require.Equal(t, 1, state.CurrentPage)
	require.Equal(t, 20, state.PageSize) // default
	require.Equal(t, 0, state.CurrentOffset)
}

func TestNewPaginationState_PageSize_ZeroBased(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode:           paginationModePageSize,
			ZeroBasedIndex: true,
			PageSize: PageSizePagination{
				StartingPage: 0,
			},
		},
	}

	state := newPaginationState(cfg)
	require.Equal(t, 0, state.CurrentPage)
	require.Equal(t, 20, state.PageSize) // default
	require.Equal(t, 0, state.CurrentOffset)
}

func TestBuildPaginationParams_OffsetLimit_WithToken(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "next_offset",
			},
		},
	}

	state := &paginationState{
		CurrentOffset:      0,
		CurrentOffsetToken: "eyJvZmZzZXQiOjEwfQ==",
		Limit:              10,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "eyJvZmZzZXQiOjEwfQ==", params.Get("offset"))
	require.Equal(t, "10", params.Get("limit"))
}

func TestBuildPaginationParams_OffsetLimit_EmptyToken(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "next_offset",
			},
		},
	}

	state := &paginationState{
		CurrentOffset:      20,
		CurrentOffsetToken: "",
		Limit:              10,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "20", params.Get("offset"))
	require.Equal(t, "10", params.Get("limit"))
}

func TestParseOffsetLimitResponse_TokenPresent_FullPage(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "next_offset",
			},
		},
	}

	// Full page (10 items, limit 10) with a next token → hasMore=true
	response := map[string]any{
		"data": []map[string]any{
			map[string]any{"id": "1"}, map[string]any{"id": "2"}, map[string]any{"id": "3"},
			map[string]any{"id": "4"}, map[string]any{"id": "5"}, map[string]any{"id": "6"},
			map[string]any{"id": "7"}, map[string]any{"id": "8"}, map[string]any{"id": "9"},
			map[string]any{"id": "10"},
		},
		"next_offset": "abc123",
	}

	state := &paginationState{Limit: 10}
	hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Equal(t, "abc123", state.CurrentOffsetToken)
	require.Equal(t, 1, state.PagesFetched)
}

func TestParseOffsetLimitResponse_TokenPresent_PartialPage(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "next_offset",
			},
		},
	}

	// Partial page (1 item, limit 10) with a next token → hasMore=false, but token saved
	response := map[string]any{
		"data":        []any{map[string]any{"id": "1"}},
		"next_offset": "abc123",
	}

	state := &paginationState{Limit: 10}
	hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, "abc123", state.CurrentOffsetToken)
	require.Equal(t, 1, state.PagesFetched)
}

func TestParseOffsetLimitResponse_TokenPresent_EmptyPage(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "next_offset",
			},
		},
	}

	// Empty page with a next token → hasMore=false, token still saved for next poll
	response := map[string]any{
		"next_offset": "bookmark123",
	}

	state := &paginationState{Limit: 10}
	hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, "bookmark123", state.CurrentOffsetToken)
}

func TestParseOffsetLimitResponse_TokenEmpty(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "next_offset",
			},
		},
	}

	response := map[string]any{
		"data":        []any{map[string]any{"id": "1"}},
		"next_offset": "",
	}

	state := &paginationState{Limit: 10, CurrentOffsetToken: "previous"}
	hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, "", state.CurrentOffsetToken)
}

func TestParseOffsetLimitResponse_TokenMissing(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "next_offset",
			},
		},
	}

	response := map[string]any{
		"data": []map[string]any{map[string]any{"id": "1"}},
	}

	state := &paginationState{Limit: 10, CurrentOffsetToken: "previous"}
	hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, "", state.CurrentOffsetToken)
}

func TestParseOffsetLimitResponse_TokenNull(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "next_offset",
			},
		},
	}

	response := map[string]any{
		"data":        []any{map[string]any{"id": "1"}},
		"next_offset": nil,
	}

	state := &paginationState{Limit: 10, CurrentOffsetToken: "previous"}
	hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, "", state.CurrentOffsetToken)
}

func TestParseOffsetLimitResponse_TokenNested(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "pagination.next_cursor",
			},
		},
	}

	response := map[string]any{
		"data": []map[string]any{
			map[string]any{"id": "1"}, map[string]any{"id": "2"}, map[string]any{"id": "3"},
			map[string]any{"id": "4"}, map[string]any{"id": "5"}, map[string]any{"id": "6"},
			map[string]any{"id": "7"}, map[string]any{"id": "8"}, map[string]any{"id": "9"},
			map[string]any{"id": "10"},
		},
		"pagination": map[string]any{
			"next_cursor": "cursor_xyz",
		},
	}

	state := &paginationState{Limit: 10}
	hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Equal(t, "cursor_xyz", state.CurrentOffsetToken)
}

func TestParseOffsetLimitResponse_TokenNumeric(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName:     "offset",
				LimitFieldName:      "limit",
				NextOffsetFieldName: "next_offset",
			},
		},
	}

	fullPage := []any{
		map[string]any{"id": "1"}, map[string]any{"id": "2"}, map[string]any{"id": "3"},
		map[string]any{"id": "4"}, map[string]any{"id": "5"}, map[string]any{"id": "6"},
		map[string]any{"id": "7"}, map[string]any{"id": "8"}, map[string]any{"id": "9"},
		map[string]any{"id": "10"},
	}

	t.Run("float64", func(t *testing.T) {
		response := map[string]any{
			"data":        fullPage,
			"next_offset": float64(42),
		}
		state := &paginationState{Limit: 10}
		hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
		require.NoError(t, err)
		require.True(t, hasMore)
		require.Equal(t, "42", state.CurrentOffsetToken)
	})

	t.Run("int", func(t *testing.T) {
		response := map[string]any{
			"data":        fullPage,
			"next_offset": 99,
		}
		state := &paginationState{Limit: 10}
		hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
		require.NoError(t, err)
		require.True(t, hasMore)
		require.Equal(t, "99", state.CurrentOffsetToken)
	})
}

func TestParseOffsetLimitResponse_NoTokenFieldConfigured(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
			},
			TotalRecordCountField: "total",
		},
	}

	response := map[string]any{
		"data":  []any{map[string]any{"id": "1"}, map[string]any{"id": "2"}},
		"total": float64(10),
	}

	state := &paginationState{
		CurrentOffset: 0,
		Limit:         10,
	}

	hasMore, err := parseOffsetLimitResponse(cfg, response, testExtractData(response), state)
	require.NoError(t, err)
	require.True(t, hasMore)
	require.Equal(t, 10, state.TotalRecords)
	require.Equal(t, "", state.CurrentOffsetToken)
}

func TestBuildPaginationParams_Timestamp_EpochSeconds(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSizeFieldName:  "limit",
				PageSize:           100,
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         100,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1735689600", params.Get("since"))
	require.Equal(t, "100", params.Get("limit"))
}

func TestBuildPaginationParams_Timestamp_EpochMilliseconds(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_ms",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSizeFieldName:  "limit",
				PageSize:           50,
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         50,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1735689600000", params.Get("since"))
	require.Equal(t, "50", params.Get("limit"))
}

func TestBuildPaginationParams_Timestamp_EpochMicroseconds(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_us",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSizeFieldName:  "limit",
				PageSize:           100,
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         100,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1735689600000000", params.Get("since"))
	require.Equal(t, "100", params.Get("limit"))
}

func TestBuildPaginationParams_Timestamp_EpochNanoseconds(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_ns",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSizeFieldName:  "limit",
				PageSize:           100,
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         100,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1735689600000000000", params.Get("since"))
	require.Equal(t, "100", params.Get("limit"))
}

func TestBuildPaginationParams_Timestamp_EpochSeconds_WithOffset(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp:  ts,
		TimestampFromData: true, // should trigger +1s offset
	}

	params := buildPaginationParams(cfg, state)
	// Should be 1 second after the stored timestamp
	require.Equal(t, "1735689601", params.Get("since"))
}

func TestBuildPaginationParams_Timestamp_EpochMilliseconds_WithOffset(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_ms",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp:  ts,
		TimestampFromData: true, // should trigger +1ms offset
	}

	params := buildPaginationParams(cfg, state)
	// Should be 1 millisecond after the stored timestamp
	require.Equal(t, "1735689600001", params.Get("since"))
}

func TestBuildPaginationParams_Timestamp_EpochMicroseconds_WithOffset(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_us",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp:  ts,
		TimestampFromData: true, // should trigger +1us offset
	}

	params := buildPaginationParams(cfg, state)
	// Should be 1 microsecond after the stored timestamp
	require.Equal(t, "1735689600000001", params.Get("since"))
}

func TestBuildPaginationParams_Timestamp_EpochNanoseconds_WithOffset(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_ns",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp:  ts,
		TimestampFromData: true, // should trigger +1ns offset
	}

	params := buildPaginationParams(cfg, state)
	// Should be 1 nanosecond after the stored timestamp
	require.Equal(t, "1735689600000000001", params.Get("since"))
}

func TestNewPaginationState_Timestamp_EpochSeconds(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s",
		StartTimeValue:     "1735689600",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           100,
			},
		},
	}

	state := newPaginationState(cfg)
	expected := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	require.True(t, expected.Equal(state.CurrentTimestamp), "expected %v, got %v", expected, state.CurrentTimestamp)
	require.Equal(t, 100, state.PageSize)
}

func TestNewPaginationState_Timestamp_EpochMilliseconds(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_ms",
		StartTimeValue:     "1735689600000",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           50,
			},
		},
	}

	state := newPaginationState(cfg)
	expected := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	require.True(t, expected.Equal(state.CurrentTimestamp), "expected %v, got %v", expected, state.CurrentTimestamp)
	require.Equal(t, 50, state.PageSize)
}

func TestNewPaginationState_Timestamp_EpochMicroseconds(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_us",
		StartTimeValue:     "1735689600000000",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           100,
			},
		},
	}

	state := newPaginationState(cfg)
	expected := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	require.True(t, expected.Equal(state.CurrentTimestamp), "expected %v, got %v", expected, state.CurrentTimestamp)
	require.Equal(t, 100, state.PageSize)
}

func TestNewPaginationState_Timestamp_EpochNanoseconds(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_ns",
		StartTimeValue:     "1735689600000000000",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           100,
			},
		},
	}

	state := newPaginationState(cfg)
	expected := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	require.True(t, expected.Equal(state.CurrentTimestamp), "expected %v, got %v", expected, state.CurrentTimestamp)
	require.Equal(t, 100, state.PageSize)
}

func TestBuildPaginationParams_Timestamp_EpochSecondsFractional(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s_frac",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSizeFieldName:  "limit",
				PageSize:           100,
			},
		},
	}

	// Time with 123ms 456us — formatting should output fractional seconds
	ts := time.Date(2025, 1, 1, 0, 0, 0, 123456000, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         100,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1735689600.123456", params.Get("since"))
	require.Equal(t, "100", params.Get("limit"))
}

func TestBuildPaginationParams_Timestamp_EpochSecondsFractional_WholeSeconds(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s_frac",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
			},
		},
	}

	// Whole seconds — no fractional part in output
	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         100,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1735689600", params.Get("since"))
}

func TestBuildPaginationParams_Timestamp_EpochSecondsFractional_WithOffset(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s_frac",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 123456000, time.UTC)
	state := &paginationState{
		CurrentTimestamp:  ts,
		TimestampFromData: true, // should trigger +1us offset
	}

	params := buildPaginationParams(cfg, state)
	// Should be 1 microsecond after the stored timestamp
	require.Equal(t, "1735689600.123457", params.Get("since"))
}

func TestNewPaginationState_Timestamp_EpochSecondsFractional_Ms(t *testing.T) {
	// "1735689600.123" — millisecond precision fractional seconds
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s_frac",
		StartTimeValue:     "1735689600.123",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           100,
			},
		},
	}

	state := newPaginationState(cfg)
	expected := time.Date(2025, 1, 1, 0, 0, 0, 123000000, time.UTC)
	require.True(t, expected.Equal(state.CurrentTimestamp), "expected %v, got %v", expected, state.CurrentTimestamp)
	require.Equal(t, 100, state.PageSize)
}

func TestNewPaginationState_Timestamp_EpochSecondsFractional_Us(t *testing.T) {
	// "1735689600.123456" — microsecond precision fractional seconds
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s_frac",
		StartTimeValue:     "1735689600.123456",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           50,
			},
		},
	}

	state := newPaginationState(cfg)
	expected := time.Date(2025, 1, 1, 0, 0, 0, 123456000, time.UTC)
	require.True(t, expected.Equal(state.CurrentTimestamp), "expected %v, got %v", expected, state.CurrentTimestamp)
	require.Equal(t, 50, state.PageSize)
}

func TestNewPaginationState_Timestamp_EpochSecondsFractional_Ns(t *testing.T) {
	// "1735689600.123456789" — nanosecond precision fractional seconds
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s_frac",
		StartTimeValue:     "1735689600.123456789",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           50,
			},
		},
	}

	state := newPaginationState(cfg)
	expected := time.Date(2025, 1, 1, 0, 0, 0, 123456789, time.UTC)
	require.True(t, expected.Equal(state.CurrentTimestamp), "expected %v, got %v", expected, state.CurrentTimestamp)
	require.Equal(t, 50, state.PageSize)
}

func TestNewPaginationState_Timestamp_EpochSecondsFractional_WholeSeconds(t *testing.T) {
	// "1735689600" — whole seconds with epoch_s_frac format
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s_frac",
		StartTimeValue:     "1735689600",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           100,
			},
		},
	}

	state := newPaginationState(cfg)
	expected := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	require.True(t, expected.Equal(state.CurrentTimestamp), "expected %v, got %v", expected, state.CurrentTimestamp)
	require.Equal(t, 100, state.PageSize)
}

func TestParseTimestampValue_FractionalFloat(t *testing.T) {
	// Float64 seconds with fractional part
	result := parseTimestampValue(1735689600.123, "")
	expected := time.Date(2025, 1, 1, 0, 0, 0, 123000000, time.UTC)
	require.InDelta(t, expected.UnixNano(), result.UnixNano(), 1000, "expected %v, got %v", expected, result)

	// Float64 milliseconds with fractional part
	result = parseTimestampValue(1735689600123.456, "")
	expected = time.Date(2025, 1, 1, 0, 0, 0, 123456000, time.UTC)
	require.InDelta(t, expected.UnixNano(), result.UnixNano(), 1000, "expected %v, got %v", expected, result)
}

func TestParseTimestampValue_ConfiguredFormat(t *testing.T) {
	// Timestamp with milliseconds and no-colon timezone offset.
	const format = "2006-01-02T15:04:05.000-0700"

	result := parseTimestampValue("2026-03-29T02:09:21.550+0000", format)
	expected := time.Date(2026, 3, 29, 2, 9, 21, 550000000, time.UTC)
	require.True(t, expected.Equal(result), "expected %v, got %v", expected, result)

	// Regex-based fallback handles ±HHMM timezone offsets
	result = parseTimestampValue("2026-03-29T02:09:21.550+0000", "")
	require.True(t, expected.Equal(result), "expected regex fallback to parse ±HHMM timezone, got %v", result)
}

func TestParseTimestampString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantOK   bool
		wantTime time.Time
	}{
		// RFC3339 / ISO8601 with T separator
		{
			name:     "RFC3339 with Z",
			input:    "2024-06-15T10:30:00Z",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			name:     "RFC3339 with positive offset",
			input:    "2024-06-15T10:30:00+05:30",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 0, time.FixedZone("", 5*3600+30*60)),
		},
		{
			name:     "RFC3339 with negative offset",
			input:    "2024-06-15T10:30:00-04:00",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 0, time.FixedZone("", -4*3600)),
		},
		// Fractional seconds of varying precision
		{
			name:     "milliseconds with Z",
			input:    "2024-06-15T10:30:00.123Z",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 123000000, time.UTC),
		},
		{
			name:     "microseconds with Z",
			input:    "2024-06-15T10:30:00.123456Z",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 123456000, time.UTC),
		},
		{
			name:     "nanoseconds with Z",
			input:    "2024-06-15T10:30:00.123456789Z",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 123456789, time.UTC),
		},
		{
			name:     "single fractional digit",
			input:    "2024-06-15T10:30:00.1Z",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 100000000, time.UTC),
		},
		// No-colon timezone offset (±HHMM)
		{
			name:     "no-colon tz offset with fractional",
			input:    "2024-06-15T10:30:00.550+0000",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 550000000, time.UTC),
		},
		{
			name:     "no-colon negative tz offset",
			input:    "2024-06-15T10:30:00-0500",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 0, time.FixedZone("", -5*3600)),
		},
		// Short timezone (±HH)
		{
			name:     "short tz offset",
			input:    "2024-06-15T10:30:00+05",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 0, time.FixedZone("", 5*3600)),
		},
		// Space separator instead of T
		{
			name:     "space separator with tz",
			input:    "2024-06-15 10:30:00+00:00",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			name:     "space separator with microseconds",
			input:    "2024-06-15 10:30:00.123456-07:00",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 123456000, time.FixedZone("", -7*3600)),
		},
		// No timezone
		{
			name:     "no timezone",
			input:    "2024-06-15T10:30:00",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			name:     "space separator no timezone",
			input:    "2024-06-15 10:30:00",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		},
		// Date only
		{
			name:     "date only",
			input:    "2024-06-15",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
		},
		// Whitespace trimming
		{
			name:     "leading/trailing whitespace",
			input:    "  2024-06-15T10:30:00Z  ",
			wantOK:   true,
			wantTime: time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		},
		// Non-matching inputs
		{
			name:   "empty string",
			input:  "",
			wantOK: false,
		},
		{
			name:   "plain text",
			input:  "not-a-timestamp",
			wantOK: false,
		},
		{
			name:   "epoch number as string",
			input:  "1735689600",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTimestampString(tt.input)
			require.Equal(t, tt.wantOK, ok, "parseTimestampString(%q) ok", tt.input)
			if tt.wantOK {
				require.True(t, tt.wantTime.Equal(got),
					"parseTimestampString(%q) = %v, want %v", tt.input, got, tt.wantTime)
			}
		})
	}
}

func TestParsePaginationResponse_WithDataArray(t *testing.T) {
	cfg := &Config{
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
		},
	}

	// Response where data is an array (not in a map)
	responseData := []map[string]any{
		{"id": "1"}, {"id": "2"}, {"id": "3"}, {"id": "4"}, {"id": "5"},
		{"id": "6"}, {"id": "7"}, {"id": "8"}, {"id": "9"}, {"id": "10"},
	}

	// Convert to JSON and back to simulate real response
	jsonBytes, _ := json.Marshal(responseData)
	var response any
	json.Unmarshal(jsonBytes, &response)

	state := &paginationState{
		CurrentOffset: 0,
		Limit:         10,
	}

	// Extract data for pagination parsing
	responseMap := map[string]any{"data": response}
	data := extractDataFromResponse(responseMap, "", nil)

	// When response is directly an array, we need to handle it differently
	// For now, we'll assume if we got a full page, there might be more
	hasMore, err := parsePaginationResponse(cfg, responseMap, data, state, zap.NewNop())
	require.NoError(t, err)
	require.True(t, hasMore) // Full page of 10 items
}

func TestBuildPaginationParams_Timestamp_EndParam(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "start_time",
		EndTimeParamName:   "end_time",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSizeFieldName:  "limit",
				PageSize:           100,
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         100,
	}

	before := time.Now().UTC()
	params := buildPaginationParams(cfg, state)
	after := time.Now().UTC()

	// Start param should be the configured timestamp
	require.Equal(t, "2025-01-01T00:00:00Z", params.Get("start_time"))

	// End param should be approximately now
	endStr := params.Get("end_time")
	require.NotEmpty(t, endStr)
	endTime, err := time.Parse(time.RFC3339, endStr)
	require.NoError(t, err)
	require.False(t, endTime.Before(before.Truncate(time.Second)))
	require.False(t, endTime.After(after.Add(time.Second)))

	require.Equal(t, "100", params.Get("limit"))
}

func TestBuildPaginationParams_Timestamp_EndParam_Epoch(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		EndTimeParamName:   "until",
		TimestampFormat:    "epoch_s",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           50,
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         50,
	}

	before := time.Now().UTC()
	params := buildPaginationParams(cfg, state)

	require.Equal(t, "1735689600", params.Get("since"))

	endStr := params.Get("until")
	require.NotEmpty(t, endStr)
	// Parse the epoch value and verify it's close to now
	var endEpoch int64
	_, err := fmt.Sscanf(endStr, "%d", &endEpoch)
	require.NoError(t, err)
	require.InDelta(t, before.Unix(), endEpoch, 2)
}

func TestBuildPaginationParams_Timestamp_EndParam_FixedValue(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "start_time",
		EndTimeParamName:   "end_time",
		EndTimeValue:       "2025-06-01T00:00:00Z",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSizeFieldName:  "limit",
				PageSize:           100,
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         100,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "2025-01-01T00:00:00Z", params.Get("start_time"))
	require.Equal(t, "2025-06-01T00:00:00Z", params.Get("end_time"))
	require.Equal(t, "100", params.Get("limit"))
}

func TestBuildPaginationParams_Timestamp_EndParam_FixedEpoch(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		EndTimeParamName:   "until",
		EndTimeValue:       "1748736000",
		TimestampFormat:    "epoch_s",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           50,
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         50,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1735689600", params.Get("since"))
	require.Equal(t, "1748736000", params.Get("until"))
}

func TestBuildPaginationParams_Timestamp_NoEndParam(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		TimestampFormat:    "epoch_s",
		Pagination: PaginationConfig{
			Mode: paginationModeTimestamp,
			Timestamp: TimestampPagination{
				TimestampFieldName: "ts",
				PageSize:           100,
			},
		},
	}

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &paginationState{
		CurrentTimestamp: ts,
		PageSize:         100,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1735689600", params.Get("since"))
	require.Empty(t, params.Get("end_time"))
	require.Empty(t, params.Get("until"))
}

func TestBuildPaginationParams_TimeBound_OffsetLimit(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "from",
		StartTimeValue:     "2025-01-01T00:00:00Z",
		EndTimeParamName:   "to",
		EndTimeValue:       "2025-06-01T00:00:00Z",
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
			},
		},
	}

	state := &paginationState{
		CurrentOffset: 0,
		Limit:         10,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "0", params.Get("offset"))
	require.Equal(t, "10", params.Get("limit"))
	require.Equal(t, "2025-01-01T00:00:00Z", params.Get("from"))
	require.Equal(t, "2025-06-01T00:00:00Z", params.Get("to"))
}

func TestBuildPaginationParams_TimeBound_PageSize(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "since",
		StartTimeValue:     "1735689600",
		EndTimeParamName:   "until",
		EndTimeValue:       "1748736000",
		TimestampFormat:    "epoch_s",
		Pagination: PaginationConfig{
			Mode: paginationModePageSize,
			PageSize: PageSizePagination{
				PageNumFieldName:  "page",
				PageSizeFieldName: "size",
			},
		},
	}

	state := &paginationState{
		CurrentPage: 1,
		PageSize:    20,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "1", params.Get("page"))
	require.Equal(t, "20", params.Get("size"))
	require.Equal(t, "1735689600", params.Get("since"))
	require.Equal(t, "1748736000", params.Get("until"))
}

func TestBuildPaginationParams_TimeBound_NoPagination(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "start_date",
		StartTimeValue:     "2025-03-01T00:00:00Z",
		EndTimeParamName:   "end_date",
		EndTimeValue:       "now",
		Pagination: PaginationConfig{
			Mode: paginationModeNone,
		},
	}

	state := &paginationState{}

	before := time.Now().UTC()
	params := buildPaginationParams(cfg, state)
	after := time.Now().UTC()

	require.Equal(t, "2025-03-01T00:00:00Z", params.Get("start_date"))

	// End should be approximately now in RFC3339
	endStr := params.Get("end_date")
	require.NotEmpty(t, endStr)
	endTime, err := time.Parse(time.RFC3339, endStr)
	require.NoError(t, err)
	require.False(t, endTime.Before(before.Truncate(time.Second)))
	require.False(t, endTime.After(after.Add(time.Second)))
}

func TestBuildPaginationParams_TimeBound_EndTimeOnly(t *testing.T) {
	cfg := &Config{
		EndTimeParamName: "before",
		EndTimeValue:     "2025-12-31T23:59:59Z",
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				OffsetFieldName: "offset",
				LimitFieldName:  "limit",
			},
		},
	}

	state := &paginationState{
		CurrentOffset: 0,
		Limit:         10,
	}

	params := buildPaginationParams(cfg, state)
	require.Equal(t, "0", params.Get("offset"))
	require.Equal(t, "10", params.Get("limit"))
	require.Empty(t, params.Get("from"))
	require.Equal(t, "2025-12-31T23:59:59Z", params.Get("before"))
}

func TestBuildPaginationParams_TimeBound_StartTimeNow(t *testing.T) {
	cfg := &Config{
		StartTimeParamName: "from",
		StartTimeValue:     "now",
		TimestampFormat:    "epoch_s",
		Pagination: PaginationConfig{
			Mode: paginationModeNone,
		},
	}

	state := &paginationState{}

	before := time.Now().UTC()
	params := buildPaginationParams(cfg, state)

	fromStr := params.Get("from")
	require.NotEmpty(t, fromStr)
	var fromEpoch int64
	_, err := fmt.Sscanf(fromStr, "%d", &fromEpoch)
	require.NoError(t, err)
	require.InDelta(t, before.Unix(), fromEpoch, 2)
}
