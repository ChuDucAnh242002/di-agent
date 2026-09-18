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
TF_BACKEND_MODE="${TF_BACKEND_MODE:-local}"
TFSTATE_BUCKET="${TFSTATE_BUCKET:-}"
TFSTATE_KEY="${TFSTATE_KEY:-poc2-eks.tfstate}"
TFSTATE_REGION="${TFSTATE_REGION:-${TF_VAR_region:-${AWS_REGION:-${AWS_DEFAULT_REGION:-}}}}"
TFSTATE_DYNAMODB_TABLE="${TFSTATE_DYNAMODB_TABLE:-}"
TERRAFORM_DIR="$TF_DIR"
LOCAL_TF_DIR=""
BACKEND_ARGS=()

command -v aws >/dev/null 2>&1 || { err "aws CLI not found"; exit 1; }
command -v terraform >/dev/null 2>&1 || { err "terraform not found"; exit 1; }
aws sts get-caller-identity >/dev/null 2>&1 || { err "Not authenticated with AWS. Run: aws configure (or aws sso login)"; exit 1; }

info "Initializing Terraform with '$TF_BACKEND_MODE' state ..."
case "$TF_BACKEND_MODE" in
	local)
		LOCAL_TF_DIR=$(mktemp -d)
		trap 'rm -rf "$LOCAL_TF_DIR"' EXIT
		cp "$TF_DIR"/*.tf "$LOCAL_TF_DIR"/
		# The checked-in module declares an S3 backend for CI.
		# Local mode uses the same configuration with Terraform's local backend.
		sed -i 's|backend "s3" {}|backend "local" { path = "'"$TF_DIR"'/terraform.tfstate" }|' "$LOCAL_TF_DIR/providers.tf"
		TERRAFORM_DIR="$LOCAL_TF_DIR"
		(cd "$TERRAFORM_DIR" && terraform init -reconfigure -input=false)
		;;
	remote)
		if [ -z "$TFSTATE_BUCKET" ] || [ -z "$TFSTATE_KEY" ] || [ -z "$TFSTATE_REGION" ]; then
			err "Remote state requires TFSTATE_BUCKET, TFSTATE_KEY, and TFSTATE_REGION"
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
		err "TF_BACKEND_MODE must be 'local' or 'remote'"
		exit 1
		;;
esac

info "Running terraform apply in $TERRAFORM_DIR ..."
(cd "$TERRAFORM_DIR" && terraform apply -auto-approve)

CLUSTER=$(cd "$TERRAFORM_DIR" && terraform output -raw cluster_name)
REGION=$(cd "$TERRAFORM_DIR" && terraform output -raw region)
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
