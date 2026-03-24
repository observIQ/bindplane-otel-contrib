package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGenmappings_RunWithTestdata(t *testing.T) {
	dir := t.TempDir()
	// Repo root: from internal/ocsf/cmd/genmappings go up 4 levels.
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	schemaDir := filepath.Join(root, "internal", "ocsf", "testdata", "schema")
	if _, err := os.Stat(schemaDir); err != nil {
		t.Skipf("testdata schema not found: %v", err)
	}
	cmd := exec.Command("go", "run", "./internal/ocsf/cmd/genmappings", "-schema-dir", schemaDir, "-out", dir)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("genmappings failed: %v\n%s", err, out)
	}
	outPath := filepath.Join(dir, "mappings_generated.go")
	if _, err := os.Stat(outPath); err != nil {
		t.Fatal(err)
	}
}
