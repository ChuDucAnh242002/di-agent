import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

const source = fileURLToPath(new URL("./devbox-workspace.mjs", import.meta.url));

function fixture(t) {
  const dir = mkdtempSync(join(tmpdir(), "devbox-workspace-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const root = join(dir, "di-agent");
  const scripts = join(root, ".github", "scripts");
  const state = join(root, ".devbox", "workspace");
  const bin = join(dir, "bin");
  mkdirSync(scripts, { recursive: true });
  mkdirSync(state, { recursive: true });
  mkdirSync(bin);
  copyFileSync(source, join(scripts, "devbox-workspace.mjs"));
  writeFileSync(join(state, "control-plane-id"), "expected-id\n");
  writeFileSync(join(state, "kubeconfig"), "old-config\n");
  for (const repo of ["di-agent", "Life-Pension-workspace", "SME-Cash-Flow-Copilot"]) {
    mkdirSync(join(dir, repo), { recursive: true });
    writeFileSync(join(dir, repo, "devbox.json"), "{}");
  }
  const fake = join(bin, "fake");
  writeFileSync(fake, `#!/bin/sh
echo "$(basename "$0") $*" >> "$CALLS"
case "$(basename "$0"):$*" in
  "kind:get clusters") echo devbox-workspace ;;
  "kind:get kubeconfig --name devbox-workspace") echo private-test-kubeconfig ;;
  "docker:inspect "*) echo "\${CONTAINER_ID:-expected-id}" ;;
  *"kustomize "*deploy/k8s-demo*) echo '        image: sme-cashflow-recruiter-demo:0.1.0' ;;
  *"kustomize "*) echo '        image: life-pension-workspace:step6' ;;
  *"get pvc workspace-data"*) [ "\${PVC_EXISTS:-0}" = 1 ] && echo persistentvolumeclaim/workspace-data ;;
  *"apply -f "*rendered.yaml) cp "\${@: -1}" "$APPLIED" ;;
  "git:rev-parse --show-toplevel") pwd ;;
  "git:remote get-url origin") echo "https://github.com/ChuDucAnh242002/$(basename "$PWD").git" ;;
  "git:status --porcelain") [ "\${DIRTY_REPO:-}" = "$(basename "$PWD")" ] && echo ' M changed' ;;
  "git:branch --show-current") [ "\${DETACHED_REPO:-}" = "$(basename "$PWD")" ] || echo main ;;
esac
exit 0
`.replace('cp "${@: -1}" "$APPLIED"', 'for arg in "$@"; do last="$arg"; done; cp "$last" "$APPLIED"'), { mode: 0o755 });
  for (const name of ["docker", "kind", "kubectl", "helm", "devbox", "git"]) {
    symlinkSync(fake, join(bin, name));
  }
  const calls = join(dir, "calls");
  const env = { ...process.env, PATH: `${bin}:/usr/bin:/bin`, CALLS: calls, APPLIED: join(dir, "applied") };
  const invoke = (...args) => spawnSync(process.execPath, [join(scripts, "devbox-workspace.mjs"), ...args], { env, encoding: "utf8" });
  return { dir, root, state, calls, env, invoke };
}

test("unknown services fail before invoking tools", (t) => {
  const f = fixture(t);
  const result = f.invoke("deploy", "unknown");
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Unknown service/);
  assert.throws(() => readFileSync(f.calls));
});

test("a replaced cluster is rejected before any deployment", (t) => {
  const f = fixture(t);
  f.env.CONTAINER_ID = "different-id";
  const result = f.invoke("deploy", "life-pension");
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /no longer matches/);
  assert.doesNotMatch(readFileSync(f.calls, "utf8"), /build|apply/);
});

test("deploy-all checks for all repositories before changing the cluster", (t) => {
  const f = fixture(t);
  rmSync(join(f.dir, "SME-Cash-Flow-Copilot"), { recursive: true });
  const result = f.invoke("deploy-all");
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /sme-cashflow is missing/);
  assert.throws(() => readFileSync(f.calls));
});

test("life demo loads a unique image and creates private credentials", (t) => {
  const f = fixture(t);
  const result = f.invoke("deploy", "life-pension");
  assert.equal(result.status, 0, result.stderr);
  const calls = readFileSync(f.calls, "utf8");
  assert.match(calls, /kind load docker-image life-pension-workspace:dev-/);
  assert.match(calls, /create secret generic workspace-auth/);
  assert.match(readFileSync(join(f.dir, "applied"), "utf8"), /image: life-pension-workspace:dev-/);
  assert.equal(readFileSync(join(f.state, "life-secrets", "database-password"), "utf8").length, 48);
  assert.doesNotMatch(result.stdout, /[a-f0-9]{48}/);
});

test("life demo refuses to replace a missing secret when its data volume exists", (t) => {
  const f = fixture(t);
  f.env.PVC_EXISTS = "1";
  const result = f.invoke("deploy", "life-pension");
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /restore its original database credential/);
  assert.doesNotMatch(readFileSync(f.calls, "utf8"), /create secret|rendered\.yaml/);
});

test("SME demo uses the synthetic overlay and waits for both dependencies", (t) => {
  const f = fixture(t);
  const result = f.invoke("deploy", "sme-cashflow");
  assert.equal(result.status, 0, result.stderr);
  const calls = readFileSync(f.calls, "utf8");
  assert.match(calls, /kustomize .*deploy\/k8s-demo/);
  assert.match(calls, /rollout status statefulset\/postgres/);
  assert.match(calls, /rollout status statefulset\/kafka/);
  assert.match(readFileSync(join(f.dir, "applied"), "utf8"), /image: sme-cashflow-recruiter-demo:dev-/);
});

test("a single collector update retains Helm release values", (t) => {
  const f = fixture(t);
  const result = f.invoke("deploy", "dataset-collector");
  assert.equal(result.status, 0, result.stderr);
  const calls = readFileSync(f.calls, "utf8");
  assert.match(calls, /helm upgrade di-agent-system .*--reuse-values --set datasetCollector\.image\.tag=dev-/);
  assert.match(calls, /rollout status deployment\/dataset-collector/);
  assert.doesNotMatch(calls, /--install/);
});

test("pull skips dirty and detached repositories without switching branches", (t) => {
  const f = fixture(t);
  f.env.DIRTY_REPO = "Life-Pension-workspace";
  f.env.DETACHED_REPO = "SME-Cash-Flow-Copilot";
  const result = f.invoke("pull");
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Skipped life-pension/);
  assert.match(result.stderr, /Skipped sme-cashflow/);
  const calls = readFileSync(f.calls, "utf8");
  assert.equal((calls.match(/git pull --ff-only origin main/g) || []).length, 1);
  assert.doesNotMatch(calls, /git (switch|checkout|reset)/);
});
