#!/usr/bin/env bash
# provision-eks.sh — terraform apply the cloud/eks module and fetch its
#                    kubeconfig locally.
#
# Requires: aws CLI authenticated (`aws sts get-caller-identity` works),
#           Terraform >= 1.5.
# Usage: ./provision-eks.sh
#   Override any cloud/eks variable via TF_VAR_<name>, e.g.
#   TF_VAR_node_count=5 TF_VAR_region=eu-west-1 ./provision-eks.sh

set -euo pipefail

GREEN=$(tput setaf 2 2>/dev/null || echo "")
YELLOW=$(tput setaf 3 2>/dev/null || echo "")
RED=$(tput setaf 1 2>/dev/null || echo "")
RESET=$(tput sgr0 2>/dev/null || echo "")

info() { echo "${YELLOW}[eks] $*${RESET}"; }
ok()   { echo "${GREEN}[eks] $*${RESET}"; }
err()  { echo "${RED}[eks] $*${RESET}" >&2; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TF_DIR="$(dirname "$SCRIPT_DIR")/cloud/eks"
LOCAL_KUBECONFIG="${LOCAL_KUBECONFIG:-$HOME/.kube/config-poc2-eks}"

command -v aws >/dev/null 2>&1 || { err "aws CLI not found"; exit 1; }
command -v terraform >/dev/null 2>&1 || { err "terraform not found"; exit 1; }
aws sts get-caller-identity >/dev/null 2>&1 || { err "Not authenticated with AWS. Run: aws configure (or aws sso login)"; exit 1; }

info "Running terraform apply in $TF_DIR ..."
(cd "$TF_DIR" && terraform init -input=false && terraform apply -auto-approve)

CLUSTER=$(cd "$TF_DIR" && terraform output -raw cluster_name)
REGION=$(cd "$TF_DIR" && terraform output -raw region)
ok "EKS cluster ready: $CLUSTER ($REGION)"

info "Fetching kubeconfig ..."
aws eks update-kubeconfig --region "$REGION" --name "$CLUSTER" --kubeconfig "$LOCAL_KUBECONFIG"
chmod 600 "$LOCAL_KUBECONFIG"
ok "Kubeconfig saved to $LOCAL_KUBECONFIG"

echo ""
echo "To use this cluster:"
echo "  export KUBECONFIG=$LOCAL_KUBECONFIG"
echo ""
echo "Then proceed with the same app-layer steps as the local PoC:"
echo "  make images helm-install REGISTRY=ghcr.io/your-org TAG=v1"
echo "  make agent-cloud peers-cloud REGISTRY=ghcr.io/your-org TAG=v1"
