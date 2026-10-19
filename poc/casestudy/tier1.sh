#!/usr/bin/env bash
# tier1.sh — the agent's own decision model on the node, held to fixed limits so it cannot
# take what the workloads need. Runs on the node, as root; provision.sh calls it.
#
# A node without a model of its own is not autonomous: the review loop's tier 1 runs here.
# Only the reasoning model, which a node this size cannot hold, is reached over the network.
#
# Limits (systemd drop-in, the ollama unit):
#   AllowedCPUs=6-9, CPUQuota=400%   the four CPUs node-cpus.sh reserves from Kubernetes, and no more;
#                                    on two CPUs a review took 1-2 min against the agent's 2 min timeout
#   CPUWeight=20                     below a pod's default 100: a pod wins any contest for CPU
#   MemoryHigh=8G, MemoryMax=9G      tev1:4b-node, resident 6.2 GB and more as cache allows: 4.6 GB of weights and the
#                                    decision runner's buffers. A 6 GB MemoryHigh throttled it
#                                    (10k events in 7 min, 1m39s a review). It is tev1:4b at a
#                                    2048-token context. The base
#                                    model's own num_ctx of 8192 overrides OLLAMA_CONTEXT_LENGTH
#                                    and needs 6.4 GB, sits at a 6 GB MemoryHigh and is throttled
#                                    into the agent's 2 min timeout (seen in the 2026-10-19b run),
#                                    so the node runs a derived model with num_ctx set
#   IOWeight=20
#   OLLAMA_MAX_LOADED_MODELS=1, OLLAMA_NUM_PARALLEL=1, OLLAMA_KEEP_ALIVE=-1
#                                    one model, one request at a time, kept loaded so memory is
#                                    steady rather than reloaded every review
#   OLLAMA_CONTEXT_LENGTH=2048, OLLAMA_FLASH_ATTENTION=1
#                                    a review prompt is ~800 tokens; a smaller KV cache is memory
#                                    the workloads keep
# Ollama listens on 127.0.0.1:11434 only.
set -euo pipefail
BASE="${1:-tev1:4b}"
MODEL="$BASE-node"
command -v ollama >/dev/null || curl -fsSL https://ollama.com/install.sh | sh
mkdir -p /etc/systemd/system/ollama.service.d
cat >/etc/systemd/system/ollama.service.d/limits.conf <<'UNIT'
[Service]
AllowedCPUs=6-9
CPUQuota=400%
CPUWeight=20
MemoryHigh=8G
MemoryMax=9G
IOWeight=20
Environment="OLLAMA_HOST=127.0.0.1:11434"
Environment="OLLAMA_MAX_LOADED_MODELS=1"
Environment="OLLAMA_NUM_PARALLEL=1"
Environment="OLLAMA_KEEP_ALIVE=-1"
Environment="OLLAMA_CONTEXT_LENGTH=2048"
Environment="OLLAMA_FLASH_ATTENTION=1"
UNIT
systemctl daemon-reload
systemctl restart ollama
for _ in $(seq 30); do curl -sf localhost:11434/api/tags >/dev/null && break; sleep 1; done
ollama list | grep -q "^$BASE " || ollama pull "$BASE"
printf 'FROM %s\nPARAMETER num_ctx 2048\n' "$BASE" >/tmp/tier1.Modelfile
ollama create "$MODEL" -f /tmp/tier1.Modelfile >/dev/null
systemctl show ollama -p AllowedCPUs -p CPUQuotaPerSecUSec -p CPUWeight -p MemoryMax
