package ianacodegen

import (
	"fmt"
	"os"
	"path/filepath"
)

// Run loads IANA CSVs from assetsDir, generates Go code, and writes iana.go into outDir.
func Run(assetsDir, outDir string) error {
	d, err := Load(assetsDir)
	if err != nil {
		return err
	}
	code, err := Generate(d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	outPath := filepath.Join(outDir, "iana.go")
	if err := os.WriteFile(outPath, code, 0644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}
