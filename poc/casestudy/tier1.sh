#!/usr/bin/env bash
# tier1.sh — the agent's own decision model on the node, held to fixed limits so it cannot
# take what the workloads need. Runs on the node, as root; provision.sh calls it.
#
# A node without a model of its own is not autonomous: the review loop's tier 1 runs here.
# Only the reasoning model, which a node this size cannot hold, is reached over the network.
#
# Limits (systemd drop-in, the ollama unit):
#   AllowedCPUs=4-5, CPUQuota=200%   at most two of the six CPUs, and always the same two
#   CPUWeight=20                     below a pod's default 100: a pod wins any contest for CPU
#   MemoryHigh=6G, MemoryMax=7G      tev1:4b needs ~4.6 GB resident
#   IOWeight=20
#   OLLAMA_MAX_LOADED_MODELS=1, OLLAMA_NUM_PARALLEL=1, OLLAMA_KEEP_ALIVE=-1
#                                    one model, one request at a time, kept loaded so memory is
#                                    steady rather than reloaded every review
# Ollama listens on 127.0.0.1:11434 only.
set -euo pipefail
MODEL="${1:-tev1:4b}"
command -v ollama >/dev/null || curl -fsSL https://ollama.com/install.sh | sh
mkdir -p /etc/systemd/system/ollama.service.d
cat >/etc/systemd/system/ollama.service.d/limits.conf <<'UNIT'
[Service]
AllowedCPUs=4-5
CPUQuota=200%
CPUWeight=20
MemoryHigh=6G
MemoryMax=7G
IOWeight=20
Environment="OLLAMA_HOST=127.0.0.1:11434"
Environment="OLLAMA_MAX_LOADED_MODELS=1"
Environment="OLLAMA_NUM_PARALLEL=1"
Environment="OLLAMA_KEEP_ALIVE=-1"
UNIT
systemctl daemon-reload
systemctl restart ollama
for _ in $(seq 30); do curl -sf localhost:11434/api/tags >/dev/null && break; sleep 1; done
ollama list | grep -q "^${MODEL%%:*}" || ollama pull "$MODEL"
systemctl show ollama -p AllowedCPUs -p CPUQuotaPerSecUSec -p CPUWeight -p MemoryMax
