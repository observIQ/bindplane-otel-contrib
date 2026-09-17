#!/usr/bin/env bash
# Copyright  observIQ, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
# Runs on the Mac over vmrig/results/ after the profiles are scp'd back.
# Go CPU/heap profiles carry their own symbols, so the collector binary is not needed.
set -uo pipefail
cd "$(dirname "$0")/results"

DIRS=${DIRS:-"batch1-stock batch1000-stock batch1-budget batch1000-budget batch1-jit batch1000-jit"}

for d in $DIRS; do
  [ -d "$d" ] || continue
  for side in with no; do
    [ -s "$d/${side}_cpu.pprof" ]    && go tool pprof -top -cum -nodecount=30 "$d/${side}_cpu.pprof" > "$d/${side}_cpu_cum.txt" 2>/dev/null
    [ -s "$d/${side}_allocs.pprof" ] && go tool pprof -top -nodecount=30 -sample_index=alloc_space "$d/${side}_allocs.pprof" > "$d/${side}_allocs_top.txt" 2>/dev/null
  done
done

# the nodes the report asks for: cum% from the with-snapshot CPU profile
echo "dir,symbol,flat%,cum%"
for d in $DIRS; do
  [ -s "$d/with_cpu.pprof" ] || continue
  go tool pprof -top -cum -nodecount=100000 -nodefraction=0 "$d/with_cpu.pprof" 2>/dev/null \
  | awk -v d="$d" '
      /snapshotProcessor\)\.processLogs|LogBuffer\)\.Add|admission\)\.exhausted|admission\)\.decide|\.CopyTo|runtime\.mallocgc/ {
        flat=$2; cum=$5; sym=$6; for(i=7;i<=NF;i++) sym=sym" "$i;
        printf "%s,%s,%s,%s\n", d, sym, flat, cum }'
done
