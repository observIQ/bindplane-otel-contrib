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

# retry.sh — run a command, retrying only on transient network/registry failures.
#
# Absorbs CI flakes from Go module-proxy blips (proxy.golang.org HTTP/2 stream
# errors, i/o timeouts) and Docker Hub pull timeouts (registry-1.docker.io).
# Bounded, so a persistent outage still fails the job after RETRY_MAX attempts.
#
# Retries only when the output carries a transient network signature, so a real
# compile/test failure is never masked. RETRY_ANY=1 retries on any non-zero exit.
#
# Usage: bash .github/scripts/retry.sh <command> [args...]
# Env:   RETRY_MAX (default 3), RETRY_DELAY base backoff seconds (default 10),
#        RETRY_ANY (default 0).
set -uo pipefail

max="${RETRY_MAX:-3}"
base_delay="${RETRY_DELAY:-10}"
retry_any="${RETRY_ANY:-0}"

# Transient network/registry failures that clear on a retry. Match error
# signatures, not bare hostnames: a permanent 404 names the same host as a
# transient blip, so matching the host would wrongly retry it. A permanent error
# carries no signature here, so it falls through and is not retried.
transient_re='i/o timeout|TLS handshake timeout|connection reset|connection refused|unexpected EOF|EOF$|GOAWAY|http2:|stream error|INTERNAL_ERROR|net/http: request canceled|Client\.Timeout exceeded|Internal Server Error|Service Unavailable|Bad Gateway|Gateway Time-?out|Too Many Requests|temporary failure|no such host|Could not resolve host'

# One temp file for the run. The EXIT trap clears it on every exit path; the
# INT/TERM traps exit (which fires EXIT), so a cancelled job dies immediately
# instead of the loop treating the killed command as a failed attempt and
# launching another one.
log="$(mktemp)"
trap 'rm -f "$log"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

attempt=1
while true; do
  "$@" 2>&1 | tee "$log"
  status="${PIPESTATUS[0]}"
  if [ "$status" -eq 0 ]; then
    exit 0
  fi

  if [ "$attempt" -ge "$max" ]; then
    echo "retry: '$*' failed after ${max} attempt(s) (exit ${status}); giving up." >&2
    exit "$status"
  fi

  if [ "$retry_any" != "1" ] && ! grep -qiE "$transient_re" "$log"; then
    echo "retry: '$*' failed (exit ${status}) with no transient-network signature; not retrying." >&2
    exit "$status"
  fi

  delay=$(( base_delay * attempt ))
  echo "retry: transient failure on attempt ${attempt}/${max} (exit ${status}); retrying '$*' in ${delay}s..." >&2
  sleep "$delay"
  attempt=$(( attempt + 1 ))
done
