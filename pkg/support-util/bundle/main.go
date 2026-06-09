package bundle

import (
	"runtime"
)

// This file is a placeholder for the bundle module's main entry point
// The actual CLI integration will be in cmd/bindplane-support/main.go
// This allows the bundle package to be used as a library

// CreateBundleFromOptions is a convenience function that creates a bundle
// with default sources. This can be called from CLI or other entry points.
func CreateBundleFromOptions(opts BundleOptions, sources []ArtifactSource) (string, error) {
	var writer BundleWriter
	if runtime.GOOS == "windows" {
		writer = &ZipWriter{}
	} else {
		writer = &TarGzWriter{}
	}

	service := &BundleService{
		Sources: sources,
		Writer:  writer,
	}

	return service.CreateBundle(opts)
}

// Example usage (not actually called, just for reference):
// func example() {
// 	opts := DefaultBundleOptions()
// 	opts.OutputDir = "./bundles"
// 	opts.LogDir = "C:\\ProgramData\\BindplaneSupport\\logs"
// 	opts.ConfigPath = "config.yaml"
//
// 	// Sources would be created by the CLI entry point
// 	// and passed to CreateBundleFromOptions
//
// 	fmt.Fprintf(os.Stderr, "Bundle module loaded. Use from CLI: bindplane-support bundle create\n")
// }
