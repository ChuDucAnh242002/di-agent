#!/usr/bin/env bash
# teardown-eks.sh — terraform destroy the cloud/eks module.

set -euo pipefail

GREEN=$(tput setaf 2 2>/dev/null || echo "")
YELLOW=$(tput setaf 3 2>/dev/null || echo "")
RESET=$(tput sgr0 2>/dev/null || echo "")

info() { echo "${YELLOW}[eks] $*${RESET}"; }
ok()   { echo "${GREEN}[eks] $*${RESET}"; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TF_DIR="$(dirname "$SCRIPT_DIR")/cloud/eks"

info "Running terraform destroy in $TF_DIR ..."
(cd "$TF_DIR" && terraform destroy -auto-approve)
ok "EKS cluster, node group and VPC destroyed."
