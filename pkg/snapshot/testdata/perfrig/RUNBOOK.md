# BPOP-5844 snapshot-processor A/B benchmark on a GCP Linux/amd64 VM

Reproduces the local Docker rig at `../rig/` on an x86-64 Linux VM, for three collector
builds (`stock`, `budget`, `jit`) x two scenarios (`batch1`, `batch1000`).

**Status: EXECUTED 2026-09-17.** All six runs completed. The VM `bpop5844-bench` is
STOPPED (not deleted) with all three images cached on its disk.

`$VMRIG` below means this directory:

```
VMRIG=<this directory>
GC="--zone=us-central1-a --project=iris-dev-351502 --quiet"
```

Every `gcloud compute ssh|scp` below was run with `$GC`. SSH went over the VM's external
IP (default gcloud SSH); no IAP tunnel and no extra firewall rules were needed.

---

---

## Appendix A — first attempt failed on IAM (resolved 2026-09-17)

Run, twice, at 2026-09-17:

```
gcloud compute instances create bpop5844-bench \
  --zone=us-central1-a --machine-type=n2-standard-4 \
  --image-family=ubuntu-2204-lts --image-project=ubuntu-os-cloud \
  --boot-disk-size=50GB --boot-disk-type=pd-balanced \
  --project=iris-dev-351502
```

Exact error (also saved at `vm-create-error.txt`):

```
ERROR: (gcloud.compute.instances.create) Could not fetch resource:
 - Required 'compute.instances.create' permission for 'projects/iris-dev-351502/zones/us-central1-a/instances/bpop5844-bench'
```

Diagnosis:

```
gcloud auth list                       # ekansh.gupta@bindplane.com (active, only account)
gcloud config list                     # project = iris-dev-351502

gcloud projects get-iam-policy iris-dev-351502 \
  --flatten='bindings[].members' \
  --filter='bindings.members:ekansh.gupta@bindplane.com' \
  --format='value(bindings.role)'
# -> roles/pubsub.admin
#    roles/serviceusage.serviceUsageConsumer
#    roles/storage.admin
#    roles/viewer            <-- read-only on compute; no instances.create

gcloud compute instances list --project=iris-dev-351502   # works (viewer), 40+ instances exist
```

No other project is usable either — `compute.instances.list` is denied in
`otel-agent-dev`, `bpcli-dev`, `bindplane-op-scale-testing`, and the Compute API is
not enabled in `bindplane-internal`:

```
for p in otel-agent-dev bpcli-dev bindplane-op-scale-testing bindplane-internal; do
  gcloud compute instances list --project="$p" --limit=3
done
# -> Required 'compute.instances.list' permission for 'projects/<p>'   (x3)
# -> Compute Engine API has not been used in project bindplane-internal ...
```

**Resolved:** `roles/compute.admin`, `roles/compute.osAdminLogin` and
`roles/iam.serviceAccountUser` were granted on `iris-dev-351502`, after which every
command below ran clean. (Original note: grant `roles/compute.instanceAdmin.v1` (+ `roles/iap.tunnelResourceAccessor`
if SSH must go over IAP).)

---

## What is staged in this directory

| Path | What it is |
|---|---|
| `bin/collector_linux_amd64_budget` | budget build, 380342434 B — `ELF 64-bit LSB executable, x86-64` (verified) |
| `bin/collector_linux_amd64_jit` | on-demand build, 380350626 B — `ELF 64-bit LSB executable, x86-64` (verified) |
| `telemetrygen/telemetrygen` | telemetrygen v0.139.0, 24 MB — `ELF 64-bit LSB executable, x86-64` (verified) |
| `telemetrygen/Dockerfile` | `FROM scratch` wrapper, copied from `../rig` |
| `docker-compose.yml` | copied from `../rig`, `cpus: 2` → `cpus: ${CPUS:-2}` |
| `configs/batch1/{with,no}.yaml`, `configs/batch1000/{with,no}.yaml` | copied unchanged from `../rig` |
| `Dockerfile.patched` | copied from `../rig`, `COPY collector_linux_arm64` → `ARG BIN` + `COPY ${BIN}` (serves budget and jit) |
| `logging.yaml` | copied unchanged from `../rig` |
| `run.sh` | `../rig/run.sh` + 3 changes, see below |
| `remote-setup.sh` | new: Docker install + specs capture + all three image builds, runs on the VM |
| `analyze.sh` | new: pprof text extraction, runs on the Mac over `results/` |
| `results/<scen>-<tag>/` | the six runs' output, pulled back from the VM |

### telemetrygen cross-build (already done, on this Mac)

```
GOOS=linux GOARCH=amd64 go install github.com/open-telemetry/opentelemetry-collector-contrib/cmd/telemetrygen@v0.139.0
cp "$(go env GOPATH)/bin/linux_amd64/telemetrygen" $VMRIG/telemetrygen/telemetrygen
file $VMRIG/telemetrygen/telemetrygen
# ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, Go BuildID=...
```

