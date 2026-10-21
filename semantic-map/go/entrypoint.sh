#!/bin/bash
set -e

# Read environment variables with defaults
NODE_ID=${NODE_ID:-"node1"}
REGIME=${REGIME:-"bursty"}
PORT=${PORT:-"9090"}
PROFILE=${PROFILE:-"edge-minimal"}
KD=${KD:-"k8s"}
COLLECT_INTERVAL=${COLLECT_INTERVAL:-"5s"}
PROPOSER=${PROPOSER:-"false"}
NETDATA_URL=${NETDATA_URL:-"http://localhost:19999"}
DOMAIN_SPEC=${DOMAIN_SPEC:-"/etc/di-agent/domain_spec.json"}
CGROUP_ROOT=${CGROUP_ROOT:-"/sys/fs/cgroup"}

echo "Starting di-agent with:"
echo "  NODE_ID=${NODE_ID}"
echo "  REGIME=${REGIME}"
echo "  PORT=${PORT}"
echo "  PROFILE=${PROFILE}"
echo "  KD=${KD}"
echo "  COLLECT_INTERVAL=${COLLECT_INTERVAL}"
echo "  PROPOSER=${PROPOSER}"
echo "  NETDATA_URL=${NETDATA_URL}"
echo "  DOMAIN_SPEC=${DOMAIN_SPEC}"

# The agent is described by a spec file; write this container's from the variables.
SPEC_FILE=${SPEC_FILE:-"/tmp/agent.yaml"}
cat > "${SPEC_FILE}" <<SPEC
node:
  addr: ":${PORT}"
  id: "${NODE_ID}"
profile:
  name: "${PROFILE}"
  kd: "${KD}"
  regime: "${REGIME}"
domain:
  path: "${DOMAIN_SPEC}"
sources:
  interval: ${COLLECT_INTERVAL}
  netdata:
    url: "${NETDATA_URL}"
map:
  proposer:
    enabled: ${PROPOSER}
SPEC

exec /usr/local/bin/di-agent -config "${SPEC_FILE}" "$@"
