#!/usr/bin/env bash
# 02-k0s.sh — install and start k0s in single-node mode on each VM
#
# Usage: ./02-k0s.sh [vm1 vm2 vm3]
#
# The version is pinned so a run can name it: K0S_VERSION overrides it.

set -euo pipefail

# ── colours ─────────────────────────────────────────────────────────────────
GREEN=$(tput setaf 2 2>/dev/null || echo "")
YELLOW=$(tput setaf 3 2>/dev/null || echo "")
RED=$(tput setaf 1 2>/dev/null || echo "")
RESET=$(tput sgr0 2>/dev/null || echo "")

info() { echo "${YELLOW}[k0s] $*${RESET}"; }
ok()   { echo "${GREEN}[k0s] $*${RESET}"; }
err()  { echo "${RED}[k0s] $*${RESET}" >&2; }

K0S_VERSION="${K0S_VERSION:-v1.36.4+k0s.1}"

# ── helpers ──────────────────────────────────────────────────────────────────
vm_ip() { multipass list | awk -v vm="$1" '$1 == vm {print $3}'; }

wait_k0s_ready() {
    local vm="$1"
    local timeout=300
    local elapsed=0
    info "Waiting for k0s node to reach Ready on $vm (timeout=${timeout}s) ..."
    while [ "$elapsed" -lt "$timeout" ]; do
        if multipass exec "$vm" -- sudo k0s kubectl get nodes 2>/dev/null \
            | grep -q " Ready"; then
            ok "k0s node Ready on $vm"
            return 0
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
    err "Timed out waiting for k0s on $vm"
    return 1
}

# ── args ─────────────────────────────────────────────────────────────────────
if [ "$#" -eq 0 ]; then
    VMS=(diag-1 diag-2 diag-3)
else
    VMS=("$@")
fi

# ── install + start ──────────────────────────────────────────────────────────
for vm in "${VMS[@]}"; do
    info "Installing k0s $K0S_VERSION on $vm ..."
    multipass exec "$vm" -- bash -c \
        "curl -sSLf https://get.k0s.sh | sudo K0S_VERSION='$K0S_VERSION' sh"
    installed=$(multipass exec "$vm" -- sudo k0s version)
    if [ "$installed" != "$K0S_VERSION" ]; then
        err "$vm has k0s $installed, expected $K0S_VERSION"
        exit 1
    fi
    ok "k0s $installed installed on $vm"

    info "Installing k0s controller (single-node) on $vm ..."
    # Idempotent: ignore error if already installed
    multipass exec "$vm" -- bash -c \
        "sudo k0s install controller --single 2>/dev/null || true"

    info "Starting k0s on $vm ..."
    multipass exec "$vm" -- bash -c \
        "sudo k0s start 2>/dev/null || sudo systemctl start k0scontroller || true"

    wait_k0s_ready "$vm"
done

echo ""
ok "k0s installed and running on all VMs."
