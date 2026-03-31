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
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// toInt coerces a value to int, handling float64, int, and string.
// Returns (value, true) on success, (0, false) if the value cannot be converted.
func toInt(v any) (int, bool) {
	switch val := v.(type) {
	case float64:
		return int(val), true
	case int:
		return val, true
	case string:
		n, err := strconv.Atoi(val)
		return n, err == nil
	default:
		return 0, false
	}
}

// paginationState tracks the current state of pagination.
type paginationState struct {
	// For offset/limit pagination
	CurrentOffset      int    `json:"current_offset,omitempty"`
	CurrentOffsetToken string `json:"current_offset_token,omitempty"` // token-based (cursor) offset
	Limit              int    `json:"limit,omitempty"`

	// For page/size pagination
	CurrentPage int `json:"current_page,omitempty"`
	PageSize    int `json:"page_size,omitempty"`

	// For timestamp-based pagination
	CurrentTimestamp  time.Time `json:"current_timestamp,omitempty"`
	TimestampFromData bool      `json:"timestamp_from_data,omitempty"` // true if CurrentTimestamp was set from response data (vs initial config)

	// Metadata
	TotalRecords int `json:"total_records,omitempty"`
	TotalPages   int `json:"total_pages,omitempty"`
	PagesFetched int `json:"pages_fetched,omitempty"`
}

// newPaginationState creates a new pagination state based on the configuration.
func newPaginationState(cfg *Config) *paginationState {
	state := &paginationState{
		Limit:    10, // default limit
		PageSize: 20, // default page size
	}

	switch cfg.Pagination.Mode {
	case paginationModeOffsetLimit:
		state.CurrentOffset = cfg.Pagination.OffsetLimit.StartingOffset
		// Use a default limit - this will be sent as a query parameter
		// The actual page size may differ based on API response
		state.Limit = 10

	case paginationModePageSize:
		if cfg.Pagination.ZeroBasedIndex {
			state.CurrentPage = cfg.Pagination.PageSize.StartingPage
		} else {
			state.CurrentPage = cfg.Pagination.PageSize.StartingPage
		}
		if cfg.Pagination.PageSize.PageSizeFieldName != "" {
			// Use a default page size if not specified
			state.PageSize = 20
		}

	case paginationModeTimestamp:
		// Set initial timestamp if provided, otherwise start from zero time.
		// Config validation ensures the timestamp is parseable.
		if cfg.Pagination.Timestamp.InitialTimestamp != "" {
			if isEpochFormat(cfg.Pagination.Timestamp.TimestampFormat) {
				// Parse epoch numeric value
				if t, err := parseEpochTimestamp(cfg.Pagination.Timestamp.InitialTimestamp, cfg.Pagination.Timestamp.TimestampFormat); err == nil {
					state.CurrentTimestamp = t
				}
			} else {
				// First try the user's configured format (they likely copied the timestamp from the API)
				if cfg.Pagination.Timestamp.TimestampFormat != "" {
					if t, err := time.Parse(cfg.Pagination.Timestamp.TimestampFormat, cfg.Pagination.Timestamp.InitialTimestamp); err == nil {
						state.CurrentTimestamp = t
					}
				}
				// Fall back to RFC3339 (the default format)
				if state.CurrentTimestamp.IsZero() {
					if t, err := time.Parse(time.RFC3339, cfg.Pagination.Timestamp.InitialTimestamp); err == nil {
						state.CurrentTimestamp = t
					}
				}
			}
		}
		if cfg.Pagination.Timestamp.PageSize > 0 {
			state.PageSize = cfg.Pagination.Timestamp.PageSize
		} else {
			state.PageSize = 100 // Default page size for timestamp pagination
		}
	}

	// Set limit if configured for offset/limit pagination
	if cfg.Pagination.Mode == paginationModeOffsetLimit &&
		cfg.Pagination.OffsetLimit.LimitFieldName != "" {
		state.Limit = 10 // reasonable default
	}

	return state
}

