#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import {
  chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync,
  rmSync, writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createInterface } from "node:readline/promises";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const parent = dirname(root);
const state = join(root, ".devbox", "workspace");
const kubeconfig = join(state, "kubeconfig");
const marker = join(state, "control-plane-id");
const cluster = "devbox-workspace";
const repos = {
  "di-agent": { path: root, url: "https://github.com/ChuDucAnh242002/di-agent.git" },
  "life-pension": { path: join(parent, "Life-Pension-workspace"), url: "https://github.com/ChuDucAnh242002/Life-Pension-workspace.git" },
  "sme-cashflow": { path: join(parent, "SME-Cash-Flow-Copilot"), url: "https://github.com/ChuDucAnh242002/SME-Cash-Flow-Copilot.git" },
};

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd || root,
    env: options.env || process.env,
    encoding: "utf8",
    stdio: options.capture ? ["ignore", "pipe", "pipe"] : "inherit",
  });
  if (result.error || result.status !== 0) {
    const detail = result.error?.message || result.stderr?.trim() || `exit ${result.status}`;
    throw new Error(`${command} ${args.join(" ")} failed: ${detail}`);
  }
  return result.stdout?.trim() || "";
}

function requireTools(names) {
  for (const name of names) {
    const result = spawnSync("sh", ["-c", 'command -v "$1" >/dev/null', "sh", name]);
    if (result.status !== 0) throw new Error(`Missing ${name}; install it and rerun setup.`);
  }
}

function clusterExists() {
  return run("kind", ["get", "clusters"], { capture: true }).split("\n").includes(cluster);
}

function controlPlaneId() {
  return run("docker", ["inspect", "--format", "{{.Id}}", `${cluster}-control-plane`], { capture: true });
}

function verifyCluster() {
  requireTools(["docker", "kind", "kubectl"]);
  if (!clusterExists() || !existsSync(marker) || !existsSync(kubeconfig)) {
    throw new Error("Dedicated kind cluster is not initialized. Run devbox run setup.");
  }
  if (readFileSync(marker, "utf8").trim() !== controlPlaneId()) {
    throw new Error("The kind cluster no longer matches the one created by setup; refusing to deploy.");
  }
  const config = run("kind", ["get", "kubeconfig", "--name", cluster], { capture: true });
  writeFileSync(kubeconfig, config + "\n", { mode: 0o600 });
  chmodSync(kubeconfig, 0o600);
  run("kubectl", ["--kubeconfig", kubeconfig, "cluster-info"], { capture: true });
  kubectl(["wait", "--for=condition=Ready", `node/${cluster}-worker`, "--timeout=120s"]);
}

function kubectl(args, options = {}) {
  return run("kubectl", ["--kubeconfig", kubeconfig, ...args], options);
}

async function setup() {
  requireTools(["git", "docker", "kind", "kubectl", "helm"]);
  run("docker", ["info"], { capture: true });
  console.log("Docker, kind, kubectl, Helm and Git are available. Docker access grants host-level privileges.");
  console.log("No cloud account is required for the local kind cluster. Telepresence is optional.");
  if (clusterExists()) {
    verifyCluster();
    console.log(`Using the previously created ${cluster} cluster.`);
    return;
  }
  if (existsSync(marker)) throw new Error("A previous cluster is missing. Remove its workspace state only after reviewing any retained data.");
  if (!process.stdin.isTTY) throw new Error("Setup requires an interactive terminal to confirm cluster creation.");
  const prompt = createInterface({ input: process.stdin, output: process.stdout });
  let answer;
  try {
    answer = await prompt.question(`Create dedicated kind cluster ${cluster} (one control plane and one worker)? [y/N] `);
  } finally {
    prompt.close();
  }
  if (!/^y(es)?$/i.test(answer.trim())) throw new Error("Cluster creation cancelled.");
  mkdirSync(state, { recursive: true, mode: 0o700 });
  chmodSync(state, 0o700);
  const config = join(state, "kind.yaml");
  writeFileSync(config, "kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n  - role: control-plane\n  - role: worker\n", { mode: 0o600 });
  run("kind", ["create", "cluster", "--name", cluster, "--config", config, "--kubeconfig", kubeconfig, "--wait", "180s"]);
  chmodSync(kubeconfig, 0o600);
  writeFileSync(marker, controlPlaneId() + "\n", { mode: 0o600, flag: "wx" });
  console.log("Cluster created. No production cluster or current kubectl context was changed.");
}

