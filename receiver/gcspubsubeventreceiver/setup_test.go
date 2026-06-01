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

package gcspubsubeventreceiver

import "os"

// setupTestEnvironment is invoked from the generated TestMain (wired via the
// tests.goleak.setup hook in metadata.yaml). It points the GCP client
// libraries at a fake credentials file so the generated component lifecycle
// test can construct the Pub/Sub and GCS clients without real Application
// Default Credentials. The clients connect lazily, so no requests are made
// against these credentials during the test.
//
// If GOOGLE_APPLICATION_CREDENTIALS is already set (e.g. a developer running
// against real GCP), it is left untouched.
func setupTestEnvironment() {
	if _, ok := os.LookupEnv("GOOGLE_APPLICATION_CREDENTIALS"); !ok {
		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "testdata/fake_credentials.json")
	}
}
