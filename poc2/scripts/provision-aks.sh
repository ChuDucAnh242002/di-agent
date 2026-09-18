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
TF_BACKEND_MODE="${TF_BACKEND_MODE:-local}"
TFSTATE_RESOURCE_GROUP="${TFSTATE_RESOURCE_GROUP:-}"
TFSTATE_STORAGE_ACCOUNT="${TFSTATE_STORAGE_ACCOUNT:-}"
TFSTATE_CONTAINER="${TFSTATE_CONTAINER:-}"
TFSTATE_KEY="${TFSTATE_KEY:-poc2-aks.tfstate}"
TERRAFORM_DIR="$TF_DIR"
LOCAL_TF_DIR=""

command -v az >/dev/null 2>&1 || { err "az CLI not found. See https://learn.microsoft.com/cli/azure/install-azure-cli"; exit 1; }
command -v terraform >/dev/null 2>&1 || { err "terraform not found"; exit 1; }
az account show >/dev/null 2>&1 || { err "Not logged in to Azure. Run: az login"; exit 1; }

info "Initializing Terraform with '$TF_BACKEND_MODE' state ..."
case "$TF_BACKEND_MODE" in
	local)
		LOCAL_TF_DIR=$(mktemp -d)
		trap 'rm -rf "$LOCAL_TF_DIR"' EXIT
		cp "$TF_DIR"/*.tf "$LOCAL_TF_DIR"/
		# The checked-in module declares an Azure Storage backend for CI.
		# Local mode uses the same configuration with Terraform's local backend.
		sed -i 's|backend "azurerm" {}|backend "local" { path = "'"$TF_DIR"'/terraform.tfstate" }|' "$LOCAL_TF_DIR/providers.tf"
		TERRAFORM_DIR="$LOCAL_TF_DIR"
		(cd "$TERRAFORM_DIR" && terraform init -reconfigure -input=false)
		;;
	remote)
		if [ -z "$TFSTATE_RESOURCE_GROUP" ] || [ -z "$TFSTATE_STORAGE_ACCOUNT" ] || [ -z "$TFSTATE_CONTAINER" ]; then
			err "Remote state requires TFSTATE_RESOURCE_GROUP, TFSTATE_STORAGE_ACCOUNT, and TFSTATE_CONTAINER"
			exit 1
		fi
		(cd "$TERRAFORM_DIR" && terraform init -reconfigure -input=false \
			-backend-config="resource_group_name=$TFSTATE_RESOURCE_GROUP" \
			-backend-config="storage_account_name=$TFSTATE_STORAGE_ACCOUNT" \
			-backend-config="container_name=$TFSTATE_CONTAINER" \
			-backend-config="key=$TFSTATE_KEY" \
			-backend-config="use_azuread_auth=true")
		;;
	*)
		err "TF_BACKEND_MODE must be 'local' or 'remote'"
		exit 1
		;;
esac

info "Running terraform apply in $TF_DIR ..."
(cd "$TERRAFORM_DIR" && terraform apply -auto-approve)

RG=$(cd "$TERRAFORM_DIR" && terraform output -raw resource_group_name)
CLUSTER=$(cd "$TERRAFORM_DIR" && terraform output -raw cluster_name)
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
