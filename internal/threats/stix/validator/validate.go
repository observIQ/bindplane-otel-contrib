package validator

import (
	"errors"
	"io"
	"os"
	"sort"
	"sync"

	"github.com/goccy/go-json"
)

// Exit codes per VALIDATOR_PLAN (same as Python codes.py).
const (
	ExitSuccess         = 0x0
	ExitFailure         = 0x1
	ExitSchemaInvalid   = 0x2
	ExitValidationError = 0x10
)

// DefaultOptions returns options with default values (STIX 2.1, no checks disabled).
func DefaultOptions() Options {
	return Options{
		Version: "2.1",
	}
}

// GetCode returns the exit code by ORing status across file results.
// Object errors -> ExitSchemaInvalid (0x2); fatal -> ExitValidationError (0x10).
func GetCode(results []FileResult) int {
	code := ExitSuccess
	for _, fr := range results {
		for _, o := range fr.ObjectResults {
			if len(o.Errors) > 0 {
				code |= ExitSchemaInvalid
				break
			}
		}
		if fr.Fatal != nil {
			code |= ExitValidationError
		}
	}
	return code
}

// ValidateFiles validates the given files and returns one FileResult per file.
func ValidateFiles(files []string, opts Options) ([]FileResult, error) {
	return ValidateFilesWithConfig(files, Config{Options: opts})
}

// ValidateFilesWithConfig validates the given files using the provided Config.
// Today this is equivalent to calling ValidateFiles with cfg.Options; future
// engine-level behavior (concurrency, limits) will be driven by additional
// Config fields.
func ValidateFilesWithConfig(files []string, cfg Config) ([]FileResult, error) {
	opts := cfg.Options
	loader, err := loaderForOptions(opts)
	if err != nil {
		return nil, err
	}
	results := make([]FileResult, 0, len(files))
	for _, filepath := range files {
		fr := FileResult{Filepath: filepath}
		data, err := os.ReadFile(filepath)
		if err != nil {
			fr.Fatal = &FatalResult{Message: err.Error()}
			fr.Result = false
			results = append(results, fr)
			continue
		}
		objResults, fatal := validateInput(data, loader, opts, cfg)
		fr.ObjectResults = objResults
		fr.Fatal = fatal
		fr.Result = fatal == nil && len(objResults) > 0 && allResultsOK(objResults)
		results = append(results, fr)
	}
	return results, nil
}

// ValidateReader validates JSON read from r (bundle or single object) and returns file results.
func ValidateReader(r io.Reader, opts Options) ([]FileResult, error) {
	return ValidateReaderWithConfig(r, Config{Options: opts})
}

// ValidateReaderWithConfig validates JSON read from r (bundle or single object)
// using the provided Config. Today this is equivalent to calling
// ValidateReader with cfg.Options; future engine-level behavior (limits,
// streaming preferences) will be driven by additional Config fields.
func ValidateReaderWithConfig(r io.Reader, cfg Config) ([]FileResult, error) {
	opts := cfg.Options
	loader, err := loaderForOptions(opts)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	objResults, fatal := validateInput(data, loader, opts, cfg)
	fr := FileResult{
		Filepath:      "stdin",
		ObjectResults: objResults,
		Fatal:         fatal,
		Result:        fatal == nil && len(objResults) > 0 && allResultsOK(objResults),
	}
	return []FileResult{fr}, nil
}

// ValidateReaderStreaming validates a STIX bundle read from r by decoding and validating
// one bundle object at a time, reducing peak memory to O(largest object) instead of O(full bundle).
// The input must be a bundle (top-level object with an "objects" array). For single objects
// or non-bundle input, use ValidateReader instead.
func ValidateReaderStreaming(r io.Reader, opts Options) ([]FileResult, error) {
	return ValidateReaderStreamingWithConfig(r, Config{Options: opts})
}

