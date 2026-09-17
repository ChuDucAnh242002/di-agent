#!/usr/bin/env bash
# provision-aks.sh — terraform apply the cloud/aks module and fetch its
#                    kubeconfig locally.
#
# Requires: az CLI logged in (`az login`), Terraform >= 1.5.
# Usage: ./provision-aks.sh
#   Override any cloud/aks variable via TF_VAR_<name>, e.g.
#   TF_VAR_node_count=5 TF_VAR_location=westeurope ./provision-aks.sh

set -euo pipefail

GREEN=$(tput setaf 2 2>/dev/null || echo "")
YELLOW=$(tput setaf 3 2>/dev/null || echo "")
RED=$(tput setaf 1 2>/dev/null || echo "")
RESET=$(tput sgr0 2>/dev/null || echo "")

info() { echo "${YELLOW}[aks] $*${RESET}"; }
ok()   { echo "${GREEN}[aks] $*${RESET}"; }
err()  { echo "${RED}[aks] $*${RESET}" >&2; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TF_DIR="$(dirname "$SCRIPT_DIR")/cloud/aks"
LOCAL_KUBECONFIG="${LOCAL_KUBECONFIG:-$HOME/.kube/config-poc2-aks}"

command -v az >/dev/null 2>&1 || { err "az CLI not found. See https://learn.microsoft.com/cli/azure/install-azure-cli"; exit 1; }
command -v terraform >/dev/null 2>&1 || { err "terraform not found"; exit 1; }
az account show >/dev/null 2>&1 || { err "Not logged in to Azure. Run: az login"; exit 1; }

info "Running terraform apply in $TF_DIR ..."
(cd "$TF_DIR" && terraform init -input=false && terraform apply -auto-approve)

RG=$(cd "$TF_DIR" && terraform output -raw resource_group_name)
CLUSTER=$(cd "$TF_DIR" && terraform output -raw cluster_name)
ok "AKS cluster ready: $CLUSTER (resource group $RG)"

info "Fetching kubeconfig ..."
az aks get-credentials --resource-group "$RG" --name "$CLUSTER" --file "$LOCAL_KUBECONFIG" --overwrite-existing
chmod 600 "$LOCAL_KUBECONFIG"
ok "Kubeconfig saved to $LOCAL_KUBECONFIG"

echo ""
echo "To use this cluster:"
echo "  export KUBECONFIG=$LOCAL_KUBECONFIG"
echo ""
echo "Then proceed with the same app-layer steps as the local PoC:"
echo "  make images helm-install REGISTRY=ghcr.io/your-org TAG=v1"
echo "  make agent-cloud peers-cloud REGISTRY=ghcr.io/your-org TAG=v1"
