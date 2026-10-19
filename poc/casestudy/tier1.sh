#!/usr/bin/env bash
# tier1.sh — the agent's own decision model on the node, held to fixed limits so it cannot
# take what the workloads need. Runs on the node, as root; provision.sh calls it.
#
# A node without a model of its own is not autonomous: the review loop's tier 1 runs here.
# Only the reasoning model, which a node this size cannot hold, is reached over the network.
#
# Limits (systemd drop-in, the ollama unit), each set by what the 2026-10-19 runs showed:
#   AllowedCPUs=6-9, CPUQuota=400%   the four CPUs node-cpus.sh keeps from every pod, and no
#                                    more. On two CPUs a review took 1-2 min and hit the agent's
#                                    2 min timeout; on these four, 40-80 s.
#   CPUWeight=20, IOWeight=20        below a pod's default 100
#   MemoryMax=9G, no MemoryHigh      6.3 GB resident (4.6 GB of weights and the decision
#                                    runner's buffers) and ~2.3 GB of page cache from reading the
#                                    model once. A MemoryHigh below that throttles reclaim of the
#                                    cache and stalls every review; the hard cap alone bounds it.
#   model tev1:4b-node               tev1:4b with num_ctx 2048: a review prompt is ~800 tokens,
#                                    and the base model's num_ctx of 8192 (6.4 GB) overrides
#                                    OLLAMA_CONTEXT_LENGTH
#   OLLAMA_MAX_LOADED_MODELS=1, OLLAMA_NUM_PARALLEL=1, OLLAMA_KEEP_ALIVE=-1
#                                    one model, one request at a time, kept loaded
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
MemoryMax=9G
IOWeight=20
Environment="OLLAMA_HOST=127.0.0.1:11434"
Environment="OLLAMA_MAX_LOADED_MODELS=1"
Environment="OLLAMA_NUM_PARALLEL=1"
Environment="OLLAMA_KEEP_ALIVE=-1"
Environment="OLLAMA_FLASH_ATTENTION=1"
UNIT
systemctl daemon-reload
systemctl restart ollama
for _ in $(seq 30); do curl -sf localhost:11434/api/tags >/dev/null && break; sleep 1; done
ollama list | grep -q "^$BASE " || ollama pull "$BASE"
printf 'FROM %s\nPARAMETER num_ctx 2048\n' "$BASE" >/tmp/tier1.Modelfile
ollama create "$MODEL" -f /tmp/tier1.Modelfile >/dev/null
systemctl show ollama -p AllowedCPUs -p CPUQuotaPerSecUSec -p CPUWeight -p MemoryMax
