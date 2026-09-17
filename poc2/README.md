# PoC2: local di-agent lab on libvirt VMs

This directory is the second proof-of-concept for the di-agent project: a small Kubernetes lab that runs on libvirt virtual machines, with a distributed telemetry stack and a trust-based coordination experiment.

The important thing to understand is that PoC2 is not just a cluster bootstrap. It is a reproducible environment for testing a specific thesis:

- multiple agent instances run on different machines,
- each node observes a different workload pattern,
- the nodes exchange peer trust information,
- a coordinator observes how routing recommendations change when trust drops.

## What is actually required to run this PoC

This is the minimal working stack the project expects:

- Linux host with libvirt + KVM + QEMU enabled
- Terraform installed and configured for libvirt
- Docker for building the custom images used by the app stack
- Helm 3 installed
- kubectl and a valid kubeconfig after cluster bootstrap
- SSH access to the VMs, using the key at `$HOME/.ssh/id_ed25519_vms`
- a container registry for the chart-managed services (`ghcr.io/...` by default)
- access to Ubuntu cloud images so Terraform can create VM disks

The default topology is simple and intentional:

- `ubuntu-vm1` = Kubernetes control plane
- `ubuntu-vm2`, `ubuntu-vm3`, ... = worker nodes

The first VM in the list is the control plane; all remaining VMs are treated as workers.

## Deploying on AKS or EKS instead of libvirt

The Helm chart (`helm/di-agent-system`) and the `di-agent` peer mesh don't
depend on libvirt at all — they only need a working `kubectl`/`KUBECONFIG`
against *some* Kubernetes cluster. `cloud/aks` and `cloud/eks` are Terraform
modules that stand up a managed cluster as a drop-in replacement for
`main.tf` + `scripts/02-k8s.sh`:

```bash
# Azure (requires `az login`)
make provision-aks
export KUBECONFIG=$HOME/.kube/config-poc2-aks

# or AWS (requires `aws configure` / `aws sso login`)
make provision-eks
export KUBECONFIG=$HOME/.kube/config-poc2-eks

# then the usual app-layer steps, unchanged:
make images helm-install REGISTRY=ghcr.io/your-org TAG=v1
make agent-cloud peers-cloud REGISTRY=ghcr.io/your-org TAG=v1
make demo-cloud

# teardown:
make teardown-aks   # or teardown-eks
```

The differences from the local flow are confined to the infrastructure and
agent-placement layers:

- `provision-aks` / `provision-eks` replace `provision` + `k8s`: they create
  the cluster directly (AKS/EKS) instead of libvirt VMs + kubeadm, and fetch
  its kubeconfig to `~/.kube/config-poc2-aks` or `-eks`.
- `agent-cloud` replaces `agent`: since managed clusters don't have a fixed,
  known set of node hostnames, di-agent runs as a `DaemonSet` (one pod per
  node, image pulled from `REGISTRY` rather than imported into containerd)
  instead of one `Deployment` per VM pinned by `nodeSelector`.
- `peers-cloud` / `demo-cloud` replace `peers` / `demo`: pod IPs on AKS/EKS
  live inside the cluster's private VNet/VPC and aren't reachable from your
  laptop, so these scripts use `kubectl port-forward` to reach each agent
  pod, while still registering each other's real in-cluster pod IP as the
  peer URL (agent-to-agent traffic stays on the cluster network).
- `helm-install` and everything under `helm/di-agent-system` (Kafka,
  InfluxDB, Grafana, workload simulators, telemetry) is unchanged — it only
  needs `KUBECONFIG` pointed at the right cluster and the same
  `influxdb-credentials`/`grafana-credentials` secrets described below.

See `cloud/aks/variables.tf` and `cloud/eks/variables.tf` for what's
configurable (region/location, node count, VM/instance size); override any
of them with `TF_VAR_<name>` before running `provision-aks`/`provision-eks`.

## What this PoC deploys

The deployment consists of two separate layers:

1. Infrastructure and cluster layer
   - VM creation via Terraform in `main.tf`
   - Kubernetes bootstrap via `scripts/02-k8s.sh`
   - one cluster for the VM fleet

2. Application and demo layer
   - Helm chart in `helm/di-agent-system` deploys Kafka, InfluxDB, Grafana, genset, battery, propulsion, auxload, switchboard, telemetry-writer, and playground
   - `scripts/03-agent.sh` builds and deploys the Go `di-agent` binary as one pod per worker VM
   - `scripts/04-peers.sh` registers each agent as a peer of the others and sets trust values
   - `scripts/coordinator.sh` runs the trust-drain/routing experiment

The key point is that the `di-agent` pods are intentionally not part of the Helm chart. They are created separately, one pod per VM, to model host-level peer nodes directly on the VM network.

## Required secrets and configuration

Before the chart is installed, the chart expects a namespace and credentials for the telemetry services.

