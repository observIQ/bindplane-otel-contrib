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
# Runs ON the GCP VM (Ubuntu 22.04). Installs Docker, records VM specs,
# builds the two rig images. Idempotent-ish: safe to re-run.
set -euo pipefail
cd ~/rig

# --- Docker Engine + compose plugin, official apt repo ---
if ! command -v docker >/dev/null; then
  sudo apt-get update -qq
  sudo apt-get install -y -qq ca-certificates curl gnupg
  sudo install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  sudo chmod a+r /etc/apt/keyrings/docker.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
  sudo apt-get update -qq
  sudo apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  sudo usermod -aG docker "$USER"
fi

# --- VM specs ---
mkdir -p results
{ echo "=== uname -a ==="; uname -a
  echo; echo "=== nproc ==="; nproc
  echo; echo "=== lscpu ==="; lscpu
  echo; echo "=== free -g ==="; free -g
  echo; echo "=== docker version ==="; sudo docker version
  echo; echo "=== docker compose version ==="; sudo docker compose version
} > results/vm-specs.txt 2>&1

# --- images ---
sudo docker build -t telemetrygen-local:v0.139.0 telemetrygen/
sudo docker pull observiq/bindplane-agent:1.107.0
sudo docker image inspect observiq/bindplane-agent:1.107.0 -f 'stock image arch={{.Os}}/{{.Architecture}}' | tee -a results/vm-specs.txt
for b in budget jit; do
  sudo docker build -t "bdot-patched:$b" --build-arg "BIN=collector_linux_amd64_$b" -f Dockerfile.patched .
  sudo docker image inspect "bdot-patched:$b" -f "$b image arch={{.Os}}/{{.Architecture}}" | tee -a results/vm-specs.txt
done
sudo docker images | tee -a results/vm-specs.txt
