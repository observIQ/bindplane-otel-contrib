package ocsf

import (
	"testing"
	"time"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/matcher"
)

func TestEventToObservedBundle_SingleObservable(t *testing.T) {
	b := NewBuilder()
	ev := map[string]interface{}{
		"metadata": map[string]interface{}{
			"time": "2021-01-01T12:00:00Z",
		},
		"observables": []interface{}{
			map[string]interface{}{
				"name":    "file.name",
				"type_id": 7,
				"type":    "File Name",
				"value":   "/tmp/foo",
			},
		},
	}
	bundle, err := b.EventToObservedBundle(ev)
	if err != nil {
		t.Fatal(err)
	}
	if typ, _ := bundle["type"].(string); typ != "bundle" {
		t.Errorf("bundle type = %q, want bundle", typ)
	}
	objs, _ := bundle["objects"].([]interface{})
	if len(objs) < 2 {
		t.Fatalf("expected at least 2 objects (observed-data + file SCO), got %d", len(objs))
	}
	var observed map[string]interface{}
	var fileSCO map[string]interface{}
	for _, o := range objs {
		m, _ := o.(map[string]interface{})
		if m == nil {
			continue
		}
		switch m["type"] {
		case "observed-data":
			observed = m
		case "file":
			fileSCO = m
		}
	}
	if observed == nil {
		t.Fatal("no observed-data in bundle")
	}
	if first, _ := observed["first_observed"].(string); first != "2021-01-01T12:00:00Z" {
		t.Errorf("first_observed = %q", first)
	}
	if last, _ := observed["last_observed"].(string); last != "2021-01-01T12:00:00Z" {
		t.Errorf("last_observed = %q", last)
	}
	if num := observed["number_observed"]; num != float64(1) && num != 1 {
		t.Errorf("number_observed = %v", num)
	}
	refs, _ := observed["object_refs"].([]interface{})
	if len(refs) != 1 {
		t.Errorf("object_refs length = %d, want 1", len(refs))
	}
	if fileSCO == nil {
		t.Fatal("no file SCO in bundle")
	}
	if name, _ := fileSCO["name"].(string); name != "/tmp/foo" {
		t.Errorf("file name = %q, want /tmp/foo", name)
	}
	if refs != nil && len(refs) > 0 {
		wantID, _ := fileSCO["id"].(string)
		if refs[0] != wantID {
			t.Errorf("object_refs[0] = %v, want %q", refs[0], wantID)
		}
	}
}

func TestEventToObservedBundle_DefaultArtifact(t *testing.T) {
	b := NewBuilder()
	b.Now = func() time.Time { return time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC) }
	n := 0
	b.NewID = func(typ string) string {
		n++
		return typ + "--test-id-" + string(rune('0'+n))
	}
	ev := map[string]interface{}{
		"observables": []interface{}{
			map[string]interface{}{
				"name":    "vendor.custom.field",
				"type_id": 99,
				"type":    "Other",
				"value":   "some-value",
			},
		},
	}
	bundle, err := b.EventToObservedBundle(ev)
	if err != nil {
		t.Fatal(err)
	}
	objs, _ := bundle["objects"].([]interface{})
	var artifact map[string]interface{}
	for _, o := range objs {
		m, _ := o.(map[string]interface{})
		if m != nil && m["type"] == "artifact" {
			artifact = m
			break
		}
	}
	if artifact == nil {
		t.Fatal("expected artifact SCO for unmapped observable")
	}
	ext, _ := artifact["extensions"].(map[string]interface{})
	if ext == nil {
		t.Fatal("artifact has no extensions")
	}
	ocsf, _ := ext["ocsf"].(map[string]interface{})
	if ocsf == nil {
		t.Fatal("artifact has no extensions.ocsf")
	}
	if v, _ := ocsf["name"].(string); v != "vendor.custom.field" {
		t.Errorf("extensions.ocsf.name = %q", v)
	}
	if v, _ := ocsf["type"].(string); v != "Other" {
		t.Errorf("extensions.ocsf.type = %q", v)
	}
	if v, _ := ocsf["type_id"].(float64); int(v) != 99 {
		if vi, _ := ocsf["type_id"].(int); vi != 99 {
			t.Errorf("extensions.ocsf.type_id = %v", ocsf["type_id"])
		}
	}
	if v := ocsf["value"]; v != "some-value" {
		t.Errorf("extensions.ocsf.value = %v", v)
	}
	observed, _ := bundle["objects"].([]interface{})
	var od map[string]interface{}
	for _, o := range observed {
		m, _ := o.(map[string]interface{})
		if m != nil && m["type"] == "observed-data" {
			od = m
			break
		}
	}
	if od != nil {
		refs, _ := od["object_refs"].([]interface{})
		if len(refs) != 1 {
			t.Errorf("object_refs length = %d, want 1", len(refs))
		}
		aid, _ := artifact["id"].(string)
		if len(refs) > 0 && refs[0] != aid {
			t.Errorf("object_refs[0] = %v, want %q", refs[0], aid)
		}
	}
}