// ValidateReaderStreamingWithConfig validates a STIX bundle read from r using
// the provided Config. Today this is equivalent to calling
// ValidateReaderStreaming with cfg.Options; future engine-level behavior
// (concurrency, limits) will be driven by additional Config fields.
func ValidateReaderStreamingWithConfig(r io.Reader, cfg Config) ([]FileResult, error) {
	opts := cfg.Options
	loader, err := loaderForOptions(opts)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(r)
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, errors.New("streaming requires a top-level JSON object")
	}
	envelope := make(map[string]interface{})
	var foundObjects bool
	for {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if d, ok := t.(json.Delim); ok && d == '}' {
			break
		}
		key, ok := t.(string)
		if !ok {
			return nil, errors.New("expected object key")
		}
		if key == "objects" {
			t2, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if d, ok := t2.(json.Delim); ok && d == '[' {
				foundObjects = true
				break
			}
			if err := skipJSONValue(dec, t2); err != nil {
				return nil, err
			}
			continue
		}
		val, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if key == "type" || key == "id" || key == "spec_version" {
			envelope[key] = val
		} else {
			if err := skipJSONValue(dec, val); err != nil {
				return nil, err
			}
		}
	}
	if !foundObjects {
		return nil, errors.New("streaming requires a bundle (top-level object with 'objects' array)")
	}
	if typ, _ := envelope["type"].(string); typ != "bundle" {
		return nil, errors.New("streaming requires type \"bundle\"")
	}
	bundleErrs, err := ValidateObject(envelope, "bundle", loader)
	if err != nil {
		return nil, err
	}
	var results []ObjectResult
	if len(bundleErrs) > 0 {
		results = append(results, ObjectResult{
			Result: false, ObjectID: "", Errors: convertErrors(bundleErrs),
		})
	}
	if !cfg.ParallelizeBundles || cfg.MaxConcurrentObjects <= 1 {
		for {
			var obj map[string]interface{}
			if err := dec.Decode(&obj); err != nil {
				if err == io.EOF {
					break
				}
				var syntaxErr *json.SyntaxError
				if len(results) > 0 && errors.As(err, &syntaxErr) {
					break
				}
				return nil, err
			}
			results = append(results, validateOneBundleObject(obj, loader, opts))
		}
	} else {
		// Parallel path: decode on main goroutine, validate in worker pool with semaphore.
		// Collector runs in a separate goroutine so main can keep decoding and sending work.
		type workItem struct {
			idx int
			obj map[string]interface{}
		}
		type resultItem struct {
			idx int
			res ObjectResult
		}
		workCh := make(chan workItem, cfg.MaxConcurrentObjects)
		resultCh := make(chan resultItem, cfg.MaxConcurrentObjects)
		sem := make(chan struct{}, cfg.MaxConcurrentObjects)
		var workerWg sync.WaitGroup
		for w := 0; w < cfg.MaxConcurrentObjects; w++ {
			workerWg.Add(1)
			go func() {
				defer workerWg.Done()
				for item := range workCh {
					sem <- struct{}{}
					res := validateOneBundleObject(item.obj, loader, opts)
					<-sem
					resultCh <- resultItem{idx: item.idx, res: res}
				}
			}()
		}
		go func() {
			workerWg.Wait()
			close(resultCh)
		}()
		resultDone := make(chan []resultItem)
		go func() {
			indexed := make([]resultItem, 0)
			for r := range resultCh {
				indexed = append(indexed, r)
			}
			resultDone <- indexed
		}()
		var count int
		for {
			var obj map[string]interface{}
			if err := dec.Decode(&obj); err != nil {
				if err == io.EOF {
					break
				}
				var syntaxErr *json.SyntaxError
				if count > 0 && errors.As(err, &syntaxErr) {
					break
				}
				close(workCh)
				<-resultDone
				return nil, err
			}
			workCh <- workItem{idx: count, obj: obj}
			count++
		}
		close(workCh)
		indexed := <-resultDone
		if cfg.PreserveObjectOrder && len(indexed) > 1 {
			sort.Slice(indexed, func(i, j int) bool { return indexed[i].idx < indexed[j].idx })
		}
		for _, r := range indexed {
			results = append(results, r.res)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := dec.Token(); err != nil && err != io.EOF {
			return nil, err
		}
	}
	fr := FileResult{
		Filepath: "stdin", ObjectResults: results, Fatal: nil,
		Result: len(results) > 0 && allResultsOK(results),
	}
	return []FileResult{fr}, nil
}

func skipJSONValue(dec *json.Decoder, firstToken interface{}) error {
	depth := 0
	if d, ok := firstToken.(json.Delim); ok {
		if d == '{' || d == '[' {
			depth = 1
		} else {
			return nil
		}
	}
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else if d == '}' || d == ']' {
				depth--
			}
		}
	}
	return nil
}

func validateOneBundleObject(obj map[string]interface{}, loader *Loader, opts Options) ObjectResult {
	if obj == nil {
		return ObjectResult{Result: false, Errors: []SchemaError{{Message: "bundle object is not an object"}}}
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		return ObjectResult{Result: false, ObjectID: "", Errors: []SchemaError{{Message: "missing type in bundle object"}}}
	}
	if len(typ) > 2 && typ[:2] == "x-" && loader.CompiledForType(typ) == nil {
		objectID, _ := obj["id"].(string)
		return ObjectResult{
			Result: true, ObjectID: objectID,
			Warnings: []SchemaError{{Message: "Extension type " + typ + " not validated against schema."}},
		}
	}
	schemaErrs, err := ValidateObject(obj, typ, loader)
	if err != nil {
		return ObjectResult{Result: false, ObjectID: "", Errors: []SchemaError{{Message: err.Error()}}}
	}
	mustOpts := &MustOptions{Disabled: opts.Disabled}
	mustErrs := RunMUSTs(obj, typ, mustOpts)
	shouldOpts := ShouldOptions{
		Disabled: opts.Disabled, Enabled: opts.Enabled,
		StrictTypes: opts.StrictTypes, StrictProperties: opts.StrictProperties, EnforceRefs: opts.EnforceRefs,
	}
	shouldErrs := RunShoulds(obj, typ, shouldOpts)
	errs := convertErrors(schemaErrs)
	errs = append(errs, convertErrors(mustErrs)...)
	var warnings []SchemaError
	if opts.Strict {
		errs = append(errs, convertErrors(shouldErrs)...)
	} else {
		warnings = convertErrors(shouldErrs)
	}
	objectID, _ := obj["id"].(string)
	return ObjectResult{
		Result: len(errs) == 0, ObjectID: objectID, Errors: errs, Warnings: warnings,
	}
}