function pull() {
  requireTools(["git"]);
  let skipped = false;
  for (const [name, repo] of Object.entries(repos)) {
    if (!existsSync(repo.path)) {
      run("git", ["clone", "--branch", "main", "--single-branch", repo.url, repo.path]);
      continue;
    }
    const actualRoot = run("git", ["rev-parse", "--show-toplevel"], { cwd: repo.path, capture: true });
    if (resolve(actualRoot) !== repo.path) throw new Error(`${repo.path} is not the expected repository root.`);
    const remote = run("git", ["remote", "get-url", "origin"], { cwd: repo.path, capture: true });
    const expected = repo.url.replace("https://github.com/", "git@github.com:");
    if (![repo.url, repo.url.slice(0, -4), expected, expected.slice(0, -4)].includes(remote)) {
      throw new Error(`${name} has an unexpected origin; refusing to update it.`);
    }
    const dirty = run("git", ["status", "--porcelain"], { cwd: repo.path, capture: true });
    const branch = run("git", ["branch", "--show-current"], { cwd: repo.path, capture: true });
    if (dirty || branch !== "main") {
      console.error(`Skipped ${name}: checkout must be clean and on main.`);
      skipped = true;
      continue;
    }
    run("git", ["pull", "--ff-only", "origin", "main"], { cwd: repo.path });
  }
  if (skipped) throw new Error("Some repositories were skipped; review their branches and local changes.");
}

function requiredRepo(name) {
  const repo = repos[name];
  if (!repo || !existsSync(join(repo.path, "devbox.json"))) {
    throw new Error(`Repository ${name} is missing; run devbox run pull from di-agent.`);
  }
  return repo.path;
}

function loadImage(image) {
  run("kind", ["load", "docker-image", image, "--name", cluster]);
}

function buildImage(path, image) {
  run("docker", ["build", "-t", image, path]);
  loadImage(image);
}