```bash
kubectl create namespace default --dry-run=client -o yaml | kubectl apply -f -
kubectl -n default create secret generic influxdb-credentials \
  --from-literal=admin-user="$INFLUXDB_ADMIN_USER" \
  --from-literal=admin-password="$INFLUXDB_ADMIN_PASSWORD" \
  --from-literal=admin-token="$INFLUXDB_ADMIN_TOKEN"
kubectl -n default create secret generic grafana-credentials \
  --from-literal=admin-user="$GRAFANA_ADMIN_USER" \
  --from-literal=admin-password="$GRAFANA_ADMIN_PASSWORD"
```

This is required because `helm/di-agent-system/values.yaml` references:

- `influxdb.existingSecret: influxdb-credentials`
- `grafana.existingSecret: grafana-credentials`

### Optional: cloud eventual-consistency sync

`telemetryCloudSync` mirrors telemetry Kafka topics to Azure (Event Hubs or IoT Hub), and `telemetryWriterCloud` is a second telemetry-writer that reads them back out of Event Hubs into a cloud-hosted InfluxDB, so the cloud store eventually converges with the edge one. Both are disabled by default. To enable them:

```bash
kubectl -n default create secret generic eventhub-credentials \
  --from-literal=connection-string="$EVENTHUB_CONNECTION_STRING"
kubectl -n default create secret generic influxdb-cloud-credentials \
  --from-literal=admin-token="$INFLUXDB_CLOUD_ADMIN_TOKEN"
```

then set `telemetryCloudSync.enabled=true`, `telemetryCloudSync.target=eventhub`, `telemetryWriterCloud.enabled=true`, and `telemetryWriterCloud.influxdb.url` to your cloud InfluxDB endpoint.

You should also set the registry and tag before installing the chart if you are not using the defaults:

```bash
make images REGISTRY=ghcr.io/your-org TAG=v1
make helm-install REGISTRY=ghcr.io/your-org TAG=v1
```

The chart also expects the build context directories under `system/` and `playground/` to exist and to be buildable with Docker.

## End-to-end workflow

Use this sequence from this directory:

```bash
make provision
make k8s
make images
make helm-install
make agent
make peers
make demo
```

That sequence does the following:

1. `make provision` creates the libvirt VMs.
2. `make k8s` bootstraps the cluster on the VMs.
3. `make images` builds and pushes the workload images used by the Helm chart.
4. `make helm-install` deploys Kafka, InfluxDB, Grafana, switchboard, genset, battery, propulsion, auxload, and the playground.
5. `make agent` builds the Go agent binary and imports the image into each worker node; it then deploys one `di-agent` pod per VM.
6. `make peers` registers each agent as a peer on the others and assigns trust values.
7. `make demo` executes the coordinator loop that probes `/cost`, inspects recommendations, and lowers trust to show rerouting.

## Useful commands

```bash
make status
make list-vms
make demo
make teardown
```

The trust-routing acceptance scenarios are in `bdd/features`. They exercise
the external agent HTTP contract with deterministic in-process peers, so they
do not require VMs, Kubernetes, or a running semantic-map agent:

```bash
make bdd
```

The scenarios cover the PoC's central behavior: selecting a trusted,
lower-cost peer, excluding a peer below the trust floor, and rejecting a
recommendation when no peer has sufficient trust. Cypress remains the test
surface for the playground UI, while these Gherkin scenarios cover the
cross-agent workflow.

The demo is the proof-of-value. It does the following:

- calls `/cost` on each agent,
- identifies the highest-cost node,
- asks that node for a recommendation,
- reduces trust on one peer mid-run,
- demonstrates that routing changes when trust crosses the effective threshold.

## Important project files

The files that matter for understanding or running the PoC are:

- `main.tf`: libvirt VM definition and disk layout
- `variables.tf`: VM and image configuration
- `providers.tf`: libvirt provider setup
- `cloud/aks`, `cloud/eks`: Terraform modules for managed-cluster alternatives to the above (see "Deploying on AKS or EKS" above)
- `scripts/01-provision.sh`: creates the VM fleet
- `scripts/02-k8s.sh`: bootstraps the cluster
- `scripts/03-agent.sh`: builds and deploys the `di-agent` pods (per-VM, local lab)
- `scripts/agent-cloud.sh`, `scripts/peers-cloud.sh`, `scripts/coordinator-cloud.sh`: DaemonSet-based di-agent deployment, peer registration, and demo for AKS/EKS
- `scripts/04-peers.sh`: registers peers and trust values
- `scripts/coordinator.sh`: runs the trust-based recommendation demo
- `scripts/build-push-images.sh`: builds/pushes the runtime service images
- `helm/di-agent-system/values.yaml`: all chart defaults and required secrets
- `helm/di-agent-system/templates/`: the actual Kubernetes manifests for Kafka, InfluxDB, Grafana, and simulation services
- `system/*`: workload generators and telemetry services

## What the system is proving

PoC2 is designed to validate a distributed coordination scenario in a controlled local environment:

- nodes start with similar priors,
- local workloads diverge,
- trust values diverge between peers,
- recommendations are no longer purely cost-driven,
- traffic shifts when a previously trusted peer loses trust.

This is an operational test bed for the di-agent concept, not a production-ready cluster design.

## Cleanup

To destroy the VM fleet and associated libvirt resources:

```bash
make teardown
```

This leaves the host in a clean state after the PoC is finished.
