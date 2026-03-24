package matcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadObs(t *testing.T, name string) []map[string]interface{} {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var obs []map[string]interface{}
	if err := json.Unmarshal(data, &obs); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return obs
}

func TestMatch_SimpleEqual(t *testing.T) {
	obs := loadObs(t, "basic_obs.json")
	res, err := Match("[test:int = 5]", obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !res.Matched {
		t.Error("expected match")
	}
	if len(res.SDOs) == 0 {
		t.Error("expected at least one SDO")
	}
}

func TestMatch_SimpleNotEqual(t *testing.T) {
	obs := loadObs(t, "basic_obs.json")
	res, err := Match("[test:int != 8]", obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !res.Matched {
		t.Error("expected match for != 8")
	}
}

func TestMatch_NoMatch(t *testing.T) {
	obs := loadObs(t, "basic_obs.json")
	res, err := Match("[test:int = 99]", obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if res.Matched {
		t.Error("expected no match")
	}
	if len(res.SDOs) != 0 {
		t.Error("expected no SDOs")
	}
}

func TestMatch_StringLike(t *testing.T) {
	obs := loadObs(t, "basic_obs.json")
	res, err := Match("[test:string LIKE 'he%']", obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !res.Matched {
		t.Error("expected match for LIKE 'he%'")
	}
}

func TestMatch_StringEquals(t *testing.T) {
	obs := loadObs(t, "basic_obs.json")
	res, err := Match("[test:string = 'hello']", obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !res.Matched {
		t.Error("expected match")
	}
}

func TestMatch_Order(t *testing.T) {
	obs := loadObs(t, "basic_obs.json")
	res, err := Match("[test:int > 3]", obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !res.Matched {
		t.Error("expected match for int > 3")
	}
	res, err = Match("[test:int < 2]", obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if res.Matched {
		t.Error("expected no match for int < 2")
	}
}

func TestMatch_InSet(t *testing.T) {
	obs := loadObs(t, "basic_obs.json")
	res, err := Match("[test:int IN (-4, 5, 6)]", obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !res.Matched {
		t.Error("expected match for IN (-4, 5, 6)")
	}
	res, err = Match("[test:int IN ('a', 'b', 'c')]", obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if res.Matched {
		t.Error("expected no match for IN ('a','b','c')")
	}
}

func TestMatch_InvalidPattern(t *testing.T) {
	obs := loadObs(t, "basic_obs.json")
	_, err := Match("[test:name = 'x'", obs, nil)
	if err == nil {
		t.Error("expected error for invalid pattern")
	}
}

func TestMatch_EmptyObs(t *testing.T) {
	res, err := Match("[test:int = 5]", nil, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if res.Matched {
		t.Error("expected no match with nil observations")
	}
}

func TestCompile_Match(t *testing.T) {
	obs := loadObs(t, "basic_obs.json")
	cp, err := Compile("[test:string = 'hello']")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	res, err := cp.Match(obs, nil)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !res.Matched {
		t.Error("expected match")
	}
}