// buildPaginationParams builds query parameters for pagination based on the current state.
func buildPaginationParams(cfg *Config, state *paginationState) url.Values {
	params := url.Values{}

	switch cfg.Pagination.Mode {
	case paginationModeOffsetLimit:
		if cfg.Pagination.OffsetLimit.OffsetFieldName != "" {
			if state.CurrentOffsetToken != "" {
				// Use token-based offset when available
				params.Set(cfg.Pagination.OffsetLimit.OffsetFieldName, state.CurrentOffsetToken)
			} else {
				params.Set(cfg.Pagination.OffsetLimit.OffsetFieldName, fmt.Sprintf("%d", state.CurrentOffset))
			}
		}
		if cfg.Pagination.OffsetLimit.LimitFieldName != "" {
			params.Set(cfg.Pagination.OffsetLimit.LimitFieldName, fmt.Sprintf("%d", state.Limit))
		}

	case paginationModePageSize:
		if cfg.Pagination.PageSize.PageNumFieldName != "" {
			params.Set(cfg.Pagination.PageSize.PageNumFieldName, fmt.Sprintf("%d", state.CurrentPage))
		}
		if cfg.Pagination.PageSize.PageSizeFieldName != "" {
			params.Set(cfg.Pagination.PageSize.PageSizeFieldName, fmt.Sprintf("%d", state.PageSize))
		}

	case paginationModeTimestamp:
		// Add page size parameter
		if cfg.Pagination.Timestamp.PageSizeFieldName != "" {
			params.Set(cfg.Pagination.Timestamp.PageSizeFieldName, fmt.Sprintf("%d", state.PageSize))
		}
		// Add timestamp parameter if we have one
		if !state.CurrentTimestamp.IsZero() {
			if cfg.Pagination.Timestamp.ParamName != "" {
				timestampForRequest := state.CurrentTimestamp
				// Check if we should add an offset to avoid re-fetching the same record.
				// We add the offset when:
				// 1. pagesFetched > 0: Within a poll cycle, after the first page
				// 2. timestampFromData is true: The timestamp came from response data (not initial config),
				//    meaning we've already fetched records up to this timestamp in a previous cycle
				if state.PagesFetched > 0 || state.TimestampFromData {
					// Increment by the minimum resolution of the configured format to avoid
					// re-fetching the same record.
					switch cfg.Pagination.Timestamp.TimestampFormat {
					case epochSeconds:
						timestampForRequest = timestampForRequest.Add(time.Second)
					case epochMilliseconds:
						timestampForRequest = timestampForRequest.Add(time.Millisecond)
					case epochMicroseconds:
						timestampForRequest = timestampForRequest.Add(time.Microsecond)
					case epochNanoseconds:
						timestampForRequest = timestampForRequest.Add(time.Nanosecond)
					case epochSecondsFractional:
						// Fractional seconds — increment by 1 microsecond as a reasonable default.
						timestampForRequest = timestampForRequest.Add(time.Microsecond)
					default:
						// For string formats, increment by 1 microsecond since most formats
						// preserve microsecond precision at best.
						timestampForRequest = timestampForRequest.Add(time.Microsecond)
					}
				}
				// Use configured format or default to RFC3339
				format := cfg.Pagination.Timestamp.TimestampFormat
				if isEpochFormat(format) {
					params.Set(cfg.Pagination.Timestamp.ParamName, formatTimestampEpoch(timestampForRequest, format))
				} else {
					if format == "" {
						format = time.RFC3339
					}
					params.Set(cfg.Pagination.Timestamp.ParamName, timestampForRequest.Format(format))
				}
			}
		}

		// Add end timestamp parameter if configured (bounded time range)
		if cfg.Pagination.Timestamp.EndParamName != "" {
			now := time.Now().UTC()
			format := cfg.Pagination.Timestamp.TimestampFormat
			if isEpochFormat(format) {
				params.Set(cfg.Pagination.Timestamp.EndParamName, formatTimestampEpoch(now, format))
			} else {
				if format == "" {
					format = time.RFC3339
				}
				params.Set(cfg.Pagination.Timestamp.EndParamName, now.Format(format))
			}
		}

	case paginationModeNone:
		// No pagination parameters
	}

	return params
}

