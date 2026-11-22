import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { collect, parseDiagnosis, promptFor, render, publish, isNewer, skipReason } from "./ci-triage.mjs";

const report = {
  id: 123, attempt: 2, sha: "abc123", branch: "test", pullRequests: [5],
  url: "https://github.com/example/repo/actions/runs/123",
  failed: [{ name: "Go tests", url: "https://github.com/example/repo/actions/runs/123/job/7",
    conclusion: "failure", steps: ["Test (race detector)"] }],
};
const diagnosis = { likely_cause: "Insufficient evidence", suggested_next_steps: "Inspect the failed test logs", confidence: "low" };

test("prompt contains only failed-job metadata and output validation rejects unsupported claims", () => {
  const prompt = promptFor({ ...report, secret: "must-not-be-sent" });
  assert.ok(!prompt.includes("must-not-be-sent"));
  assert.ok(!prompt.includes(report.sha));
  assert.deepEqual(parseDiagnosis(JSON.stringify(diagnosis)), diagnosis);
  for (const bad of [{ ...diagnosis, confidence: "high" }, { ...diagnosis, extra: "field" },
    { ...diagnosis, likely_cause: "" }, { ...diagnosis, likely_cause: "x".repeat(1501) }, null]) {
    assert.throws(() => parseDiagnosis(JSON.stringify(bad)));
  }
  assert.throws(() => parseDiagnosis("not JSON"));
  assert.throws(() => promptFor({ ...report, failed: [{ name: "x".repeat(12000), steps: [] }] }),
    /12000-character/);
  assert.doesNotThrow(() => promptFor({ ...report, failed: [{ name: "x".repeat(11000), steps: [] }] }));
});

test("report escapes model markup and mentions and rejects foreign evidence URLs", () => {
  const body = render(report, { ...diagnosis, likely_cause: "<script>@everyone [approve](evil)</script>" });
  assert.ok(!body.includes("<script>"));
  assert.ok(!body.includes("@everyone"));
  assert.ok(body.includes("Only job/step metadata"));
  assert.throws(() => render({ ...report, url: "https://evil.example" }, diagnosis));
  assert.ok(isNewer("<!-- run:124 attempt:1 -->", report));
  assert.ok(isNewer("<!-- run:123 attempt:3 -->", report));
  assert.ok(!isNewer("<!-- run:123 attempt:1 -->", report));
});

