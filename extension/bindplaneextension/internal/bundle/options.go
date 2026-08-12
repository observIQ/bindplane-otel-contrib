// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bundle

// Options controls what the Bundler collects and how it encrypts the output.
type Options struct {
	// ManagerConfigPath is the path to the OpAMP manager config file.
	ManagerConfigPath string
	// CollectorConfigPath is the path to the OTel collector config file.
	CollectorConfigPath string
	// CollectorLogDir is the directory containing collector log files.
	// When empty, the implementation derives it from CollectorConfigPath as <configDir>/log.
	CollectorLogDir string
	// CollectorManagerConfig is the path to the collector's manager config (may equal ManagerConfigPath).
	CollectorManagerConfig string
	// CollectorProfileDir is the directory containing pprof profiles to include.
	CollectorProfileDir string
	// CollectorInstallRoot is the collector install root directory. When set, the bundle
	// includes plugins/, version.txt, and other install-level artifacts.
	CollectorInstallRoot string
	// OutputDir is where temporary bundle files are written. Defaults to os.TempDir().
	OutputDir string

	// EncryptionPublicKeyPEM is the PEM-encoded RSA public key used to encrypt the bundle
	// into BNDL format. When nil the default embedded key is used.
	EncryptionPublicKeyPEM []byte

	IncludeConfig       bool
	IncludeLogs         bool
	IncludeSystemInfo   bool
	IncludeProfiles     bool
	IncludeNetworkState bool
	ProfileMaxAge       int
}