// parsePaginationResponse parses the pagination response to determine if there are more pages.
// It also updates the state with metadata from the response.
// The extractedData parameter contains the already-extracted data array from extractDataFromResponse.
func parsePaginationResponse(cfg *Config, response any, extractedData []map[string]any, state *paginationState, logger *zap.Logger) (bool, error) {
	switch cfg.Pagination.Mode {
	case paginationModeOffsetLimit:
		return parseOffsetLimitResponse(cfg, response, extractedData, state)

	case paginationModePageSize:
		return parsePageSizeResponse(cfg, response, extractedData, state)

	case paginationModeTimestamp:
		return parseTimestampResponse(cfg, extractedData, state, logger)

	case paginationModeNone:
		return false, nil

	default:
		return false, fmt.Errorf("unsupported pagination mode: %s", cfg.Pagination.Mode)
	}
}

// parseOffsetLimitResponse parses the response for offset/limit pagination.
func parseOffsetLimitResponse(cfg *Config, response any, extractedData []map[string]any, state *paginationState) (bool, error) {
	// If NextOffsetFieldName is configured, use token-based offset extraction
	if cfg.Pagination.OffsetLimit.NextOffsetFieldName != "" {
		responseMap, ok := response.(map[string]any)
		if !ok {
			state.CurrentOffsetToken = ""
			return false, nil
		}

		tokenVal, exists := getNestedField(responseMap, cfg.Pagination.OffsetLimit.NextOffsetFieldName)
		if !exists || tokenVal == nil {
			state.CurrentOffsetToken = ""
			return false, nil
		}

		var tokenStr string
		switch v := tokenVal.(type) {
		case string:
			tokenStr = v
		case float64:
			tokenStr = fmt.Sprintf("%v", v)
		case int:
			tokenStr = fmt.Sprintf("%d", v)
		default:
			tokenStr = fmt.Sprintf("%v", v)
		}

		if tokenStr == "" {
			state.CurrentOffsetToken = ""
			return false, nil
		}

		state.CurrentOffsetToken = tokenStr
		state.PagesFetched++

		// The token is a bookmark for resuming — always save it.
		// But hasMore is determined by data count: a partial/empty page means
		// we're caught up, even though the API returned a valid token.
		dataCount := len(extractedData)
		return dataCount >= state.Limit, nil
	}

	// Try to extract total record count if configured
	if cfg.Pagination.TotalRecordCountField != "" {
		if responseMap, ok := response.(map[string]any); ok {
			if totalVal, exists := responseMap[cfg.Pagination.TotalRecordCountField]; exists {
				if total, ok := toInt(totalVal); ok {
					state.TotalRecords = total
				}
			}
		}
	}

	// Determine if there are more records
	// If we have total records, compare current offset + actual items returned to total
	if state.TotalRecords > 0 {
		dataCount := len(extractedData)
		itemsProcessed := state.CurrentOffset + dataCount
		hasMore := itemsProcessed < state.TotalRecords
		return hasMore, nil
	}

	// If no total records field, check if we got a full page
	// This is a heuristic: if we got exactly 'limit' items, assume there might be more
	dataCount := len(extractedData)
	if dataCount >= state.Limit {
		return true, nil // Full page, assume more
	}

	return false, nil // Partial page, no more
}

// parsePageSizeResponse parses the response for page/size pagination.
func parsePageSizeResponse(cfg *Config, response any, extractedData []map[string]any, state *paginationState) (bool, error) {
	// Try to extract total pages if configured
	if cfg.Pagination.PageSize.TotalPagesFieldName != "" {
		if responseMap, ok := response.(map[string]any); ok {
			if totalPagesVal, exists := responseMap[cfg.Pagination.PageSize.TotalPagesFieldName]; exists {
				if totalPages, ok := toInt(totalPagesVal); ok {
					state.TotalPages = totalPages
				}
			}
		}
	}

	// Determine if there are more pages
	// If we have total pages, compare current page to total
	if state.TotalPages > 0 {
		hasMore := state.CurrentPage < state.TotalPages
		return hasMore, nil
	}

	// If no total pages field, check if we got a full page
	// This is a heuristic: if we got exactly 'pageSize' items, assume there might be more
	dataCount := len(extractedData)
	if dataCount >= state.PageSize {
		return true, nil // Full page, assume more
	}

	return false, nil // Partial page, no more
}

