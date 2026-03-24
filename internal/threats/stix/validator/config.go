package validator

// Config groups both semantic validation options (Options) and engine-level
// behavior such as concurrency, streaming, and safety limits. It is intended
// to be the primary way library and CLI callers configure the validator.
type Config struct {
	// Options holds semantic validation settings (schema directory, version,
	// SHOULD/MUST behavior, strictness flags, verbosity, etc.).
	Options Options

	// MaxConcurrentObjects limits how many bundle objects may be validated in
	// parallel. A value of 0 or 1 means validation is effectively serial.
	// Larger values allow more parallelism at the cost of additional CPU/memory.
	MaxConcurrentObjects int

	// ParallelizeBundles controls whether bundle validation is allowed to use
	// per-object parallelism. When false, bundles are validated serially even
	// if MaxConcurrentObjects is greater than 1.
	ParallelizeBundles bool

	// PreserveObjectOrder indicates whether object-level results should be
	// returned in the same order as they appear in the input bundle. Disabling
	// this can unlock more aggressive parallel implementations at the cost of
	// deterministic ordering.
	PreserveObjectOrder bool

	// UseStreaming controls whether streaming bundle validation should be used
	// when possible (for large inputs or when explicitly requested). When false,
	// callers will continue to use the non-streaming APIs unless they opt in.
	UseStreaming bool

	// StreamingMinSizeBytes, when greater than zero, can be used by callers to
	// decide when to prefer streaming validation automatically based on input
	// size. It does not change behavior by itself; it is a hint for higher-level
	// orchestration code.
	StreamingMinSizeBytes int64

	// MaxObjectsPerBundle limits how many objects are validated from a single
	// bundle. A value of 0 means no explicit limit. This can be used as a
	// safety guard for untrusted or extremely large inputs.
	MaxObjectsPerBundle int

	// MaxErrorsPerObject limits how many errors are recorded per object before
	// truncation. A value of 0 means no explicit per-object limit.
	MaxErrorsPerObject int

	// MaxErrorsPerFile limits how many errors are recorded per file/bundle
	// before truncation. A value of 0 means no explicit per-file limit.
	MaxErrorsPerFile int

	// FailFast, when true, allows higher-level callers to stop validation
	// early once a threshold has been reached (for example, after the first
	// error). The core validator does not enforce a particular strategy; this
	// flag is provided so orchestration layers and future enhancements can
	// share a common configuration surface.
	FailFast bool
}

// DefaultConfig returns a Config value that mirrors the current default
// behavior of the validator: semantic defaults from DefaultOptions(), serial
// bundle validation, no additional limits, and deterministic object ordering.
func DefaultConfig() Config {
	return Config{
		Options:             DefaultOptions(),
		PreserveObjectOrder: true,
	}
}
