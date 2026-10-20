#!/usr/bin/env bash
# tier1.sh — the agent's own models on the node, held to fixed limits so they cannot take
# what the workloads need. Runs on the node, as root; provision.sh calls it.
#
# A node without models of its own is not autonomous, so both review tiers run here: the
# decision model (tier 1) and a reasoning model with tools (tier 2). A larger reasoning
# model on a peer or across the network is optional (loops.review.reasoning.url); the
# case study used the host's qwen3.8:27b once and dropped it — it ran the host out of GPU
# memory mid-review — and may return to one later.
#
# Limits (systemd drop-in, the ollama unit), each set by what the 2026-10-19/20 runs showed:
#   AllowedCPUs=6-11, CPUQuota=600%  the six CPUs node-cpus.sh keeps from every pod, and no
#                                    more. On two CPUs a review took 1-2 min against the agent's
#                                    timeout; on four, 40-80 s.
#   CPUWeight=20, IOWeight=20        below a pod's default 100
#   MemoryMax=13G, no MemoryHigh     tier 1 ~6.3 GB resident, tier 2 ~6 GB, and the page cache of
#                                    reading each once. A MemoryHigh below that throttles reclaim
#                                    and stalls every review; the hard cap alone bounds it.
#   models <name>-node               each with num_thread 6, one per CPU it has (by default the
#                                    runner's threads outnumber them and it waits on itself), and a
#                                    context sized to its prompts: 2048 for tier 1 (a review is ~800
#                                    tokens; the base model's own 8192 overrides OLLAMA_CONTEXT_LENGTH),
#                                    8192 for tier 2, whose tool calls bring map state into the prompt
#   OLLAMA_MAX_LOADED_MODELS=2, OLLAMA_NUM_PARALLEL=1, OLLAMA_KEEP_ALIVE=-1
#                                    both tiers kept loaded, one request at a time
# Ollama listens on 127.0.0.1:11434 only.
set -euo pipefail
DECIDE="${1:-tev1:4b}"
REASON="${2:-qwen3:8b}"
command -v ollama >/dev/null || curl -fsSL https://ollama.com/install.sh | sh
mkdir -p /etc/systemd/system/ollama.service.d
cat >/etc/systemd/system/ollama.service.d/limits.conf <<'UNIT'
[Service]
AllowedCPUs=6-11
CPUQuota=600%
CPUWeight=20
MemoryMax=13G
IOWeight=20
Environment="OLLAMA_HOST=127.0.0.1:11434"
Environment="OLLAMA_MAX_LOADED_MODELS=2"
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
node_model "$REASON" 8192
systemctl show ollama -p AllowedCPUs -p CPUQuotaPerSecUSec -p CPUWeight -p MemoryMax
