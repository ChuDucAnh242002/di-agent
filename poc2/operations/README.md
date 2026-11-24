# Disposable local operations exercise

This is a local Kubernetes lab, not a cloud deployment or production monitoring.
It builds the real Go agent, deploys it to a new single-node kind cluster, and
measures its `/healthz` endpoint with Blackbox Exporter and Prometheus.
It does not change the main Helm chart, existing clusters, or cloud accounts.

## Run

From the repository root, with the Go version in `semantic-map/go/go.mod`,
Node.js 22+, Docker, kind and kubectl on `PATH`:

```sh
node poc2/operations/lab.mjs
```

Devbox supplies Go and Node.js; Docker/kind/kubectl are host prerequisites.
Allow several GiB of RAM and disk and time to pull the versioned images.
The same exercise runs in GitHub Actions for lab PR changes or manual dispatch.

The runner:

1. Builds the agent image and tags it with its Docker content ID, not `latest`.
2. Creates a uniquely named kind cluster with its own temporary kubeconfig.
3. Bootstraps its namespace and prints `kubectl diff` before applying workloads.
4. Waits for application-level HTTP readiness and verifies live availability, health-check latency,
   HTTP status samples and the provisioned four-panel Grafana dashboard.
5. Scales down only the lab agent, verifies **AgentUnavailable** actually reaches
   the Alertmanager webhook, restores the agent and verifies a resolved event.
6. Injects a bad readiness path, verifies rollout cannot finish, then rolls back
   and checks the restored image and probe.
7. Removes its cluster and API proxy even after a failure, and verifies deletion.

Kubernetes services remain ClusterIP; the temporary API proxy binds to loopback
on an automatically allocated port. Pods have resource limits, run without root,
drop Linux capabilities and do not receive service-account tokens. No cloud
credentials, model credentials, privileged pods, host mounts or ingress are used.
The built agent image and downloaded Docker layers remain cached for reruns;
no global Docker pruning is performed.

## Metrics and alert limits

The Grafana dashboard is provisioned in the lab, then deleted during teardown.
Its metrics are **synthetic HTTP health probes**, not end-user request latency,
application error rate, Kafka backlog, disk pressure or an uptime SLO. A failed
probe ratio includes connection failures; HTTP status 0 means no response.
`ProbeTelemetryMissing` distinguishes exporter/scrape loss from healthy service,
and `AgentHealthSlow` flags probes slower than one second.

Prometheus routes to Alertmanager, which sends firing and resolved notifications
to a lab-only webhook receiver. The runner asserts delivery; receiver logs include
the alert's suggested action. This sink is not an email, pager or GitHub issue
integration. Use the cross-repo dashboard for GitHub CI incident issues.
No external notification credentials are placed in Kubernetes.

Grafana update checks, reporting and automatic plugin preinstallation are
disabled in this lab. Dashboard provisioning is checked with a bounded wait;
transient startup HTTP failures/timeouts are printed and retried, not treated
as a healthy or provisioned dashboard.

All data is ephemeral. Grafana permits anonymous **Viewer** access only for this
private disposable lab; never reuse these settings on a public endpoint.
This exercise validates rollback of a stateless workload, not database migration,
data restoration, cloud IAM, Terraform apply, or production disaster recovery.