function deployAgent(tag) {
  requireTools(["devbox", "helm"]);
  if (process.arch !== "x64") throw new Error("The di-agent kind build currently supports x86-64 hosts only.");
  const agent = requiredRepo("di-agent");
  const temporary = mkdtempSync(join(tmpdir(), "devbox-agent-build-"));
  try {
    mkdirSync(join(temporary, "go", "cmd", "agent", "tmp"), { recursive: true });
    copyFileSync(join(agent, "semantic-map", "Dockerfile"), join(temporary, "Dockerfile"));
    copyFileSync(join(agent, "semantic-map", "domain_spec.json"), join(temporary, "domain_spec.json"));
    copyFileSync(join(agent, "semantic-map", "go", "entrypoint.sh"), join(temporary, "go", "entrypoint.sh"));
    run("devbox", ["run", "build-agent", join(temporary, "go", "cmd", "agent", "tmp", "di-agent-poc")], { cwd: agent });
    buildImage(temporary, `devbox-local/di-agent:${tag}`);
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
  for (const name of ["dataset-collector", "telemetry-writer"]) {
    buildImage(join(agent, "poc2", "system", name), `devbox-local/${name}:${tag}`);
  }
  run("helm", [
    "upgrade", "--install", "di-agent-system", join(agent, "poc2/helm/di-agent-system"),
    "--kubeconfig", kubeconfig, "--namespace", "devbox-di-agent", "--create-namespace",
    "--set", "global.imageRegistry=devbox-local", "--set", `global.imageTag=${tag}`,
    "--set", "global.imagePullPolicy=IfNotPresent", "--set", "global.forceRollout=false", "--set", "postgres.enabled=false",
    "--set", "streamer.enabled=false", "--set", "kafka.persistence.enabled=false",
    "--set", "influxdb.persistence.enabled=false", "--set", "prometheus.persistence.enabled=false",
    "--wait", "--timeout", "8m",
  ]);
  kubectl(["-n", "devbox-di-agent", "rollout", "status", "daemonset/di-agent", "--timeout=180s"]);
  const daemonset = JSON.parse(kubectl(["-n", "devbox-di-agent", "get", "daemonset", "di-agent", "-o", "json"], { capture: true }));
  if (!daemonset.status?.desiredNumberScheduled || daemonset.status.numberReady !== daemonset.status.desiredNumberScheduled) {
    throw new Error("The agent DaemonSet has no ready workers.");
  }
}

function deployAgentService(name, tag) {
  requireTools(["helm"]);
  const agent = requiredRepo("di-agent");
  const setting = name === "dataset-collector" ? "datasetCollector" : "telemetryWriter";
  buildImage(join(agent, "poc2", "system", name), `devbox-local/${name}:${tag}`);
  run("helm", [
    "upgrade", "di-agent-system", join(agent, "poc2/helm/di-agent-system"),
    "--kubeconfig", kubeconfig, "--namespace", "devbox-di-agent", "--reuse-values",
    "--set", `${setting}.image.tag=${tag}`, "--wait", "--timeout", "8m",
  ]);
  kubectl(["-n", "devbox-di-agent", "rollout", "status", `deployment/${name}`, "--timeout=180s"]);
}

function ensureLifeSecret() {
  const ns = "life-pension-demo";
  if (kubectl(["-n", ns, "get", "secret", "workspace-auth", "--ignore-not-found", "-o", "name"], { capture: true })) return;
  if (kubectl(["-n", ns, "get", "pvc", "workspace-data", "--ignore-not-found", "-o", "name"], { capture: true })) {
    throw new Error("workspace-auth is missing but the H2 volume exists; restore its original database credential.");
  }
  const dir = join(state, "life-secrets");
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  chmodSync(dir, 0o700);
  for (const name of ["adviser-password", "database-password"]) {
    const path = join(dir, name);
    if (!existsSync(path)) writeFileSync(path, randomBytes(24).toString("hex"), { mode: 0o600, flag: "wx" });
  }
  kubectl(["-n", ns, "create", "secret", "generic", "workspace-auth",
    `--from-file=adviser-password=${join(dir, "adviser-password")}`,
    `--from-file=database-password=${join(dir, "database-password")}`]);
  console.log(`Private demo credentials are stored in ${dir}; do not share them.`);
}

function deployFinance(name, tag) {
  const path = requiredRepo(name);
  const life = name === "life-pension";
  const image = life ? `life-pension-workspace:${tag}` : `sme-cashflow-recruiter-demo:${tag}`;
  const expected = life ? "life-pension-workspace:step6" : "sme-cashflow-recruiter-demo:0.1.0";
  const overlay = join(path, life ? "deploy/kubernetes/demo" : "deploy/k8s-demo");
  const namespace = life ? "life-pension-demo" : "sme-cashflow";
  buildImage(path, image);
  const rendered = kubectl(["kustomize", overlay], { capture: true });
  if (!rendered.includes(`image: ${expected}`)) throw new Error(`Expected image ${expected} not found in rendered manifests.`);
  const temporary = mkdtempSync(join(tmpdir(), "devbox-manifests-"));
  try {
    const manifest = join(temporary, "rendered.yaml");
    writeFileSync(manifest, rendered.replace(`image: ${expected}`, `image: ${image}`), { mode: 0o600 });
    if (life) {
      kubectl(["apply", "-f", join(overlay, "namespace.yaml")]);
      ensureLifeSecret();
    }
    kubectl(["apply", "-f", manifest]);
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
  if (!life) {
    for (const service of ["statefulset/postgres", "statefulset/kafka"]) {
      kubectl(["-n", namespace, "rollout", "status", service, "--timeout=300s"]);
    }
  }
  kubectl(["-n", namespace, "rollout", "status", `deployment/${life ? "workspace" : "copilot"}`, "--timeout=300s"]);
}

function deploy(name) {
  const agentServices = ["dataset-collector", "telemetry-writer"];
  if (!Object.hasOwn(repos, name) && !agentServices.includes(name)) {
    throw new Error(`Unknown service ${name}. Choose: ${[...Object.keys(repos), ...agentServices].join(", ")}.`);
  }
  verifyCluster();
  const tag = `dev-${Date.now()}-${randomBytes(3).toString("hex")}`;
  console.log(`Deploying ${name} to the dedicated ${cluster} cluster (synthetic data only).`);
  if (name === "di-agent") deployAgent(tag);
  else if (agentServices.includes(name)) deployAgentService(name, tag);
  else deployFinance(name, tag);
}

async function main(args) {
  const [command, ...rest] = args;
  if (command === "setup" && rest.length === 0) await setup();
  else if (command === "pull" && rest.length === 0) pull();
  else if (command === "deploy" && rest.length === 1) deploy(rest[0]);
  else if (command === "deploy-all" && rest.length === 0) {
    for (const name of Object.keys(repos)) requiredRepo(name);
    for (const name of Object.keys(repos)) deploy(name);
  } else {
    throw new Error("Usage: devbox run setup | pull | deploy-all | deploy <di-agent|life-pension|sme-cashflow|dataset-collector|telemetry-writer>");
  }
}

main(process.argv.slice(2)).catch((error) => {
  console.error(`devbox workspace: ${error.message}`);
  process.exitCode = 1;
});
