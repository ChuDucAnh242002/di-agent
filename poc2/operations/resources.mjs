export const namespace = 'platform-ops';

export function resources(agentImage) {
  const items = [{ apiVersion: 'v1', kind: 'Namespace', metadata: { name: namespace } }];
  const config = (name, data) => items.push({
    apiVersion: 'v1', kind: 'ConfigMap', metadata: { name, namespace }, data,
  });
  const workload = (name, image, port, args = [], mounts = [], extra = {}) => {
    items.push({
      apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name, namespace },
      spec: {
        replicas: 1, revisionHistoryLimit: 3, selector: { matchLabels: { app: name } },
        template: {
          metadata: { labels: { app: name } },
          spec: {
            automountServiceAccountToken: false,
            securityContext: { runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, fsGroup: 1000 },
            containers: [{
              name, image, args, imagePullPolicy: name === 'agent' ? 'Never' : 'IfNotPresent',
              ports: [{ containerPort: port }],
              readinessProbe: { tcpSocket: { port }, periodSeconds: 2 },
              resources: { requests: { cpu: '25m', memory: '32Mi' }, limits: { cpu: '500m', memory: '256Mi' } },
              securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ['ALL'] } },
              volumeMounts: mounts.map(([name, mountPath]) => ({ name, mountPath })),
              ...extra,
            }],
            volumes: mounts.map(([name]) => name === 'data' || name === 'tmp'
              ? { name, emptyDir: {} } : { name, configMap: { name } }),
          },
        },
      },
    }, {
      apiVersion: 'v1', kind: 'Service', metadata: { name, namespace },
      spec: { selector: { app: name }, ports: [{ port, targetPort: port }] },
    });
  };
  config('blackbox', { 'blackbox.yml': `modules:
  health:
    prober: http
    timeout: 3s
    http:
      preferred_ip_protocol: ip4
      fail_if_body_not_matches_regexp: ['"ok"\\s*:\\s*true']
` });
  config('prometheus', { 'prometheus.yml': `global:
  scrape_interval: 5s
  evaluation_interval: 5s
rule_files: [/etc/prometheus/rules.yml]
alerting:
  alertmanagers:
    - static_configs:
        - targets: ['alertmanager:9093']
scrape_configs:
  - job_name: agent-health
    metrics_path: /probe
    params:
      module: [health]
    static_configs:
      - targets: ['http://agent:9090/healthz']
    relabel_configs:
      - source_labels: [__address__]
        target_label: __param_target
      - source_labels: [__param_target]
        target_label: instance
      - target_label: __address__
        replacement: blackbox:9115
`, 'rules.yml': `groups:
  - name: agent-health
    rules:
      - alert: AgentUnavailable
        expr: probe_success{job="agent-health"} == 0
        for: 15s
        labels:
          severity: critical
        annotations:
          summary: Agent health probe failed
          action: Check agent Deployment readiness and logs; restore replicas or roll back the last rollout.
      - alert: ProbeTelemetryMissing
        expr: absent(probe_success{job="agent-health"}) or up{job="agent-health"} == 0
        for: 30s
        labels:
          severity: warning
        annotations:
          summary: Health monitoring telemetry is missing
          action: Check the blackbox exporter and Prometheus scrape target before judging service health.
      - alert: AgentHealthSlow
        expr: probe_duration_seconds{job="agent-health"} > 1
        for: 30s
        labels:
          severity: warning
        annotations:
          summary: Agent health probe exceeds one second
          action: Inspect agent resource pressure and service networking.
` });
  config('alertmanager', { 'alertmanager.yml': `route:
  receiver: lab-webhook
  group_by: [alertname]
  group_wait: 1s
  group_interval: 5s
  repeat_interval: 1h
receivers:
  - name: lab-webhook
    webhook_configs:
      - url: http://receiver:8080/alerts
        send_resolved: true
` });
  config('receiver', { 'receiver.mjs': `import http from 'node:http';
const received = [];
http.createServer(async (req, res) => {
  if (req.method === 'GET' && req.url === '/alerts') {
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify(received));
    return;
  }
  if (req.method !== 'POST' || req.url !== '/alerts') { res.writeHead(404).end(); return; }
  let body = '';
  for await (const chunk of req) {
    body += chunk;
    if (Buffer.byteLength(body) > 65536) { res.writeHead(413).end(); return; }
  }
  try {
    const alert = JSON.parse(body);
    if (!Array.isArray(alert.alerts)) { res.writeHead(400).end(); return; }
    received.push(alert);
    if (received.length > 100) received.shift();
    console.log(JSON.stringify(alert));
    res.writeHead(200).end();
  } catch (error) {
    if (!(error instanceof SyntaxError)) throw error;
    console.error('Rejected invalid webhook JSON');
    res.writeHead(400).end();
  }
}).listen(8080, '0.0.0.0');
` });
  config('grafana-provisioning', {
    'datasource.yml': 'apiVersion: 1\ndatasources:\n  - name: Prometheus\n    uid: prometheus\n    type: prometheus\n    url: http://prometheus:9090\n    access: proxy\n',
    'dashboard.yml': 'apiVersion: 1\nproviders:\n  - name: operations\n    options:\n      path: /dashboards\n',
  });
  const panels = [
    ['Availability (probe success)', 'probe_success{job="agent-health"}'],
    ['Health-check latency (seconds)', 'probe_duration_seconds{job="agent-health"}'],
    ['Failed probe ratio (5 minutes)', '1 - avg_over_time(probe_success{job="agent-health"}[5m])'],
    ['HTTP status (0 means no response)', 'probe_http_status_code{job="agent-health"}'],
  ].map(([title, expr], index) => ({
    id: index + 1, title, type: 'timeseries', datasource: { type: 'prometheus', uid: 'prometheus' },
    targets: [{ expr, refId: 'A' }], gridPos: { x: index % 2 * 12, y: Math.floor(index / 2) * 8, w: 12, h: 8 },
  }));
  config('grafana-dashboard', { 'operations.json': JSON.stringify({
    uid: 'platform-ops', title: 'Agent service health (local lab)', schemaVersion: 39,
    refresh: '5s', time: { from: 'now-15m', to: 'now' }, panels,
  }) });
  workload('agent', agentImage, 9090, [], [['data', '/var/lib/di-agent'], ['tmp', '/tmp']], {
    env: Object.entries({
      NODE_ID: 'operations-lab', NETDATA_URL: '', PROPOSER: 'false',
      REVIEW_LEVEL: 'rules', DECISION_BACKEND: 'none', REASONING_BACKEND: 'none',
    }).map(([name, value]) => ({ name, value })),
    readinessProbe: { httpGet: { path: '/healthz', port: 9090 }, periodSeconds: 2 },
  });
  workload('blackbox', 'prom/blackbox-exporter:v0.29.1', 9115,
    ['--config.file=/etc/blackbox/blackbox.yml'], [['blackbox', '/etc/blackbox']]);
  workload('prometheus', 'prom/prometheus:v3.16.0', 9090,
    ['--config.file=/etc/prometheus/prometheus.yml', '--storage.tsdb.path=/prometheus', '--storage.tsdb.retention.time=1h'],
    [['prometheus', '/etc/prometheus'], ['data', '/prometheus']]);
  workload('alertmanager', 'prom/alertmanager:v0.35.0', 9093,
    ['--config.file=/etc/alertmanager/alertmanager.yml', '--storage.path=/alertmanager', '--cluster.listen-address='],
    [['alertmanager', '/etc/alertmanager'], ['data', '/alertmanager']]);
  workload('receiver', 'node:24.22.1-bookworm', 8080, ['/app/receiver.mjs'], [['receiver', '/app']]);
  workload('grafana', 'grafana/grafana:13.3.1', 3000, [], [
    ['data', '/var/lib/grafana'], ['grafana-dashboard', '/dashboards'],
    ['grafana-provisioning', '/etc/lab-provisioning'],
  ], {
    env: [
      { name: 'GF_AUTH_ANONYMOUS_ENABLED', value: 'true' },
      { name: 'GF_AUTH_ANONYMOUS_ORG_ROLE', value: 'Viewer' },
      { name: 'GF_AUTH_BASIC_ENABLED', value: 'false' },
      { name: 'GF_AUTH_DISABLE_LOGIN_FORM', value: 'true' },
      { name: 'GF_PATHS_PROVISIONING', value: '/tmp/provisioning' },
    ],
    command: ['/bin/sh', '-ec'],
    args: [
      'mkdir -p /tmp/provisioning/datasources /tmp/provisioning/dashboards; ' +
      'cp /etc/lab-provisioning/datasource.yml /tmp/provisioning/datasources/; ' +
      'cp /etc/lab-provisioning/dashboard.yml /tmp/provisioning/dashboards/; exec /run.sh',
    ],
  });
  return { apiVersion: 'v1', kind: 'List', items };
}
