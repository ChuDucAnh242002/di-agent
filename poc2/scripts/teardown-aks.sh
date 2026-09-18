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
TF_BACKEND_MODE="${TF_BACKEND_MODE:-local}"
TFSTATE_RESOURCE_GROUP="${TFSTATE_RESOURCE_GROUP:-}"
TFSTATE_STORAGE_ACCOUNT="${TFSTATE_STORAGE_ACCOUNT:-}"
TFSTATE_CONTAINER="${TFSTATE_CONTAINER:-}"
TFSTATE_KEY="${TFSTATE_KEY:-poc2-aks.tfstate}"
TERRAFORM_DIR="$TF_DIR"
LOCAL_TF_DIR=""

info "Initializing Terraform with '$TF_BACKEND_MODE' state ..."
case "$TF_BACKEND_MODE" in
	local)
		LOCAL_TF_DIR=$(mktemp -d)
		trap 'rm -rf "$LOCAL_TF_DIR"' EXIT
		cp "$TF_DIR"/*.tf "$LOCAL_TF_DIR"/
		sed -i 's|backend "azurerm" {}|backend "local" { path = "'"$TF_DIR"'/terraform.tfstate" }|' "$LOCAL_TF_DIR/providers.tf"
		TERRAFORM_DIR="$LOCAL_TF_DIR"
		(cd "$TERRAFORM_DIR" && terraform init -reconfigure -input=false)
		;;
	remote)
		if [ -z "$TFSTATE_RESOURCE_GROUP" ] || [ -z "$TFSTATE_STORAGE_ACCOUNT" ] || [ -z "$TFSTATE_CONTAINER" ]; then
			echo "[aks] Remote state requires TFSTATE_RESOURCE_GROUP, TFSTATE_STORAGE_ACCOUNT, and TFSTATE_CONTAINER" >&2
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
		echo "[aks] TF_BACKEND_MODE must be 'local' or 'remote'" >&2
		exit 1
		;;
esac

info "Running terraform destroy in $TERRAFORM_DIR ..."
(cd "$TERRAFORM_DIR" && terraform destroy -auto-approve)
ok "AKS cluster and resource group destroyed."
