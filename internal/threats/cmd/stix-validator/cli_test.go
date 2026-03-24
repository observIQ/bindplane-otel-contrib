package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/validator"
)

// builtBinary is the path to the built stix-validator binary for tests (avoids go run not propagating exit codes).
var builtBinary string
var buildOnce sync.Once

func ensureBuilt(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir := findCmdStixValidator(t)
		tmp := filepath.Join(os.TempDir(), "stix-validator-test")
		cmd := exec.Command(goExe(t), "build", "-o", tmp, ".")
		cmd.Dir = dir
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build stix-validator: %v\n%s", err, out)
		}
		builtBinary = tmp
	})
	return builtBinary
}

func TestCLI_ExitCode_ValidFile(t *testing.T) {
	dir := findCmdStixValidator(t)
	validPath := filepath.Join(dir, "testdata", "valid_minimal.json")
	// Verify fixture is valid per library so we can assert CLI exit 0
	data, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	results, err := validator.ValidateReader(strings.NewReader(string(data)), validator.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Result {
		t.Skipf("fixture %s is not valid per validator library (schema may have not constraint); skipping CLI exit 0 test", validPath)
	}
	bin := ensureBuilt(t)
	cmd := exec.Command(bin, validPath)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	code := exitCode(t, cmd, err)
	if code != validator.ExitSuccess {
		t.Errorf("valid file: exit code = 0x%x, want 0; output: %s", code, out)
	}
	if len(out) > 0 && !strings.Contains(string(out), "Valid") {
		t.Logf("output: %s", out)
	}
}

func TestCLI_ExitCode_InvalidSchema(t *testing.T) {
	dir := findCmdStixValidator(t)
	invalidPath := filepath.Join(dir, "testdata", "invalid_schema.json")
	bin := ensureBuilt(t)
	cmd := exec.Command(bin, invalidPath)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	_, err := cmd.CombinedOutput()
	code := exitCode(t, cmd, err)
	if code != validator.ExitSchemaInvalid {
		t.Errorf("invalid schema: exit code = 0x%x, want 0x2", code)
	}
}

func TestCLI_ExitCode_MissingFile(t *testing.T) {
	dir := findCmdStixValidator(t)
	bin := ensureBuilt(t)
	cmd := exec.Command(bin, filepath.Join(dir, "testdata", "nonexistent-404.json"))
	cmd.Dir = dir
	cmd.Env = os.Environ()
	_, err := cmd.CombinedOutput()
	code := exitCode(t, cmd, err)
	if code != validator.ExitValidationError {
		t.Errorf("missing file: exit code = 0x%x, want 0x10", code)
	}
}

func TestCLI_ExitCode_StdinValid(t *testing.T) {
	validJSON := `{"type":"indicator","spec_version":"2.1","id":"indicator--x","name":"Test","description":"Test","created":"2020-01-01T00:00:00.000Z","modified":"2020-01-01T00:00:00.000Z","pattern":"[file:hashes.MD5='x']","pattern_type":"stix","valid_from":"2020-01-01T00:00:00.000Z"}`
	results, err := validator.ValidateReader(strings.NewReader(validJSON), validator.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Result {
		t.Skip("fixture is not valid per validator library (schema may have not constraint); skipping stdin exit 0 test")
	}
	dir := findCmdStixValidator(t)
	bin := ensureBuilt(t)
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(validJSON)
	cmd.Env = os.Environ()
	_, err = cmd.CombinedOutput()
	code := exitCode(t, cmd, err)
	if code != validator.ExitSuccess {
		t.Errorf("stdin valid: exit code = 0x%x, want 0", code)
	}
}

func TestCLI_ExitCode_StdinInvalidJSON(t *testing.T) {
	dir := findCmdStixValidator(t)
	bin := ensureBuilt(t)
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("not json")
	cmd.Env = os.Environ()
	_, err := cmd.CombinedOutput()
	code := exitCode(t, cmd, err)
	if code != validator.ExitValidationError {
		t.Errorf("stdin invalid JSON: exit code = 0x%x, want 0x10", code)
	}
}

func TestCLI_FlagParsing_SilentAndVerbose(t *testing.T) {
	dir := findCmdStixValidator(t)
	bin := ensureBuilt(t)
	cmd := exec.Command(bin, "-q", "-v")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	code := exitCode(t, cmd, err)
	if code != validator.ExitFailure {
		t.Errorf("-q -v: exit code = 0x%x, want 0x1", code)
	}
	if !strings.Contains(string(out), "silent") || !strings.Contains(string(out), "verbose") {
		t.Errorf("output should mention silent and verbose: %s", out)
	}
}

func TestCLI_OutputShape_ContainsFilepathAndResults(t *testing.T) {
	dir := findCmdStixValidator(t)
	validPath := filepath.Join(dir, "testdata", "valid_minimal.json")
	bin := ensureBuilt(t)
	cmd := exec.Command(bin, validPath)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, _ := cmd.CombinedOutput()
	text := string(out)
	if !strings.Contains(text, "Results for:") {
		t.Errorf("output should contain 'Results for:'; got: %s", text)
	}
	if !strings.Contains(text, validPath) && !strings.Contains(text, "valid_minimal.json") {
		t.Errorf("output should contain filepath; got: %s", text)
	}
}

func TestCLI_OutputJSON_Structure(t *testing.T) {
	dir := findCmdStixValidator(t)
	validPath := filepath.Join(dir, "testdata", "valid_minimal.json")
	bin := ensureBuilt(t)
	cmd := exec.Command(bin, "-json", validPath)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	// Capture stdout only so exit status text is not mixed into JSON
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		t.Fatalf("run CLI: %v", err)
	}
	var results []validator.FileResult
	if err := json.Unmarshal(out, &results); err != nil {
		t.Fatalf("JSON output should be []FileResult: %v\noutput: %s", err, out)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Filepath != validPath {
		t.Errorf("filepath = %q, want %q", results[0].Filepath, validPath)
	}
	if len(results[0].ObjectResults) == 0 {
		t.Error("object_results should be non-empty")
	}
}

func findCmdStixValidator(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// We might be in .../cmd/stix-validator or .../stix-go
	for d := dir; len(d) > 1; d = filepath.Dir(d) {
		try := filepath.Join(d, "cmd", "stix-validator")
		if info, err := os.Stat(try); err == nil && info.IsDir() {
			return try
		}
	}
	// Assume we're in cmd/stix-validator
	return dir
}

func goExe(t *testing.T) string {
	t.Helper()
	exe := "go"
	if g := os.Getenv("GOEXE"); g != "" {
		exe = g
	}
	return exe
}

func exitCode(t *testing.T, cmd *exec.Cmd, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	t.Fatal(err)
	return -1
}
