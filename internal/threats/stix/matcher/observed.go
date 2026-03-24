package matcher

import (
	"fmt"
)

// observation is the internal representation of one observed-data "bundle" after 2.1 expansion.
// objectsMap: id -> object (for 2.1 list-of-objects with id).
// timeInterval: first_observed, last_observed for WITHIN/START STOP.
// numberObserved: number_observed (>= 1).
type observation struct {
	objectsMap     map[string]map[string]interface{} // id -> object
	timeInterval   [2]string                         // first_observed, last_observed
	numberObserved int
	sdo            map[string]interface{} // the observed-data SDO for this observation
}

// normalizeObservedDataSTIX21 takes a list of observed-data SDOs or bundles and returns
// a list of observations (one per observed-data SDO, with object_refs expanded).
// Each input item can be a bundle (type "bundle", has "objects") or a single observed-data SDO.
func normalizeObservedDataSTIX21(input []map[string]interface{}) ([]*observation, error) {
	// Merge all bundles into one: collect all "objects" from bundles and single SDOs.
	mergedObjects := make([]map[string]interface{}, 0)
	for _, item := range input {
		if item == nil {
			continue
		}
		typ, _ := item["type"].(string)
		if typ == "bundle" {
			objs, _ := item["objects"].([]interface{})
			for _, o := range objs {
				if m, ok := o.(map[string]interface{}); ok {
					mergedObjects = append(mergedObjects, m)
				}
			}
		} else if typ == "observed-data" {
			mergedObjects = append(mergedObjects, item)
		}
	}

	// Build id -> object for non-observed-data (SCOs, etc.)
	scoMap := make(map[string]map[string]interface{})
	var observedDataList []map[string]interface{}
	for _, obj := range mergedObjects {
		id, _ := obj["id"].(string)
		typ, _ := obj["type"].(string)
		if typ == "observed-data" {
			observedDataList = append(observedDataList, obj)
		} else if id != "" {
			scoMap[id] = obj
		}
	}

	if len(observedDataList) == 0 {
		return nil, nil
	}

	out := make([]*observation, 0, len(observedDataList))
	for _, od := range observedDataList {
		obs, err := buildObservation(od, scoMap)
		if err != nil {
			return nil, err
		}
		out = append(out, obs)
	}
	return out, nil
}

func buildObservation(observedData map[string]interface{}, scoMap map[string]map[string]interface{}) (*observation, error) {
	refs, _ := observedData["object_refs"].([]interface{})
	if refs == nil {
		return nil, fmt.Errorf("STIX v2.1 observed-data must have object_refs")
	}
	first, _ := observedData["first_observed"].(string)
	last, _ := observedData["last_observed"].(string)
	num, _ := observedData["number_observed"]
	var numObs int
	switch n := num.(type) {
	case float64:
		numObs = int(n)
	case int:
		numObs = n
	default:
		return nil, fmt.Errorf("observed-data number_observed must be a number")
	}
	if numObs < 1 {
		return nil, fmt.Errorf("observed-data number_observed must be >= 1")
	}

	// objects list: [observedData, ...referenced SCOs]
	objectsList := []map[string]interface{}{observedData}
	for _, ref := range refs {
		refID, ok := ref.(string)
		if !ok {
			continue
		}
		if obj, ok := scoMap[refID]; ok {
			objectsList = append(objectsList, obj)
		}
	}

	// id -> object for 2.1
	objectsMap := make(map[string]map[string]interface{})
	for _, obj := range objectsList {
		id, _ := obj["id"].(string)
		if id != "" {
			objectsMap[id] = obj
		}
	}

	return &observation{
		objectsMap:     objectsMap,
		timeInterval:   [2]string{first, last},
		numberObserved: numObs,
		sdo:            observedData,
	}, nil
}
