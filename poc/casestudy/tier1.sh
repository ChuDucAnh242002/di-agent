#!/usr/bin/env bash
# tier1.sh — the agent's own decision model on the node, held to a fixed band of resources so
# it cannot take what the workloads need, nor move in a way the map would read as the node's
# behaviour. Runs on the node, as root; provision.sh calls it.
#
# A node without a model of its own is not autonomous, so tier 1 — rules, then this decision
# model — runs here. A reasoning model (tier 2) is optional and off: on this 16 GB node a
# local qwen3:8b beside it ran Ollama past its cap, qwen3:4b with tools still did, and loading
# one model at a time swung node memory by gigabytes and buried a 400 MB workload's wave
# (runs 2026-10-20e to h). A reasoning tier needs a larger node or a peer, and may return.
#
# Limits (systemd drop-in, the ollama unit):
#   AllowedCPUs=6-11, CPUQuota=600%  the six CPUs node-cpus.sh keeps from every pod, and no more.
#                                    On two CPUs a review took 1-2 min against the agent's timeout.
#   CPUWeight=20, IOWeight=20        below a pod's default 100
#   MemoryMin=6G, MemoryMax=9G       a floor and a ceiling: tev1:4b-node is 6.2 GB resident and
#   (no MemoryHigh)                  ~2.3 GB of page cache from reading it once; the floor reserves
#                                    it, so its memory is a dedicated amount and a constant offset in
#                                    the node's, and the cap bounds it. A MemoryHigh below its size
#                                    throttles reclaim and stalls every review.
#   model tev1:4b-node               tev1:4b with num_ctx 2048 (a review is ~800 tokens; the base
#                                    model's own 8192, 6.4 GB, overrides OLLAMA_CONTEXT_LENGTH) and
#                                    num_thread 6, one per CPU it has (by default its runner threads
#                                    outnumber them and it waits on itself)
#   OLLAMA_MAX_LOADED_MODELS=1, OLLAMA_NUM_PARALLEL=1, OLLAMA_KEEP_ALIVE=-1
#                                    one model, kept loaded, one request at a time
# Ollama listens on 127.0.0.1:11434 only.
set -euo pipefail
DECIDE="${1:-tev1:4b}"
command -v ollama >/dev/null || curl -fsSL https://ollama.com/install.sh | sh
mkdir -p /etc/systemd/system/ollama.service.d
cat >/etc/systemd/system/ollama.service.d/limits.conf <<'UNIT'
[Service]
AllowedCPUs=6-11
CPUQuota=600%
CPUWeight=20
MemoryMin=6G
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
node_model() { # node_model <base> <num_ctx>: <base>-node, pulled if absent
    ollama list | grep -q "^$1 " || ollama pull "$1"
    printf 'FROM %s\nPARAMETER num_ctx %s\nPARAMETER num_thread 6\n' "$1" "$2" >/tmp/tier1.Modelfile
    ollama create "$1-node" -f /tmp/tier1.Modelfile >/dev/null
}
node_model "$DECIDE" 2048
systemctl show ollama -p AllowedCPUs -p CPUQuotaPerSecUSec -p CPUWeight -p MemoryMax