func loaderForOptions(opts Options) (*Loader, error) {
	if opts.SchemaDir != "" {
		return Load(opts.SchemaDir)
	}
	return BuiltinLoader(), nil
}

func validateInput(data []byte, loader *Loader, opts Options, cfg Config) ([]ObjectResult, *FatalResult) {
	var raw interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &FatalResult{Message: "invalid JSON: " + err.Error()}
	}
	objResults, err := validateValue(raw, loader, opts, cfg)
	if err != nil {
		return nil, &FatalResult{Message: err.Error()}
	}
	return objResults, nil
}

func validateValue(raw interface{}, loader *Loader, opts Options, cfg Config) ([]ObjectResult, error) {
	obj, ok := raw.(map[string]interface{})
	if !ok {
		return nil, errors.New("top-level value is not an object")
	}
	typ, _ := obj["type"].(string)
	if typ == "" {
		return nil, errors.New("missing type field")
	}
	if typ == "bundle" {
		return validateBundle(obj, loader, opts, cfg)
	}
	// Extension types (x-* per STIX 2.1): no compiled schema; treat as valid.
	if len(typ) > 2 && typ[:2] == "x-" && loader.CompiledForType(typ) == nil {
		objectID, _ := obj["id"].(string)
		return []ObjectResult{{
			Result:   true,
			ObjectID: objectID,
			Warnings: []SchemaError{{Message: "Extension type " + typ + " not validated against schema."}},
		}}, nil
	}
	// Single object
	schemaErrs, err := ValidateObject(obj, typ, loader)
	if err != nil {
		return nil, err
	}
	mustOpts := &MustOptions{Disabled: opts.Disabled}
	mustErrs := RunMUSTs(obj, typ, mustOpts)
	shouldOpts := ShouldOptions{
		Disabled:         opts.Disabled,
		Enabled:          opts.Enabled,
		StrictTypes:      opts.StrictTypes,
		StrictProperties: opts.StrictProperties,
		EnforceRefs:      opts.EnforceRefs,
	}
	shouldErrs := RunShoulds(obj, typ, shouldOpts)
	errors := convertErrors(schemaErrs)
	errors = append(errors, convertErrors(mustErrs)...)
	var warnings []SchemaError
	if opts.Strict {
		errors = append(errors, convertErrors(shouldErrs)...)
	} else {
		warnings = convertErrors(shouldErrs)
	}
	objectID, _ := obj["id"].(string)
	return []ObjectResult{{
		Result:   len(errors) == 0,
		ObjectID: objectID,
		Errors:   errors,
		Warnings: warnings,
	}}, nil
}

func validateBundle(bundle map[string]interface{}, loader *Loader, opts Options, cfg Config) ([]ObjectResult, error) {
	// Validate bundle object itself
	bundleErrs, err := ValidateObject(bundle, "bundle", loader)
	if err != nil {
		return nil, err
	}
	var results []ObjectResult
	if len(bundleErrs) > 0 {
		results = append(results, ObjectResult{
			Result:   false,
			ObjectID: "",
			Errors:   convertErrors(bundleErrs),
		})
	}
	objs, _ := bundle["objects"].([]interface{})
	if !cfg.ParallelizeBundles || cfg.MaxConcurrentObjects <= 1 {
		for _, o := range objs {
			obj, ok := o.(map[string]interface{})
			if !ok {
				results = append(results, ObjectResult{Result: false, Errors: []SchemaError{{Message: "bundle object is not an object"}}})
				continue
			}
			results = append(results, validateOneBundleObject(obj, loader, opts))
		}
		return results, nil
	}
	// Parallel path: semaphore-limited workers, preserve order by index
	objectResults := make([]ObjectResult, len(objs))
	sem := make(chan struct{}, cfg.MaxConcurrentObjects)
	var wg sync.WaitGroup
	for i, o := range objs {
		obj, ok := o.(map[string]interface{})
		if !ok {
			objectResults[i] = ObjectResult{Result: false, Errors: []SchemaError{{Message: "bundle object is not an object"}}}
			continue
		}
		wg.Add(1)
		go func(idx int, m map[string]interface{}) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			objectResults[idx] = validateOneBundleObject(m, loader, opts)
		}(i, obj)
	}
	wg.Wait()
	results = append(results, objectResults...)
	return results, nil
}

func convertErrors(errs []ValidationError) []SchemaError {
	out := make([]SchemaError, len(errs))
	for i, e := range errs {
		out[i] = SchemaError{Path: e.Path, Message: e.Message}
	}
	return out
}

func allResultsOK(objResults []ObjectResult) bool {
	for _, o := range objResults {
		if !o.Result {
			return false
		}
	}
	return true
}
