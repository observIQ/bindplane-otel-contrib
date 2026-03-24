package matcher

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/pattern"
)

var (
	typesV21SetCached map[string]struct{}
	typesV21SetOnce   sync.Once
)

// typesV21Set returns a set of non-SCO types (object types we do not match on in 2.1).
func typesV21Set() map[string]struct{} {
	typesV21SetOnce.Do(func() {
		typesV21SetCached = make(map[string]struct{}, len(TYPES_V21))
		for _, t := range TYPES_V21 {
			typesV21SetCached[t] = struct{}{}
		}
	})
	return typesV21SetCached
}

// resolveObjectPath returns the values at the given object path for an observation.
// objectType is the pattern object type (e.g. "file"); path is the path steps.
// Returns a map: root object id -> list of values (after stepping and deref).
// In 2.1 we only include SCO types (exclude TYPES_V21).
func (o *observation) resolveObjectPath(objectType string, path []pattern.PathStep) map[string][]interface{} {
	nonSCO := typesV21Set()
	if _, skip := nonSCO[objectType]; skip {
		return nil
	}

	// Collect objects of this type (by id)
	typeObjs := make(map[string][]interface{})
	for id, obj := range o.objectsMap {
		t, _ := obj["type"].(string)
		if t == objectType {
			typeObjs[id] = []interface{}{obj}
		}
	}
	if len(typeObjs) == 0 {
		return nil
	}

	// Step through path
	for _, step := range path {
		next := make(map[string][]interface{})
		for rootID, vals := range typeObjs {
			stepped := stepIntoObjs(vals, step, o.objectsMap)
			if len(stepped) > 0 {
				next[rootID] = stepped
			}
		}
		typeObjs = next
		if len(typeObjs) == 0 {
			break
		}
	}
	return typeObjs
}

// stepIntoObjs applies one path step to a list of values (objects or already-stepped values).
// For key step: extract key from each object, apply _hex/_bin suffix, then deref _ref/_refs.
func stepIntoObjs(objs []interface{}, step pattern.PathStep, objectsMap map[string]map[string]interface{}) []interface{} {
	var out []interface{}
	if step.Key != "" {
		for _, obj := range objs {
			m, ok := obj.(map[string]interface{})
			if !ok {
				continue
			}
			v, ok := m[step.Key]
			if !ok {
				continue
			}
			v = processPropSuffix(step.Key, v)
			out = append(out, v)
		}
		// Dereference if key ends with _ref or _refs
		out = dereferenceRefs(out, step.Key, objectsMap)
	} else if step.Index != nil {
		idx := *step.Index
		for _, obj := range objs {
			arr, ok := obj.([]interface{})
			if !ok {
				continue
			}
			if idx >= 0 && idx < len(arr) {
				out = append(out, arr[idx])
			} else if idx < 0 && -idx <= len(arr) {
				out = append(out, arr[len(arr)+idx])
			}
		}
	} else if step.Star {
		for _, obj := range objs {
			arr, ok := obj.([]interface{})
			if !ok {
				continue
			}
			for _, v := range arr {
				out = append(out, v)
			}
		}
	}
	return out
}

func processPropSuffix(propName string, value interface{}) interface{} {
	s, ok := value.(string)
	if !ok {
		return value
	}
	if strings.HasSuffix(propName, "_hex") {
		b, err := hex.DecodeString(s)
		if err != nil {
			return value
		}
		return b
	}
	if strings.HasSuffix(propName, "_bin") {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return value
		}
		return b
	}
	return value
}

func dereferenceRefs(values []interface{}, propName string, objectsMap map[string]map[string]interface{}) []interface{} {
	if !strings.HasSuffix(propName, "_ref") && !strings.HasSuffix(propName, "_refs") {
		return values
	}
	var out []interface{}
	if strings.HasSuffix(propName, "_ref") {
		for _, v := range values {
			ref, ok := v.(string)
			if !ok {
				continue
			}
			if obj, ok := objectsMap[ref]; ok {
				out = append(out, obj)
			}
		}
	} else {
		for _, v := range values {
			refs, ok := v.([]interface{})
			if !ok {
				continue
			}
			for _, r := range refs {
				ref, ok := r.(string)
				if !ok {
					continue
				}
				if obj, ok := objectsMap[ref]; ok {
					out = append(out, obj)
				}
			}
		}
	}
	return out
}
