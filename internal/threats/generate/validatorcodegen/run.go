package validatorcodegen

import (
	"fmt"
	"os"
	"path/filepath"
)

// Run loads schemas from schemaDir, generates Go source, and writes
// schemas_generated.go into outDir (e.g. internal/threats/stix/validator).
func Run(schemaDir, outDir string) error {
	schemas, typeToPath, err := Load(schemaDir)
	if err != nil {
		return fmt.Errorf("load schemas: %w", err)
	}
	code, err := Generate(schemas, typeToPath)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	outPath := filepath.Join(outDir, "schemas_generated.go")
	if err := os.WriteFile(outPath, code, 0644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}
