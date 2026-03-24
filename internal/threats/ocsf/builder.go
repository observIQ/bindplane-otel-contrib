package ocsf

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Builder converts OCSF events into STIX 2.1 observed-data bundles.
type Builder struct {
	Now   func() time.Time
	NewID func(typ string) string
}

// NewBuilder returns a Builder with default time and ID generation.
func NewBuilder() *Builder {
	return &Builder{
		Now:   time.Now,
		NewID: defaultNewID,
	}
}

func defaultNewID(typ string) string {
	return typ + "--" + uuid.New().String()
}

// eventTime returns a canonical event time string (RFC3339Nano). Prefers metadata.time.
func (b *Builder) eventTime(ev map[string]interface{}) (string, error) {
	if meta, _ := ev["metadata"].(map[string]interface{}); meta != nil {
		if t, _ := meta["time"].(string); t != "" {
			if _, err := time.Parse(time.RFC3339Nano, t); err == nil {
				return t, nil
			}
			if _, err := time.Parse(time.RFC3339, t); err == nil {
				return t, nil
			}
		}
	}
	for _, key := range []string{"event_time", "activity_start_time", "time"} {
		if v, _ := ev[key].(string); v != "" {
			if _, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return v, nil
			}
			if _, err := time.Parse(time.RFC3339, v); err == nil {
				return v, nil
			}
		}
	}
	return b.Now().UTC().Format(time.RFC3339Nano), nil
}

// EventToObservedBundle converts a single OCSF event into a STIX 2.1 bundle containing
// one observed-data SDO and SCOs for each observable (typed or default artifact).
func (b *Builder) EventToObservedBundle(ev map[string]interface{}) (map[string]interface{}, error) {
	first, err := b.eventTime(ev)
	if err != nil {
		return nil, err
	}
	last := first

	observablesRaw, _ := ev["observables"].([]interface{})
	var scos []map[string]interface{}
	objectRefs := make([]interface{}, 0)

	for _, oi := range observablesRaw {
		o, _ := oi.(map[string]interface{})
		if o == nil {
			continue
		}
		name, _ := o["name"].(string)
		typeName, _ := o["type"].(string)
		var typeID int
		switch v := o["type_id"].(type) {
		case float64:
			typeID = int(v)
		case int:
			typeID = v
		}
		value := o["value"]

		var m STIXMapping
		found := false
		if name != "" {
			m, found = ByFieldName(name)
		}
		if !found && typeID != 0 {
			m, found = ByObservableTypeID(typeID)
		}
		if !found && typeName != "" {
			m, found = ByObservableTypeName(typeName)
		}
		if !found {
			m = DefaultMapping
		}

		sco := b.buildSCO(m, value, name, typeName, typeID)
		if sco == nil {
			continue
		}
		id, _ := sco["id"].(string)
		scos = append(scos, sco)
		objectRefs = append(objectRefs, id)
	}

	observed := map[string]interface{}{
		"type":            "observed-data",
		"id":              b.NewID("observed-data"),
		"spec_version":    "2.1",
		"first_observed":  first,
		"last_observed":   last,
		"number_observed": 1,
		"object_refs":     objectRefs,
	}

	objects := make([]interface{}, 0, 1+len(scos))
	objects = append(objects, observed)
	for _, sco := range scos {
		objects = append(objects, sco)
	}

	bundle := map[string]interface{}{
		"type":    "bundle",
		"id":      b.NewID("bundle"),
		"objects": objects,
	}
	return bundle, nil
}

// buildSCO creates one SCO map. For DefaultMapping (artifact) it stores value and OCSF metadata in extensions.ocsf.
func (b *Builder) buildSCO(m STIXMapping, value interface{}, ocsfName, ocsfType string, ocsfTypeID int) map[string]interface{} {
	id := b.NewID(m.Type)
	sco := map[string]interface{}{
		"type": m.Type,
		"id":   id,
	}

	if m.Type == "artifact" && m.Property == "extensions.ocsf.value" {
		ext := map[string]interface{}{
			"value": value,
		}
		if ocsfName != "" {
			ext["name"] = ocsfName
		}
		if ocsfType != "" {
			ext["type"] = ocsfType
		}
		ext["type_id"] = ocsfTypeID
		sco["extensions"] = map[string]interface{}{
			"ocsf": ext,
		}
		return sco
	}

	applyProperty(sco, m.Property, value)
	return sco
}

// applyProperty sets a property on the SCO, including nested paths like "hashes.MD5" or "extensions.xxx.yyy".
func applyProperty(sco map[string]interface{}, prop string, value interface{}) {
	parts := strings.Split(prop, ".")
	if len(parts) == 1 {
		sco[prop] = value
		return
	}
	// Nested: ensure parent map exists
	cur := sco
	for i := 0; i < len(parts)-1; i++ {
		key := parts[i]
		next, _ := cur[key].(map[string]interface{})
		if next == nil {
			next = make(map[string]interface{})
			cur[key] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}

// EventsToObservedData converts a slice of OCSF events into a slice of STIX bundles.
func (b *Builder) EventsToObservedData(events []map[string]interface{}) ([]map[string]interface{}, error) {
	out := make([]map[string]interface{}, 0, len(events))
	for _, ev := range events {
		bundle, err := b.EventToObservedBundle(ev)
		if err != nil {
			return nil, err
		}
		out = append(out, bundle)
	}
	return out, nil
}

// ObservableValueString returns a string for use in SCO properties when the observable value is mixed type.
func ObservableValueString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(v)
	}
}