var (
	// datetimeRegex matches ISO 8601 / RFC 3339 style timestamps and captures:
	//   1: separator (T or space)
	//   2: fractional seconds including dot (e.g. ".123456")
	//   3: timezone (Z, ±HH:MM, ±HHMM, or ±HH)
	datetimeRegex = regexp.MustCompile(
		`^\d{4}-\d{2}-\d{2}([T ])\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}(?::?\d{2})?)?$`,
	)
	dateOnlyRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// parseTimestampResponse parses the response for timestamp-based pagination.
// The dataArray parameter contains the already-extracted data from extractDataFromResponse.
func parseTimestampResponse(cfg *Config, dataArray []map[string]any, state *paginationState, logger *zap.Logger) (bool, error) {
	// If no data, no more pages
	if len(dataArray) == 0 {
		logger.Debug("parseTimestampResponse: no data in response, no more pages")
		return false, nil
	}

	logger.Debug("parseTimestampResponse: processing response",
		zap.Int("data_count", len(dataArray)),
		zap.Int("page_size", state.PageSize),
		zap.Time("current_state_timestamp", state.CurrentTimestamp))

	// Find the maximum timestamp across ALL items in the response.
	// This is critical because APIs may return data in any order (often descending/newest first).
	// We need to track the newest timestamp seen to avoid re-fetching the same data.
	var maxTimestamp time.Time
	timestampField := cfg.Pagination.Timestamp.TimestampFieldName

	if timestampField != "" {
		configuredFormat := cfg.Pagination.Timestamp.TimestampFormat
		for i, item := range dataArray {
			if timestampVal, exists := item[timestampField]; exists {
				parsedTime := parseTimestampValue(timestampVal, configuredFormat)
				if !parsedTime.IsZero() && parsedTime.After(maxTimestamp) {
					maxTimestamp = parsedTime
					logger.Debug("parseTimestampResponse: found newer timestamp",
						zap.Int("item_index", i),
						zap.Time("timestamp", parsedTime))
				}
			}
		}

		logger.Debug("parseTimestampResponse: scanned all items for max timestamp",
			zap.Int("item_count", len(dataArray)),
			zap.Time("max_timestamp_found", maxTimestamp),
			zap.Time("previous_timestamp", state.CurrentTimestamp))
	}

	// If we got fewer items than pageSize, definitely no more pages
	if len(dataArray) < state.PageSize {
		logger.Debug("parseTimestampResponse: partial page received, no more pages",
			zap.Int("received", len(dataArray)),
			zap.Int("page_size", state.PageSize),
			zap.Time("max_timestamp", maxTimestamp),
			zap.Time("old_timestamp", state.CurrentTimestamp))
		if !maxTimestamp.IsZero() && maxTimestamp.After(state.CurrentTimestamp) {
			state.CurrentTimestamp = maxTimestamp
			state.TimestampFromData = true // Mark that timestamp came from response data
		}
		return false, nil
	}

	// If we got exactly pageSize items, there might be more
	// However, only continue if we successfully extracted a timestamp
	if !maxTimestamp.IsZero() {
		logger.Debug("parseTimestampResponse: full page received, more pages likely",
			zap.Int("received", len(dataArray)),
			zap.Time("max_timestamp", maxTimestamp),
			zap.Time("old_timestamp", state.CurrentTimestamp))
		if maxTimestamp.After(state.CurrentTimestamp) {
			state.CurrentTimestamp = maxTimestamp
			state.TimestampFromData = true // Mark that timestamp came from response data
		}
		return true, nil
	}

	// Got a full page but couldn't extract timestamp
	// This is unusual - could indicate data structure issue
	// To be safe and avoid infinite loops, we'll stop here
	logger.Debug("parseTimestampResponse: full page but no timestamp extracted, stopping")
	return false, fmt.Errorf("received full page (%d items) but failed to extract timestamp from any item", len(dataArray))
}

// parseTimestampValue parses a timestamp value from various formats.
// If configuredFormat is non-empty, it is tried first (for string values) or used
// to select the correct epoch interpretation (for numeric values).
func parseTimestampValue(timestampVal any, configuredFormat string) time.Time {
	var parsedTime time.Time

	if timestampStr, ok := timestampVal.(string); ok {
		// If the user configured an epoch format, try parsing the string as a numeric epoch value first.
		if isEpochFormat(configuredFormat) {
			if t, err := parseEpochTimestamp(timestampStr, configuredFormat); err == nil {
				return t
			}
		}
		// Try the user's configured format first, then fall back to common formats.
		if configuredFormat != "" && !isEpochFormat(configuredFormat) {
			if t, err := time.Parse(configuredFormat, timestampStr); err == nil {
				return t
			}
		}
		// Detect format via regex and parse
		if t, ok := parseTimestampString(timestampStr); ok {
			parsedTime = t
		}
	} else if timestampFloat, ok := timestampVal.(float64); ok {
		// Unix timestamp (seconds or milliseconds)
		if timestampFloat > 1e10 {
			// Likely milliseconds
			ms := int64(timestampFloat)
			fracNs := int64((timestampFloat - float64(ms)) * 1e6)
			parsedTime = time.Unix(0, ms*int64(time.Millisecond)+fracNs)
		} else {
			// Likely seconds (preserve fractional part as nanoseconds)
			sec := int64(timestampFloat)
			fracNs := int64((timestampFloat - float64(sec)) * 1e9)
			parsedTime = time.Unix(sec, fracNs)
		}
	} else if timestampInt, ok := timestampVal.(int64); ok {
		if timestampInt > 1e10 {
			// Likely milliseconds
			parsedTime = time.Unix(0, timestampInt*1e6)
		} else {
			// Likely seconds
			parsedTime = time.Unix(timestampInt, 0)
		}
	}

	return parsedTime
}

// parseTimestampString attempts to parse a timestamp string by detecting its
// format via regex rather than iterating a fixed list of formats. This handles
// any combination of T/space separator, fractional-second precision (1-9 digits),
// and timezone style (Z, ±HH:MM, ±HHMM, ±HH, or absent).
func parseTimestampString(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)

	if m := datetimeRegex.FindStringSubmatch(s); m != nil {
		format := "2006-01-02" + m[1] + "15:04:05"

		if m[2] != "" {
			// Build fractional format matching the exact digit count.
			format += "." + strings.Repeat("0", len(m[2])-1)
		}

		if tz := m[3]; tz != "" {
			switch {
			case tz == "Z":
				format += "Z07:00" // accepts literal Z
			case strings.Contains(tz, ":"):
				format += "-07:00" // ±HH:MM
			case len(tz) == 5:
				format += "-0700" // ±HHMM
			case len(tz) == 3:
				format += "-07" // ±HH
			}
		}

		if t, err := time.Parse(format, s); err == nil {
			return t, true
		}
	}

	if dateOnlyRegex.MatchString(s) {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

// updatePaginationState updates the pagination state to the next page/offset.
func updatePaginationState(cfg *Config, state *paginationState) {
	switch cfg.Pagination.Mode {
	case paginationModeOffsetLimit:
		state.CurrentOffset += state.Limit
		state.PagesFetched++

	case paginationModePageSize:
		state.CurrentPage++
		state.PagesFetched++

	case paginationModeTimestamp:
		// Timestamp is updated in parseTimestampResponse
		state.PagesFetched++
	}
}

// checkPageLimit checks if the page limit has been reached.
func checkPageLimit(cfg *Config, state *paginationState) bool {
	if cfg.Pagination.PageLimit == 0 {
		return true // No limit
	}

	return state.PagesFetched < cfg.Pagination.PageLimit
}
