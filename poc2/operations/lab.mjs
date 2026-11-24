import { execFileSync, spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';
import { namespace, resources } from './resources.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const directory = await mkdtemp(join(tmpdir(), 'platform-ops-'));
const cluster = `platform-ops-${randomUUID().slice(0, 8)}`;
const kubeconfig = join(directory, 'kubeconfig');
const manifest = join(directory, 'resources.json');
const run = (command, args, options = {}) => execFileSync(command, args, {
  cwd: root, encoding: 'utf8', stdio: 'inherit', timeout: 600000, ...options,
});
const kubectl = (...args) => run('kubectl', ['--kubeconfig', kubeconfig, '-n', namespace, ...args]);
const capture = (...args) => run('kubectl', ['--kubeconfig', kubeconfig, '-n', namespace, ...args], { stdio: 'pipe' });
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
let created = false;
let attempted = false;
let forwarding;
let forwardExit;
let stoppingForward = false;

async function until(description, check, seconds = 120) {
  const deadline = Date.now() + seconds * 1000;
  while (Date.now() < deadline) {
    if (forwardExit && !stoppingForward) throw new Error(`Port forwarding exited: ${forwardExit}`);
    if (await check()) { console.log(`PASS: ${description}`); return; }
    await sleep(2000);
  }
  throw new Error(`Timed out: ${description}`);
}

async function json(url) {
  const response = await fetch(url, { signal: AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error(`HTTP ${response.status}: ${url}`);
  return response.json();
}

try {
  for (const command of ['go', 'docker', 'kind']) run(command, ['version']);
  run('kubectl', ['version', '--client']);
  run('go', ['build', '-o', 'cmd/agent/tmp/di-agent-poc', './cmd/agent'], { cwd: join(root, 'semantic-map/go') });
  const imageFile = join(directory, 'image-id');
  run('docker', ['build', '--iidfile', imageFile, 'semantic-map']);
  const image = `platform/di-agent:${(await readFile(imageFile, 'utf8')).trim().replace('sha256:', '')}`;
  run('docker', ['tag', (await readFile(imageFile, 'utf8')).trim(), image]);
  attempted = true;
  run('kind', ['create', 'cluster', '--name', cluster, '--image', 'kindest/node:v1.36.1',
    '--kubeconfig', kubeconfig, '--wait', '120s']);
  created = true;
  run('kind', ['load', 'docker-image', image, '--name', cluster]);
  await writeFile(manifest, JSON.stringify(resources(image)));
  kubectl('create', 'namespace', namespace);
  // A diff is a reviewable plan, not an apply. kubectl uses exit 1 for differences.
  try { kubectl('diff', '-f', manifest); } catch (error) { if (error.status !== 1) throw error; }
  kubectl('apply', '-f', manifest);
  for (const name of ['agent', 'blackbox', 'prometheus', 'alertmanager', 'receiver', 'grafana']) {
    kubectl('rollout', 'status', `deployment/${name}`, '--timeout=240s');
  }
  // The API proxy keeps every service private and binds only loopback, on a free port.
  forwarding = spawn('kubectl', ['--kubeconfig', kubeconfig, 'proxy', '--address=127.0.0.1', '--port=0'], {
    cwd: root, stdio: ['ignore', 'pipe', 'pipe'],
  });
  const base = await new Promise((resolve, reject) => {
    let output = '';
    const timeout = setTimeout(() => reject(new Error('API proxy did not start')), 15000);
    forwarding.on('error', error => { clearTimeout(timeout); reject(error); });
    forwarding.on('exit', (code, signal) => {
      forwardExit = `${code ?? signal}`;
      clearTimeout(timeout);
      reject(new Error(`API proxy exited: ${forwardExit}`));
    });
    forwarding.stderr.on('data', chunk => process.stderr.write(chunk));
    forwarding.stdout.on('data', chunk => {
      output += chunk;
      const match = output.match(/Starting to serve on 127\.0\.0\.1:(\d+)/);
      if (match) { clearTimeout(timeout); resolve(`http://127.0.0.1:${match[1]}`); }
    });
  });
  const service = (name, port, path) => `${base}/api/v1/namespaces/${namespace}/services/${name}:${port}/proxy${path}`;
  const query = async expr => {
    const data = await json(service('prometheus', 9090, `/api/v1/query?query=${encodeURIComponent(expr)}`));
    assert.equal(data.status, 'success');
    return data.data.result;
  };
  const events = () => json(service('receiver', 8080, '/alerts'));
  const observed = (events, status) => events.some(event =>
    event.alerts.some(alert => alert.labels.alertname === 'AgentUnavailable' && alert.status === status));
  await until('healthy service is measured', async () => {
    const results = await query('probe_success{job="agent-health"}');
    return results.length === 1 && results[0].value[1] === '1';
  });
  for (const metric of ['probe_duration_seconds', 'probe_http_status_code']) {
    assert.equal((await query(`${metric}{job="agent-health"}`)).length, 1);
  }
  const dashboard = await json(service('grafana', 3000, '/api/dashboards/uid/platform-ops'));
  assert.equal(dashboard.dashboard.panels.length, 4);
  console.log('PASS: Grafana dashboard provisioned; health latency and HTTP status have real samples.');

  kubectl('scale', 'deployment/agent', '--replicas=0');
  await until('availability alert delivered through Alertmanager webhook', async () => observed(await events(), 'firing'));
  kubectl('scale', 'deployment/agent', '--replicas=1');
  kubectl('rollout', 'status', 'deployment/agent', '--timeout=120s');
  await until('recovery notification delivered', async () => observed(await events(), 'resolved'));

  // An invalid readiness path simulates a bad release without fetching an unknown image.
  kubectl('patch', 'deployment/agent', '--type=strategic', '-p', JSON.stringify({
    spec: { template: { spec: { containers: [{
      name: 'agent', readinessProbe: { httpGet: { path: '/deliberately-invalid-health' } },
    }] } } },
  }));
  let failedRollout = false;
  try { capture('rollout', 'status', 'deployment/agent', '--timeout=20s'); } catch (error) {
    if (error.status !== 1 || !String(error.stderr).includes('timed out waiting for the condition')) throw error;
    failedRollout = true;
  }
  assert.equal(failedRollout, true, 'Bad readiness must prevent rollout completion');
  kubectl('rollout', 'undo', 'deployment/agent');
  kubectl('rollout', 'status', 'deployment/agent', '--timeout=120s');
  const deployment = JSON.parse(capture('get', 'deployment/agent', '-o', 'json'));
  assert.equal(deployment.spec.template.spec.containers[0].image, image);
  assert.equal(deployment.spec.template.spec.containers[0].readinessProbe.httpGet.path, '/healthz');
  console.log('PASS: failed rollout blocked; rollback restored the versioned image and health probe.');
  console.log(JSON.stringify({ cluster, image, alertEvents: (await events()).map(event => ({
    status: event.status, alerts: event.alerts.map(alert => alert.labels.alertname),
  })) }, null, 2));
} catch (error) {
  console.error('Operations exercise failed:', error.message);
  if (created) {
    try {
      kubectl('get', 'pods', '-o', 'wide');
      kubectl('get', 'events', '--sort-by=.lastTimestamp');
      kubectl('logs', '-l', 'app', '--all-containers=true', '--tail=15', '--max-log-requests=10');
    } catch (diagnosticError) {
      console.error('Could not collect all diagnostics:', diagnosticError.message);
    }
  }
  throw error;
} finally {
  if (forwarding) {
    stoppingForward = true;
    if (forwarding.exitCode === null && forwarding.signalCode === null) {
      await new Promise(resolve => {
        forwarding.once('exit', resolve);
        forwarding.kill('SIGTERM');
      });
    }
  }
  // kind also cleans up partially created clusters, never the user's current context.
  if (attempted) {
    run('kind', ['delete', 'cluster', '--name', cluster, '--kubeconfig', kubeconfig]);
    const clusters = run('kind', ['get', 'clusters'], { stdio: 'pipe' }).split(/\s+/);
    assert.ok(!clusters.includes(cluster), 'Disposable cluster must be removed');
  }
  await rm(directory, { recursive: true, force: true });
  console.log(`PASS: ${created ? 'created' : 'partially created'} cluster removed; no cloud resources or external ports created.`);
}
