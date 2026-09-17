#!/usr/bin/env bash
# agent-cloud.sh — build+push the di-agent image and deploy it as a
#                  DaemonSet (one pod per cluster node) against whatever
#                  cluster $KUBECONFIG currently points at (AKS, EKS, or
#                  any other Kubernetes cluster).
#
# This is the cloud-agnostic counterpart to scripts/03-agent.sh, which
# instead does a per-VM Deployment + hostNetwork + containerd image import
# tailored to the local libvirt lab. On a managed cluster the set of nodes
# isn't known ahead of time and there's no way to import images directly
# into each node's runtime, so this variant pushes to a registry and lets
# a DaemonSet self-schedule one pod per node.
#
# Usage: REGISTRY=ghcr.io/myorg TAG=v1 ./agent-cloud.sh
#   SKIP_BUILD=1 skips the build+push step entirely and deploys the image
#   already at REGISTRY/di-agent:TAG as-is — use this in CD pipelines that
#   built and pushed an immutable, tested image earlier in the same run.

set -euo pipefail

GREEN=$(tput setaf 2 2>/dev/null || echo "")
YELLOW=$(tput setaf 3 2>/dev/null || echo "")
RED=$(tput setaf 1 2>/dev/null || echo "")
RESET=$(tput sgr0 2>/dev/null || echo "")

info() { echo "${YELLOW}[agent-cloud] $*${RESET}"; }
ok()   { echo "${GREEN}[agent-cloud] $*${RESET}"; }
err()  { echo "${RED}[agent-cloud] $*${RESET}" >&2; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POC_DIR="$(dirname "$SCRIPT_DIR")"
SEM_DIR="$(dirname "$POC_DIR")/semantic-map"
GO_SRC="$SEM_DIR/go"
BINARY_OUT="./cmd/agent/tmp/di-agent-poc"
BUILD_GOOS="${BUILD_GOOS:-linux}"
BUILD_GOARCH="${BUILD_GOARCH:-amd64}"

REGISTRY="${REGISTRY:-ghcr.io/chuducanh242002}"
TAG="${TAG:-latest}"
IMAGE="${REGISTRY}/di-agent:${TAG}"
SKIP_BUILD="${SKIP_BUILD:-0}"

NAMESPACE="${NAMESPACE:-default}"
KAFKA_NAMESPACE="${KAFKA_NAMESPACE:-default}"
KAFKA_BROKERS="${KAFKA_BROKERS:-kafka.${KAFKA_NAMESPACE}.svc.cluster.local:9092}"
REGIME="${REGIME:-stable}"

command -v kubectl >/dev/null 2>&1 || { err "kubectl not found"; exit 1; }
kubectl cluster-info >/dev/null 2>&1 || { err "kubectl cannot reach a cluster; set KUBECONFIG first"; exit 1; }

if [ "$SKIP_BUILD" = "1" ]; then
    info "SKIP_BUILD=1: deploying pre-built image ${IMAGE} without rebuilding"
else
    # ── build ─────────────────────────────────────────────────────────────────
    info "Building di-agent for ${BUILD_GOOS}/${BUILD_GOARCH} from $GO_SRC ..."
    [ -d "$GO_SRC" ] || { err "Go source directory not found: $GO_SRC"; exit 1; }
    (
        cd "$GO_SRC"
        mkdir -p "$(dirname "$BINARY_OUT")"
        GOOS="$BUILD_GOOS" GOARCH="$BUILD_GOARCH" go build -o "$BINARY_OUT" ./cmd/agent/
    )
    ok "Binary built"

    # ── build + push image ────────────────────────────────────────────────────
    info "Building and pushing ${IMAGE} ..."
    ( cd "$SEM_DIR" && docker build -t "$IMAGE" . && docker push "$IMAGE" )
    ok "Image pushed: $IMAGE"
fi

# ── deploy ────────────────────────────────────────────────────────────────────
info "Applying di-agent DaemonSet + headless Service to namespace $NAMESPACE ..."
cat <<EOF | kubectl apply -n "$NAMESPACE" -f -
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: di-agent
  labels:
    app: di-agent
spec:
  selector:
    matchLabels:
      app: di-agent
  template:
    metadata:
      labels:
        app: di-agent
    spec:
      containers:
        - name: di-agent
          image: ${IMAGE}
          imagePullPolicy: Always
          env:
            - name: NODE_ID
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
            - name: REGIME
              value: "${REGIME}"
            - name: KAFKA_BROKERS
              value: "${KAFKA_BROKERS}"
          ports:
            - containerPort: 9090
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 250m
              memory: 256Mi
---
apiVersion: v1
kind: Service
metadata:
  name: di-agent-headless
  labels:
    app: di-agent
spec:
  clusterIP: None
  selector:
    app: di-agent
  ports:
    - port: 9090
      targetPort: 9090
EOF

info "Waiting for di-agent pods to be Ready ..."
kubectl -n "$NAMESPACE" wait --for=condition=Ready pod -l app=di-agent --timeout=180s
kubectl -n "$NAMESPACE" get pods -o wide -l app=di-agent
ok "All di-agent pods Ready"