test("collection rejects fork runs and records only failed steps from the latest attempt", async () => {
  const directory = await mkdtemp(join(tmpdir(), "ci-triage-test-"));
  const run = { name: "CI", path: ".github/workflows/ci.yml", status: "completed", conclusion: "failure",
    head_repository: { full_name: "example/repo" }, id: 123, run_attempt: 2,
    head_sha: "abc123", head_branch: "test", html_url: report.url, pull_requests: [{ number: 5 }] };
  const github = {
    rest: { actions: { getWorkflowRun: async () => ({ data: run }), listJobsForWorkflowRun: "jobs" } },
    paginate: async (_method, options) => {
      assert.equal(options.filter, "latest");
      return [
        { name: "pass", conclusion: "success" },
        { name: "fail", conclusion: "failure", html_url: report.failed[0].url,
          steps: [{ name: "setup", conclusion: "success" }, { name: "tests", conclusion: "failure" }] },
      ];
    },
  };
  try {
    const collected = await collect(github, { owner: "example", repo: "repo" }, "123", directory);
    assert.deepEqual(collected.failed[0].steps, ["tests"]);
    assert.equal(collected.failed.length, 1);
    run.head_repository.full_name = "fork/repo";
    await assert.rejects(collect(github, { owner: "example", repo: "repo" }, "123", directory));
    await assert.rejects(collect(github, { owner: "example", repo: "repo" }, "123; shell", directory));
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("publishing updates one bot comment but skips outdated PR heads and newer diagnoses", async () => {
  const directory = await mkdtemp(join(tmpdir(), "ci-triage-test-"));
  const calls = [];
  let head = report.sha;
  let previous = "<!-- ai-ci-triage -->\n<!-- run:122 attempt:1 -->";
  const github = {
    rest: {
      pulls: { get: async () => ({ data: { state: "open", head: { sha: head } } }) },
      repos: { get: async () => ({ data: { default_branch: "main" } }) },
      issues: {
        listComments: "comments",
        updateComment: async args => calls.push(args),
        createComment: async () => assert.fail("Must update the existing bot comment"),
      },
    },
    paginate: async () => [{ id: 9, user: { login: "github-actions[bot]" }, body: previous }],
  };
  try {
    await writeFile(join(directory, "ci-triage-input.json"), JSON.stringify(report));
    await writeFile(join(directory, "ci-triage-diagnosis.json"), JSON.stringify(diagnosis));
    const summary = join(directory, "summary");
    await publish(github, {}, directory, summary);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].comment_id, 9);
    assert.ok((await readFile(summary, "utf8")).includes("Human approval"));
    head = "new-head";
    await publish(github, {}, directory, summary);
    assert.equal(calls.length, 1);
    head = report.sha;
    previous = "<!-- ai-ci-triage -->\n<!-- run:124 attempt:1 -->";
    await publish(github, {}, directory, summary);
    assert.equal(calls.length, 1);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("default-branch failures update the existing open bot issue", async () => {
  const directory = await mkdtemp(join(tmpdir(), "ci-triage-test-"));
  const calls = [];
  const github = {
    rest: {
      repos: { get: async () => ({ data: { default_branch: "main" } }) },
      issues: {
        listForRepo: "issues",
        update: async args => calls.push(args),
        create: async () => assert.fail("Must reuse the existing issue"),
      },
    },
    paginate: async () => [{
      number: 10, title: "[CI triage] CI failure on default branch",
      user: { login: "github-actions[bot]" }, body: "<!-- ai-ci-triage -->",
    }],
  };
  try {
    await writeFile(join(directory, "ci-triage-input.json"),
      JSON.stringify({ ...report, branch: "main", pullRequests: [] }));
    await writeFile(join(directory, "ci-triage-diagnosis.json"), JSON.stringify(diagnosis));
    await publish(github, {}, directory, join(directory, "summary"));
    assert.equal(calls.length, 1);
    assert.equal(calls[0].issue_number, 10);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("CLI invocation exposes no tools and isolates repository credentials and working directory", async () => {
  const directory = await mkdtemp(join(tmpdir(), "ci-triage-test-"));
  try {
    await writeFile(join(directory, "ci-triage-input.json"), JSON.stringify(report));
    await writeFile(join(directory, "copilot"), `#!/usr/bin/env node
import assert from "node:assert/strict";
assert.equal(process.env.GITHUB_TOKEN, undefined);
assert.equal(process.env.GH_TOKEN, undefined);
assert.equal(process.env.COPILOT_GITHUB_TOKEN, "synthetic-token");
assert.equal(process.cwd(), process.env.HOME);
assert.equal(process.env.HOME, process.env.COPILOT_HOME);
const args = process.argv.slice(2);
assert.ok(args.includes("--available-tools"));
assert.equal(args[args.indexOf("--available-tools") + 1], "--disable-builtin-mcps");
assert.ok(!args.includes("--deny-tool"));
assert.ok(!args.includes("--deny-url"));
assert.ok(args.includes("--no-auto-update"));
assert.equal(args[args.indexOf("--model") + 1], "claude-haiku-4.5");
assert.equal(args[args.indexOf("--max-ai-credits") + 1], "30");
console.log(${JSON.stringify(JSON.stringify(diagnosis))});
`, { mode: 0o700 });
    await promisify(execFile)(process.execPath, [fileURLToPath(new URL("./ci-triage.mjs", import.meta.url))], {
      env: {
        ...process.env, PATH: `${directory}:${process.env.PATH}`, RUNNER_TEMP: directory,
        COPILOT_GITHUB_TOKEN: "synthetic-token", GITHUB_TOKEN: "must-not-reach-model",
        GH_TOKEN: "must-not-reach-model",
        AI_CI_MODEL: "claude-haiku-4.5",
      },
    });

    assert.deepEqual(JSON.parse(await readFile(join(directory, "ci-triage-diagnosis.json"), "utf8")), diagnosis);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("preflight avoids AI requests for stale PRs, duplicate attempts and newer reports", async () => {
  let head = "new-commit";
  let body = "<!-- ai-ci-triage -->\n<!-- run:123 attempt:2 -->";
  const github = {
    rest: {
      pulls: { get: async () => ({ data: { state: "open", head: { sha: head } } }) },
      issues: { listComments: "comments" },
    },
    paginate: async () => [{ user: { login: "github-actions[bot]" }, body }],
  };
  assert.ok(await skipReason(github, {}, report));
  head = report.sha;
  assert.ok(await skipReason(github, {}, report));
  body = "<!-- ai-ci-triage -->\n<!-- run:124 attempt:1 -->";
  assert.ok(await skipReason(github, {}, report));
  body = "<!-- ai-ci-triage -->\n<!-- run:123 attempt:1 -->";
  assert.equal(await skipReason(github, {}, report), null);
});

test("preflight skips duplicate default-branch issues but not unreported or summary-only runs", async () => {
  let body = "<!-- ai-ci-triage -->\n<!-- run:123 attempt:2 -->";
  const github = {
    rest: {
      repos: { get: async () => ({ data: { default_branch: "main" } }) },
      issues: { listForRepo: "issues" },
    },
    paginate: async () => [{
      title: "[CI triage] CI failure on default branch",
      user: { login: "github-actions[bot]" }, body,
    }],
  };
  const mainReport = { ...report, branch: "main", pullRequests: [] };
  assert.ok(await skipReason(github, {}, mainReport));
  body = "<!-- ai-ci-triage -->\n<!-- run:123 attempt:1 -->";
  assert.equal(await skipReason(github, {}, mainReport), null);
  assert.equal(await skipReason(github, {}, { ...mainReport, branch: "other" }), null);
});
