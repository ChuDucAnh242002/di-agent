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
TF_BACKEND_MODE="${TF_BACKEND_MODE:-local}"
TFSTATE_BUCKET="${TFSTATE_BUCKET:-}"
TFSTATE_KEY="${TFSTATE_KEY:-poc2-eks.tfstate}"
TFSTATE_REGION="${TFSTATE_REGION:-${TF_VAR_region:-${AWS_REGION:-${AWS_DEFAULT_REGION:-}}}}"
TFSTATE_DYNAMODB_TABLE="${TFSTATE_DYNAMODB_TABLE:-}"
TERRAFORM_DIR="$TF_DIR"
LOCAL_TF_DIR=""
BACKEND_ARGS=()

info "Initializing Terraform with '$TF_BACKEND_MODE' state ..."
case "$TF_BACKEND_MODE" in
	local)
		LOCAL_TF_DIR=$(mktemp -d)
		trap 'rm -rf "$LOCAL_TF_DIR"' EXIT
		cp "$TF_DIR"/*.tf "$LOCAL_TF_DIR"/
		sed -i 's|backend "s3" {}|backend "local" { path = "'"$TF_DIR"'/terraform.tfstate" }|' "$LOCAL_TF_DIR/providers.tf"
		TERRAFORM_DIR="$LOCAL_TF_DIR"
		(cd "$TERRAFORM_DIR" && terraform init -reconfigure -input=false)
		;;
	remote)
		if [ -z "$TFSTATE_BUCKET" ] || [ -z "$TFSTATE_KEY" ] || [ -z "$TFSTATE_REGION" ]; then
			echo "[eks] Remote state requires TFSTATE_BUCKET, TFSTATE_KEY, and TFSTATE_REGION" >&2
			exit 1
		fi
		BACKEND_ARGS=(
			"-backend-config=bucket=$TFSTATE_BUCKET"
			"-backend-config=key=$TFSTATE_KEY"
			"-backend-config=region=$TFSTATE_REGION"
		)
		if [ -n "$TFSTATE_DYNAMODB_TABLE" ]; then
			BACKEND_ARGS+=("-backend-config=dynamodb_table=$TFSTATE_DYNAMODB_TABLE")
		fi
		(cd "$TERRAFORM_DIR" && terraform init -reconfigure -input=false "${BACKEND_ARGS[@]}")
		;;
	*)
		echo "[eks] TF_BACKEND_MODE must be 'local' or 'remote'" >&2
		exit 1
		;;
esac

info "Running terraform destroy in $TERRAFORM_DIR ..."
(cd "$TERRAFORM_DIR" && terraform destroy -auto-approve)
ok "EKS cluster, node group and VPC destroyed."