### run.sh changes vs `../rig/run.sh`

1. **Tag derivation** — the Mac version hard-codes `stock` vs `patched`; it now yields
   `stock` for `observiq/bindplane-agent:*` and otherwise the image's docker tag, so
   `bdot-patched:budget` → `budget` and the later `bdot-patched:jit` → `jit`:
   ```sh
   TAG=${TAG:-$(case "$IMAGE" in observiq/bindplane-agent:*) echo stock ;; *) echo "${IMAGE##*:}" ;; esac)}
   ```
2. **`_total`-tolerant metric names** — the Mac rig silently reported `accepted=0` when the
   metric was `otelcol_receiver_accepted_log_records_total`. All three regexes now accept the
   suffix, and the summary prints a loud `*** ACCEPTED==0 ... ***` instead of a silent zero:
   ```sh
   /^otelcol_process_runtime_total_alloc_bytes(_total)?[ {]/
   /^otelcol_process_cpu_seconds(_total)?[ {]/
   /^otelcol_receiver_accepted_log_records(_total)?[{ ]/
   ```
   Verified against both metric spellings:
   ```
   printf 'otelcol_process_cpu_seconds_total{x="1"} 12.5\n...' > t1.txt   # -> cpu=12.5 acc=1234 alloc=999
   printf 'otelcol_process_cpu_seconds 12.5\n...'              > t2.txt   # -> cpu=12.5 acc=1234 alloc=999
   ```
3. **Per-record figures + `CPUS`** — the summary now also prints `cpu_us_per_rec` and
   `alloc_kb_per_rec` directly, and `CPUS=3 ./run.sh ...` re-runs with a wider quota.

---

## Step 1 — create the VM

`n2-standard-8`, **not** the originally planned `n2-standard-4`: one run puts two
collectors (2 cpus each) *plus* two telemetrygen containers (4 workers each) on the same
box, which does not fit in 4 vCPU. `cpus: 2` per collector is kept unchanged so the
numbers stay comparable with the Mac Docker rig.

```
gcloud compute instances create bpop5844-bench \
  --zone=us-central1-a --machine-type=n2-standard-8 \
  --image-family=ubuntu-2204-lts --image-project=ubuntu-os-cloud \
  --boot-disk-size=50GB --boot-disk-type=pd-balanced \
  --project=iris-dev-351502
# NAME            ZONE           MACHINE_TYPE   INTERNAL_IP   EXTERNAL_IP  STATUS
# bpop5844-bench  us-central1-a  n2-standard-8  10.128.0.101  34.69.77.73  RUNNING

# wait for SSH (first connect also generates/pushes the key)
until gcloud compute ssh bpop5844-bench $GC --command='echo SSH_UP; uname -r'; do sleep 10; done
# SSH_UP / 6.8.0-1066-gcp
```

## Step 2 — copy the rig up

Small files first:

```
cd $VMRIG
gcloud compute ssh bpop5844-bench $GC --command='mkdir -p ~/rig'
gcloud compute scp --recurse $GC \
  docker-compose.yml Dockerfile.patched logging.yaml run.sh remote-setup.sh configs telemetrygen \
  bpop5844-bench:~/rig/
```

The two 380 MB collector binaries are gzipped first — they compress to 30% (114 MB each),
which turned a ~20 min upload into ~2 min:

```
cd $VMRIG/bin
gzip -1 -c collector_linux_amd64_budget > /tmp/budget.gz &   # 380342434 -> 114088564
gzip -1 -c collector_linux_amd64_jit    > /tmp/jit.gz    &   # 380350626 -> 114093007
wait

gcloud compute scp $GC /tmp/budget.gz bpop5844-bench:~/rig/budget.gz &
gcloud compute scp $GC /tmp/jit.gz    bpop5844-bench:~/rig/jit.gz    &
wait

gcloud compute ssh bpop5844-bench $GC --command='cd ~/rig \
  && gunzip -f budget.gz && gunzip -f jit.gz \
  && mv budget collector_linux_amd64_budget && mv jit collector_linux_amd64_jit \
  && chmod +x collector_linux_amd64_*'
```

## Step 3 — Docker, specs, images (on the VM)

```
gcloud compute ssh bpop5844-bench $GC --command='chmod +x ~/rig/*.sh && ~/rig/remote-setup.sh'
```

`remote-setup.sh` installs Docker Engine + the compose plugin from the official apt repo,
`usermod -aG docker`, writes `results/vm-specs.txt` (`uname -a`, `nproc`, `lscpu`,
`free -g`, `docker version`, `docker compose version`), then builds/pulls:

```
docker build -t telemetrygen-local:v0.139.0 telemetrygen/
docker pull observiq/bindplane-agent:1.107.0        # -> stock image arch=linux/amd64
for b in budget jit; do
  docker build -t "bdot-patched:$b" --build-arg "BIN=collector_linux_amd64_$b" -f Dockerfile.patched .
done                                                 # -> budget/jit image arch=linux/amd64
```