func TestEventToObservedBundle_TimestampFallback(t *testing.T) {
	fixed := time.Date(2023, 1, 15, 10, 30, 0, 0, time.UTC)
	b := NewBuilder()
	b.Now = func() time.Time { return fixed }
	b.NewID = func(typ string) string { return typ + "--fixed" }
	ev := map[string]interface{}{
		"observables": []interface{}{
			map[string]interface{}{"name": "file.name", "type_id": 7, "value": "x"},
		},
	}
	bundle, err := b.EventToObservedBundle(ev)
	if err != nil {
		t.Fatal(err)
	}
	var od map[string]interface{}
	for _, o := range bundle["objects"].([]interface{}) {
		m, _ := o.(map[string]interface{})
		if m != nil && m["type"] == "observed-data" {
			od = m
			break
		}
	}
	if od == nil {
		t.Fatal("no observed-data")
	}
	first, _ := od["first_observed"].(string)
	want := fixed.UTC().Format(time.RFC3339Nano)
	if first != want {
		t.Errorf("first_observed = %q, want %q", first, want)
	}
}

func TestEventToObservedBundle_MixedKnownAndUnknown(t *testing.T) {
	b := NewBuilder()
	b.NewID = func(typ string) string { return typ + "--id-1" }
	ev := map[string]interface{}{
		"metadata": map[string]interface{}{"time": "2021-06-01T00:00:00Z"},
		"observables": []interface{}{
			map[string]interface{}{"name": "file.name", "type_id": 7, "value": "/known"},
			map[string]interface{}{"name": "unknown.thing", "type_id": 0, "value": "raw"},
		},
	}
	bundle, err := b.EventToObservedBundle(ev)
	if err != nil {
		t.Fatal(err)
	}
	objs, _ := bundle["objects"].([]interface{})
	var fileSCO, artifactSCO map[string]interface{}
	for _, o := range objs {
		m, _ := o.(map[string]interface{})
		if m == nil {
			continue
		}
		switch m["type"] {
		case "file":
			fileSCO = m
		case "artifact":
			artifactSCO = m
		}
	}
	if fileSCO == nil || fileSCO["name"] != "/known" {
		t.Errorf("expected file SCO with name /known, got %v", fileSCO)
	}
	if artifactSCO == nil {
		t.Error("expected artifact SCO for unknown.thing")
	} else if ext, _ := artifactSCO["extensions"].(map[string]interface{}); ext != nil {
		if ocsf, _ := ext["ocsf"].(map[string]interface{}); ocsf != nil && ocsf["value"] != "raw" {
			t.Errorf("artifact value = %v", ocsf["value"])
		}
	}
	var od map[string]interface{}
	for _, o := range objs {
		m, _ := o.(map[string]interface{})
		if m != nil && m["type"] == "observed-data" {
			od = m
			break
		}
	}
	if od == nil {
		t.Fatal("no observed-data")
	}
	refs, _ := od["object_refs"].([]interface{})
	if len(refs) != 2 {
		t.Errorf("object_refs length = %d, want 2", len(refs))
	}
}

