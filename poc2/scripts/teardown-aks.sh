#!/usr/bin/env bash
# teardown-aks.sh — terraform destroy the cloud/aks module.

set -euo pipefail

GREEN=$(tput setaf 2 2>/dev/null || echo "")
YELLOW=$(tput setaf 3 2>/dev/null || echo "")
RESET=$(tput sgr0 2>/dev/null || echo "")

info() { echo "${YELLOW}[aks] $*${RESET}"; }
ok()   { echo "${GREEN}[aks] $*${RESET}"; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TF_DIR="$(dirname "$SCRIPT_DIR")/cloud/aks"

info "Running terraform destroy in $TF_DIR ..."
(cd "$TF_DIR" && terraform destroy -auto-approve)
ok "AKS cluster and resource group destroyed."
