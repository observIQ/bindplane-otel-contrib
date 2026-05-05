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

package azureloganalyticsexporter

import (
	"go.opentelemetry.io/collector/pdata/plog"
)

// groupKey uniquely identifies a batch destination (DCR ingestion). The
// Endpoint is included in the key to reserve support for per-record endpoint
// routing in a future revision; for now all groups share the configured
// endpoint (see groupLogs).
//
// TODO(routing): support per-record sentinel_endpoint attribute via a per-
// endpoint client pool, then populate Endpoint from attribute precedence.
type groupKey struct {
	Endpoint   string
	RuleID     string
	StreamName string
}

// String returns the composite key string "endpoint|ruleID|streamName".
func (k groupKey) String() string {
	return k.Endpoint + "|" + k.RuleID + "|" + k.StreamName
}

// groupLogs splits an incoming plog.Logs batch into one batch per unique
// (endpoint, ruleID, streamName) combination, as resolved from per-record
// attributes with fallback to the exporter configuration.
//
// The Resource -> Scope -> LogRecord hierarchy is preserved inside each output
// batch: a given resource/scope may be duplicated across output batches if its
// records route to different destinations, but each record keeps its original
// resource and scope metadata.
//
// Backward compatibility: if no log records carry routing attributes, the
// result is a single group keyed by (cfg.Endpoint, cfg.RuleID, cfg.StreamName)
// equivalent to the original pre-grouping behavior.
func groupLogs(ld plog.Logs, cfg *Config) map[groupKey]plog.Logs {
	groups := make(map[groupKey]plog.Logs)

	// Per-group caches: map the original resource/scope pointer to the
	// corresponding ResourceLogs/ScopeLogs inside that group's output batch so
	// we only create each once per group. The *index* is used as the map key
	// rather than the struct value because plog types do not support pointer
	// comparison directly.
	type scopeEntry struct {
		// origScopeIdx is the index of the scope within its resource.
		origScopeIdx int
		dst          plog.ScopeLogs
	}
	type resourceEntry struct {
		dst    plog.ResourceLogs
		scopes map[int]*scopeEntry
	}

	// Per-group state keyed by (groupKey, origResourceIdx) => resourceEntry.
	groupResources := make(map[groupKey]map[int]*resourceEntry)

	marshaler := &azureLogAnalyticsMarshaler{cfg: cfg}

	resourceLogs := ld.ResourceLogs()
	for i := 0; i < resourceLogs.Len(); i++ {
		rl := resourceLogs.At(i)
		scopes := rl.ScopeLogs()
		for j := 0; j < scopes.Len(); j++ {
			sl := scopes.At(j)
			records := sl.LogRecords()
			for k := 0; k < records.Len(); k++ {
				lr := records.At(k)

				key := groupKey{
					Endpoint:   cfg.Endpoint,
					RuleID:     marshaler.getRuleID(lr, sl, rl),
					StreamName: marshaler.getStreamName(lr, sl, rl),
				}

				out, ok := groups[key]
				if !ok {
					out = plog.NewLogs()
					groups[key] = out
					groupResources[key] = make(map[int]*resourceEntry)
				}

				resMap := groupResources[key]
				resEntry, ok := resMap[i]
				if !ok {
					dstRL := out.ResourceLogs().AppendEmpty()
					rl.Resource().Attributes().CopyTo(dstRL.Resource().Attributes())
					dstRL.Resource().SetDroppedAttributesCount(rl.Resource().DroppedAttributesCount())
					dstRL.SetSchemaUrl(rl.SchemaUrl())
					resEntry = &resourceEntry{
						dst:    dstRL,
						scopes: make(map[int]*scopeEntry),
					}
					resMap[i] = resEntry
				}

				scEntry, ok := resEntry.scopes[j]
				if !ok {
					dstSL := resEntry.dst.ScopeLogs().AppendEmpty()
					sl.Scope().CopyTo(dstSL.Scope())
					dstSL.SetSchemaUrl(sl.SchemaUrl())
					scEntry = &scopeEntry{origScopeIdx: j, dst: dstSL}
					resEntry.scopes[j] = scEntry
				}

				lr.CopyTo(scEntry.dst.LogRecords().AppendEmpty())
			}
		}
	}

	// If the incoming batch was completely empty we still return a single
	// group keyed by the configured values so callers have a consistent shape.
	if len(groups) == 0 {
		key := groupKey{
			Endpoint:   cfg.Endpoint,
			RuleID:     cfg.RuleID,
			StreamName: cfg.StreamName,
		}
		groups[key] = plog.NewLogs()
	}

	return groups
}