func TestEventsToObservedData(t *testing.T) {
	b := NewBuilder()
	b.NewID = func(typ string) string { return typ + "--single" }
	events := []map[string]interface{}{
		{"metadata": map[string]interface{}{"time": "2021-01-01T00:00:00Z"}, "observables": []interface{}{map[string]interface{}{"name": "file.name", "value": "a"}}},
		{"metadata": map[string]interface{}{"time": "2021-01-02T00:00:00Z"}, "observables": []interface{}{map[string]interface{}{"name": "process.name", "value": "bash"}}},
	}
	out, err := b.EventsToObservedData(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	for i, bundle := range out {
		if typ, _ := bundle["type"].(string); typ != "bundle" {
			t.Errorf("bundle %d type = %q", i, typ)
		}
	}
}

func TestMatcherIntegration(t *testing.T) {
	b := NewBuilder()
	events := []map[string]interface{}{
		{
			"metadata":    map[string]interface{}{"time": "2021-01-01T10:00:00Z"},
			"observables": []interface{}{map[string]interface{}{"name": "file.name", "type_id": 7, "value": "/tmp/a"}},
		},
		{
			"metadata":    map[string]interface{}{"time": "2021-01-01T10:01:00Z"},
			"observables": []interface{}{map[string]interface{}{"name": "process.name", "type_id": 9, "value": "bash"}},
		},
	}
	obs, err := b.EventsToObservedData(events)
	if err != nil {
		t.Fatal(err)
	}
	res, err := matcher.Match("[file:name = '/tmp/a']", obs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Matched {
		t.Error("expected pattern [file:name = '/tmp/a'] to match")
	}
	res2, err := matcher.Match("([file:name = '/tmp/a'] FOLLOWEDBY [process:name = 'bash'])", obs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Matched {
		t.Error("expected FOLLOWEDBY pattern to match")
	}
}

func TestApplyProperty(t *testing.T) {
	sco := map[string]interface{}{"type": "file", "id": "file--1"}
	applyProperty(sco, "name", "x")
	if sco["name"] != "x" {
		t.Errorf("name = %v", sco["name"])
	}
	applyProperty(sco, "hashes.MD5", "abc")
	hashes, _ := sco["hashes"].(map[string]interface{})
	if hashes == nil || hashes["MD5"] != "abc" {
		t.Errorf("hashes = %v", sco["hashes"])
	}
}

func TestObservableValueString(t *testing.T) {
	tests := []struct {
		v    interface{}
		want string
	}{
		{"x", "x"},
		{nil, ""},
		{42, "42"},
		{3.14, "3.14"},
		{true, "true"},
	}
	for _, tt := range tests {
		if got := ObservableValueString(tt.v); got != tt.want {
			t.Errorf("ObservableValueString(%v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

func TestByObservableTypeID_TypeNameFallback(t *testing.T) {
	// Observable with no name but type_id 2 (IP Address) should map via type_id
	m, ok := ByObservableTypeID(2)
	if !ok || m.Type != "ipv4-addr" {
		t.Errorf("ByObservableTypeID(2) = %+v, %v", m, ok)
	}
	m2, ok2 := ByObservableTypeName("IP Address")
	if !ok2 || m2.Type != "ipv4-addr" {
		t.Errorf("ByObservableTypeName(\"IP Address\") = %+v, %v", m2, ok2)
	}
}

// Test that empty observables still produce valid observed-data (with empty object_refs).
func TestEventToObservedBundle_NoObservables(t *testing.T) {
	b := NewBuilder()
	b.NewID = func(typ string) string { return typ + "--id" }
	ev := map[string]interface{}{
		"metadata":    map[string]interface{}{"time": "2021-01-01T00:00:00Z"},
		"observables": []interface{}{},
	}
	bundle, err := b.EventToObservedBundle(ev)
	if err != nil {
		t.Fatal(err)
	}
	objs, _ := bundle["objects"].([]interface{})
	if len(objs) != 1 {
		t.Fatalf("expected 1 object (observed-data only), got %d", len(objs))
	}
	od, _ := objs[0].(map[string]interface{})
	if od["type"] != "observed-data" {
		t.Errorf("type = %v", od["type"])
	}
	refs, _ := od["object_refs"].([]interface{})
	if refs == nil {
		t.Errorf("object_refs should be non-nil (use empty slice), got nil")
	} else if len(refs) != 0 {
		t.Errorf("object_refs = %v, want empty", refs)
	}
}

func TestDefaultMapping(t *testing.T) {
	if DefaultMapping.Type != "artifact" || DefaultMapping.Property != "extensions.ocsf.value" {
		t.Errorf("DefaultMapping = %+v", DefaultMapping)
	}
}

func TestDefaultNewIDFormat(t *testing.T) {
	id := defaultNewID("file")
	if len(id) < 40 || id[:6] != "file--" {
		t.Errorf("defaultNewID(\"file\") = %q", id)
	}
}

func TestEventToObservedBundle_TypeIDFallback(t *testing.T) {
	// Observable with name that is not in field map but type_id 6 (URL) should map via type_id
	b := NewBuilder()
	b.NewID = func(typ string) string { return typ + "--x" }
	ev := map[string]interface{}{
		"metadata": map[string]interface{}{"time": "2021-01-01T00:00:00Z"},
		"observables": []interface{}{
			map[string]interface{}{
				"name":    "custom.url_field",
				"type_id": 6,
				"type":    "URL String",
				"value":   "https://example.com",
			},
		},
	}
	// ByFieldName("custom.url_field") returns DefaultMapping (artifact) because it's not in curated.
	// So we get artifact. The plan says: try ByFieldName first, then ByObservableTypeID, then ByObservableTypeName.
	// So for "custom.url_field" we get (DefaultMapping, true) from ByFieldName - we don't try type_id because we already "found" something.
	// So actually the lookup order is: 1) ByFieldName - if name non-empty, we always get (m, true) now (either specific or default).
	// So we never fall through to type_id/type_name when name is set. That's consistent with "if no mapping, use default" - we're treating "default" as a mapping. So observable with name "custom.url_field" becomes artifact. If we had no name but type_id 6, we'd get url from ByObservableTypeID. So the test "TypeIDFallback" should be: observable with empty name, type_id 6, type "URL String", value "https://..." -> should produce url SCO. Let me add that test.
	ev2 := map[string]interface{}{
		"metadata": map[string]interface{}{"time": "2021-01-01T00:00:00Z"},
		"observables": []interface{}{
			map[string]interface{}{
				"name":    "",
				"type_id": 6,
				"type":    "URL String",
				"value":   "https://example.com",
			},
		},
	}
	bundle, err := b.EventToObservedBundle(ev2)
	if err != nil {
		t.Fatal(err)
	}
	var urlSCO map[string]interface{}
	for _, o := range bundle["objects"].([]interface{}) {
		m, _ := o.(map[string]interface{})
		if m != nil && m["type"] == "url" {
			urlSCO = m
			break
		}
	}
	if urlSCO == nil {
		t.Fatal("expected url SCO when name empty and type_id 6")
	}
	if urlSCO["value"] != "https://example.com" {
		t.Errorf("url value = %v", urlSCO["value"])
	}
	// Run first event too - it should produce artifact for custom.url_field (name takes precedence)
	bundle1, _ := b.EventToObservedBundle(ev)
	var artifactSCO map[string]interface{}
	for _, o := range bundle1["objects"].([]interface{}) {
		m, _ := o.(map[string]interface{})
		if m != nil && m["type"] == "artifact" {
			artifactSCO = m
			break
		}
	}
	if artifactSCO == nil {
		t.Fatal("expected artifact for custom.url_field (name present, not in map -> default)")
	}
}
