// Package validator provides the public API for validating STIX 2.1 JSON.
// Result types use an extended shape: object_id and warnings on each object result;
// fatal is optional (omit or null when absent).
package validator

// Options holds validation options (schema dir, disabled/enabled checks, strict, etc.).
type Options struct {
	// SchemaDir is the path to the JSON Schema directory (e.g. cti-stix2-json-schemas/schemas).
	// If empty, a default is used.
	SchemaDir string `json:"-"`

	// Version is the STIX spec version to validate against (e.g. "2.1").
	Version string `json:"-"`

	// Disabled is the list of SHOULD check names/codes to skip.
	Disabled []string `json:"-"`

	// Enabled is the list of SHOULD check names/codes to run. If non-empty and Disabled is empty,
	// only these checks run; otherwise all checks run except those in Disabled.
	Enabled []string `json:"-"`

	// Strict treats SHOULD violations as errors instead of warnings.
	Strict bool `json:"-"`

	// StrictTypes warns/errors on custom object types (only spec-defined types allowed).
	StrictTypes bool `json:"-"`

	// StrictProperties warns/errors on custom properties (only spec-defined properties allowed).
	StrictProperties bool `json:"-"`

	// EnforceRefs ensures SDOs referenced by SROs are in the same bundle.
	EnforceRefs bool `json:"-"`

	// Interop runs with interop validation settings.
	Interop bool `json:"-"`

	// Verbose prints informational notes and more verbose error messages.
	Verbose bool `json:"-"`

	// Silent suppresses all stdout output.
	Silent bool `json:"-"`
}

// FileResult holds validation results for a single file (bundle or list of objects).
type FileResult struct {
	// Result is true if the file validated successfully (no errors).
	Result bool `json:"result"`

	// Filepath is the path of the validated file (or "stdin" for reader input).
	Filepath string `json:"filepath"`

	// ObjectResults are the per-object validation results.
	ObjectResults []ObjectResult `json:"object_results"`

	// Fatal is set when a non-validation error occurred (e.g. invalid JSON, file not found).
	// Omit or null when absent.
	Fatal *FatalResult `json:"fatal,omitempty"`
}

// ObjectResult holds validation results for a single STIX object.
type ObjectResult struct {
	// Result is true if the object validated successfully.
	Result bool `json:"result"`

	// ObjectID is the id of the STIX object (e.g. "indicator--...").
	ObjectID string `json:"object_id"`

	// Errors are schema and MUST check failures.
	Errors []SchemaError `json:"errors,omitempty"`

	// Warnings are SHOULD check findings (or errors when Options.Strict is true).
	Warnings []SchemaError `json:"warnings,omitempty"`
}

// SchemaError represents a single validation error (path + message).
type SchemaError struct {
	// Path is the JSON path where the error occurred (e.g. "pattern", "created").
	Path string `json:"path,omitempty"`

	// Message is the error message.
	Message string `json:"message"`
}

// FatalResult represents a fatal error (e.g. invalid JSON, file not found).
type FatalResult struct {
	Message string `json:"message"`
}
