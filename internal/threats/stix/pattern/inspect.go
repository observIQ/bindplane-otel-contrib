package pattern

// buildInspectionMap converts path refs from the parser into a map from
// object type to unique list of property path strings. Paths are deduped per type.
func buildInspectionMap(paths []pathRef) map[string][]string {
	if paths == nil {
		return map[string][]string{}
	}
	// map type -> set of paths (use map[string]struct{} to dedupe)
	seen := make(map[string]map[string]struct{})
	for _, pr := range paths {
		if seen[pr.ObjectType] == nil {
			seen[pr.ObjectType] = make(map[string]struct{})
		}
		seen[pr.ObjectType][pr.Path] = struct{}{}
	}
	out := make(map[string][]string, len(seen))
	for objType, set := range seen {
		list := make([]string, 0, len(set))
		for path := range set {
			list = append(list, path)
		}
		out[objType] = list
	}
	return out
}
