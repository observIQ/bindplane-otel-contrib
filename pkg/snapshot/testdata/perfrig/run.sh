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
# Measure snapshotprocessor CPU/alloc overhead: with-snapshot vs no-snapshot,
# same image, same load, side by side.  Usage: ./run.sh [batch1|batch1000]
# Linux/amd64 VM variant of ../rig/run.sh.  Differences from the Mac rig:
#   * TAG derived from the image tag  (bdot-patched:budget -> budget, jit -> jit)
#   * metric names tolerate the _total suffix (collector build dependent)
#   * CPUS env var feeds docker-compose so the per-collector quota is tunable
set -uo pipefail
cd "$(dirname "$0")"

SCEN=${1:-batch1}
DUR=${DUR:-90}            # seconds of steady load
RATE=${RATE:-5000}        # per worker; 4 workers -> 20k records/s
WORKERS=${WORKERS:-4}
GEN=${GEN:-telemetrygen-local:v0.139.0}
IMAGE=${IMAGE:-observiq/bindplane-agent:1.107.0}
# stock upstream image -> "stock"; anything else -> its docker tag (budget, jit, ...)
TAG=${TAG:-$(case "$IMAGE" in observiq/bindplane-agent:*) echo stock ;; *) echo "${IMAGE##*:}" ;; esac)}
CPUS=${CPUS:-2}
OUT=results/$SCEN-$TAG
mkdir -p "$OUT"

export SCEN IMAGE CPUS
docker compose down -v >/dev/null 2>&1
docker compose up -d
until curl -sf localhost:18888/metrics >/dev/null && curl -sf localhost:28888/metrics >/dev/null; do sleep 1; done
sleep 3

NET=$(docker inspect snaprig-with -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}')

curl -s localhost:18888/metrics > "$OUT/with_metrics_before.txt"
curl -s localhost:28888/metrics > "$OUT/no_metrics_before.txt"

gen() { # $1=target host  $2=logfile
  docker run --rm --network "$NET" "$GEN" logs \
    --otlp-insecure --otlp-endpoint "$1:4317" \
    --rate "$RATE" --workers "$WORKERS" --duration "${DUR}s" \
    --body "snapshot overhead rig synthetic log record for cpu measurement" \
    --telemetry-attributes rig=\"snapshot\" --telemetry-attributes scenario=\"$SCEN\" \
    > "$2" 2>&1
}
gen with-snapshot "$OUT/gen_with.log" &
gen no-snapshot   "$OUT/gen_no.log" &

# docker stats sampler (--no-stream itself costs ~2s, so ~5s cadence)
( END=$((SECONDS+DUR-5))
  while [ $SECONDS -lt $END ]; do
    docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' snaprig-with snaprig-without
    sleep 3
  done ) > "$OUT/stats.csv" &
STATS=$!

# pprof CPU profile over the middle 30s of the run
sleep 20
curl -s "localhost:11777/debug/pprof/profile?seconds=30" -o "$OUT/with_cpu.pprof" &
curl -s "localhost:21777/debug/pprof/profile?seconds=30" -o "$OUT/no_cpu.pprof" &
sleep 32
curl -s "localhost:11777/debug/pprof/allocs" -o "$OUT/with_allocs.pprof"
curl -s "localhost:21777/debug/pprof/allocs" -o "$OUT/no_allocs.pprof"

wait $STATS
wait

curl -s localhost:18888/metrics > "$OUT/with_metrics_after.txt"
curl -s localhost:28888/metrics > "$OUT/no_metrics_after.txt"

# ---- summarise ----
# metric regexes tolerate an optional _total suffix: some collector builds export
# otelcol_receiver_accepted_log_records_total / otelcol_process_cpu_seconds_total.
{
echo "scenario=$SCEN image=$IMAGE tag=$TAG cpus=$CPUS duration=${DUR}s rate=$((RATE*WORKERS))/s per collector"
for n in snaprig-with snaprig-without; do
  awk -F, -v n="$n" '$1==n {gsub("%","",$2); s+=$2; c++} END {if(c) printf "%-16s mean_cpu=%.1f%%  samples=%d\n", n, s/c, c}' "$OUT/stats.csv"
done
for p in with no; do
  b=$(awk '/^otelcol_process_runtime_total_alloc_bytes(_total)?[ {]/{print $2}' "$OUT/${p}_metrics_before.txt")
  a=$(awk '/^otelcol_process_runtime_total_alloc_bytes(_total)?[ {]/{print $2}' "$OUT/${p}_metrics_after.txt")
  cb=$(awk '/^otelcol_process_cpu_seconds(_total)?[ {]/{print $2}' "$OUT/${p}_metrics_before.txt")
  ca=$(awk '/^otelcol_process_cpu_seconds(_total)?[ {]/{print $2}' "$OUT/${p}_metrics_after.txt")
  acc=$(awk '/^otelcol_receiver_accepted_log_records(_total)?[{ ]/{s+=$2} END{print s+0}' "$OUT/${p}_metrics_after.txt")
  awk -v p="$p" -v b="$b" -v a="$a" -v cb="$cb" -v ca="$ca" -v acc="$acc" -v d="$DUR" \
    'BEGIN{
       rec=acc/d;
       printf "%-6s alloc=%.1f MB/s  cpu_sec=%.1f (%.0f%% of 1 core)  accepted=%d (%.0f rec/s)", p, (a-b)/1048576/d, ca-cb, (ca-cb)/d*100, acc, rec;
       if (rec>0) printf "  cpu_us_per_rec=%.2f  alloc_kb_per_rec=%.2f", (ca-cb)*1e6/acc, (a-b)/1024/acc;
       else printf "  *** ACCEPTED==0: metric name mismatch, check %s_metrics_after.txt ***", p;
       printf "\n"}'
done
} | tee "$OUT/summary.txt"