`Dockerfile.patched` was made binary-agnostic for this (`ARG BIN=collector_linux_amd64_budget`
+ `COPY ${BIN} /collector/observiq-otel-collector`) so one Dockerfile serves budget and jit.

Go is **not** installed on the VM: telemetrygen is cross-built on the Mac and all pprof
text extraction happens on the Mac (Go profiles carry their own symbols, so the stripped
collector binary is not needed).

## Step 4 — the six runs

Driven by `all.sh`, written on the VM and launched under `nohup` so an SSH drop cannot
kill a run. ~101 s per run, ~10 min total.

```
gcloud compute ssh bpop5844-bench $GC --command='cat > ~/rig/all.sh <<"EOF"
#!/usr/bin/env bash
cd ~/rig
for img in observiq/bindplane-agent:1.107.0 bdot-patched:budget bdot-patched:jit; do
  for scen in batch1 batch1000; do
    echo "########## IMAGE=$img SCEN=$scen $(date -Is)"
    IMAGE=$img ./run.sh $scen
    docker compose down -v >/dev/null 2>&1
    sleep 5
  done
done
echo "########## ALL_RUNS_COMPLETE $(date -Is)"
EOF
chmod +x ~/rig/all.sh && cd ~/rig && nohup ./all.sh > ~/rig/all.log 2>&1 &'

# poll until finished
until gcloud compute ssh bpop5844-bench $GC --command='grep -q ALL_RUNS_COMPLETE ~/rig/all.log'; do sleep 30; done
```

Each run: 90 s steady load, `telemetrygen logs --rate 5000 --workers 4` (20 k/s offered)
into **both** collectors simultaneously, docker-stats sampled every ~5 s, a 30 s pprof CPU
profile over the middle of the run plus an `allocs` heap profile, and `/metrics` scraped
before and after. Output lands in `results/<scenario>-<tag>/`, tags `stock`/`budget`/`jit`
derived from the image (see the run.sh change list above), so the six runs do not clobber
each other.

Every summary was checked for a non-zero `accepted=`; none tripped the
`*** ACCEPTED==0 ***` guard. This build spells the metric without the suffix:

```
grep -m1 '^otelcol_receiver_accepted_log_records' results/batch1-stock/with_metrics_after.txt
# otelcol_receiver_accepted_log_records{receiver="otlp",transport="grpc"} 733024
```

## Step 5 — pull results back

```
cd $VMRIG
gcloud compute scp --recurse $GC bpop5844-bench:'~/rig/results/*' results/
```

## Step 6 — pprof text (on the Mac)

```
$VMRIG/analyze.sh
```

Writes `<dir>/{with,no}_cpu_cum.txt` (`-top -cum -nodecount=30`) and
`<dir>/{with,no}_allocs_top.txt` (`-top -nodecount=30 -sample_index=alloc_space`) for all
twelve profiles, then prints the CSV of the report's symbols. It uses
`-nodefraction=0` so sub-0.1% nodes are not dropped — that matters for the jit build,
where `LogBuffer.Add` is expected to be hundredths of a percent.


## Step 7 — generator-bottleneck probe (WORKERS=6)

Every run landed at 8.1-9.7 k rec/s against a 20 k/s offer, well under the ~15 k bar, so
the instructed probe was run on the highest-signal pair (stock / batch1), tagged
separately so it cannot clobber `batch1-stock`:

```
gcloud compute ssh bpop5844-bench $GC --command='cd ~/rig \
  && WORKERS=6 TAG=stock-w6 IMAGE=observiq/bindplane-agent:1.107.0 ./run.sh batch1'
gcloud compute scp --recurse $GC bpop5844-bench:'~/rig/results/batch1-stock-w6' results/
DIRS="batch1-stock-w6" ./analyze.sh
```

50% more offered load (30 k/s) bought only ~11% more throughput and did not move the
overhead figure — see the report. `TAG=` is honoured by run.sh's `TAG=${TAG:-...}` default.

## Teardown

**Do not delete the VM** — it is stopped, not deleted, pending review. All four images
(`observiq/bindplane-agent:1.107.0`, `bdot-patched:budget`, `bdot-patched:jit`,
`telemetrygen-local:v0.139.0`) and `~/rig` persist on its boot disk, so a restart is
immediately ready to run.

```
gcloud compute instances stop bpop5844-bench --zone=us-central1-a --project=iris-dev-351502
gcloud compute instances start bpop5844-bench --zone=us-central1-a --project=iris-dev-351502
```

For a fourth build later: scp the binary as `~/rig/collector_linux_amd64_<name>`, then

```
docker build -t "bdot-patched:<name>" --build-arg "BIN=collector_linux_amd64_<name>" -f Dockerfile.patched .
IMAGE=bdot-patched:<name> ./run.sh batch1     # run.sh tags the output <name> automatically
IMAGE=bdot-patched:<name> ./run.sh batch1000
```
