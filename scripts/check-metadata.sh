#!/usr/bin/env bash
# Copyright observIQ, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Verifies every component module has a metadata.yaml declaring
# status.stability and status.codeowners. These feed docs generation and
# customer-facing support-tier commitments, so new components must not
# merge without them.

set -euo pipefail

fail=0
for mod in $(find receiver processor exporter extension -maxdepth 2 -name go.mod | sort); do
    dir=$(dirname "$mod")
    meta="$dir/metadata.yaml"
    if [ ! -f "$meta" ]; then
        echo "$dir: missing metadata.yaml"
        fail=1
        continue
    fi
    grep -q '^  stability:' "$meta" || { echo "$meta: missing status.stability"; fail=1; }
    grep -q '^  codeowners:' "$meta" || { echo "$meta: missing status.codeowners"; fail=1; }
done

if [ "$fail" -ne 0 ]; then
    echo "metadata check failed; see above"
    exit 1
fi
echo "all component modules have metadata.yaml with stability and codeowners"
