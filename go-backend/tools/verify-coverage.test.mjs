import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const tool = path.join(path.dirname(fileURLToPath(import.meta.url)), "verify-coverage.mjs");

function tmp() {
  return fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "vcov-")));
}

function write(file, text) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, text);
}

// Fake repo root. server/a.js line layout is asserted below.
function makeRepo() {
  const root = tmp();
  write(path.join(root, "index.js"), 'const a = require("./server/a.js")\nprocess.env.BRANCH === "x" ? a.pick(true) : a.pick(false)\na.used()\nrequire("./server/libs/v.js")\nrequire("./server/migrations/m.js")\n');
  write(
    path.join(root, "server/a.js"),
    [
      "function used() {", // 1
      "  return 1", // 2
      "}", // 3
      "function unused() {", // 4
      "  return 2", // 5
      "}", // 6
      "function pick(flag) {", // 7
      "  if (flag) {", // 8
      "    return 'yes'", // 9
      "  } else {", // 10
      "    return 'no'", // 11
      "  }", // 12
      "}", // 13
      "module.exports = { used, unused, pick }",
      "",
    ].join("\n"),
  );
  write(path.join(root, "server/b.js"), "module.exports = 1\n");
  write(path.join(root, "server/libs/v.js"), "module.exports = function vendored() { return 1 }\n");
  write(path.join(root, "server/migrations/m.js"), "module.exports = { up() {} }\n");
  return root;
}

function runNode(root, covDir, env = {}) {
  const result = spawnSync(process.execPath, [path.join(root, "index.js")], {
    env: { ...process.env, NODE_V8_COVERAGE: covDir, ...env },
    encoding: "utf8",
  });
  assert.equal(result.status, 0, result.stderr);
}

function runTool(args) {
  return spawnSync(process.execPath, [tool, ...args], { encoding: "utf8" });
}

function report(root, v8, extra = []) {
  const out = path.join(tmp(), "report.json");
  const result = runTool(["--v8", v8, "--root", root, "--out", out, ...extra]);
  assert.equal(result.status, 0, result.stderr);
  return { json: JSON.parse(fs.readFileSync(out, "utf8")), stdout: result.stdout };
}

const file = (json, rel) => json.files.find((f) => f.path === rel);
const covers = (ranges, line) => ranges.some((r) => r.start_line <= line && line <= r.end_line);

test("end to end: executed, unexecuted, untaken branch, never loaded, groups", () => {
  const root = makeRepo();
  const v8 = path.join(tmp(), "cov");
  runNode(root, path.join(v8, "run1"), { BRANCH: "x" });
  const md = path.join(tmp(), "r.md");
  const { json, stdout } = report(root, v8, ["--md", md]);

  const a = file(json, "server/a.js");
  assert.equal(a.group, "app");
  assert.ok(!a.unexecuted_functions.some((f) => f.name === "used"));
  assert.deepEqual(
    a.unexecuted_functions.map((f) => [f.name, f.start_line, f.end_line]),
    [["unused", 4, 6]],
  );
  // flag is true, so the else branch (lines 10-11) is not taken; it must be CJS-aligned.
  assert.ok(covers(a.uncovered_ranges, 11), JSON.stringify(a.uncovered_ranges));
  assert.ok(!covers(a.uncovered_ranges, 9));
  assert.ok(!covers(a.uncovered_ranges, 5), "unexecuted function body is not a branch");

  assert.deepEqual(json.never_loaded, [{ path: "server/b.js", group: "app", lines: 1 }]);
  assert.equal(file(json, "server/libs/v.js").group, "vendored");
  assert.equal(file(json, "server/migrations/m.js").group, "migrations");
  assert.equal(json.groups.app.files, 3); // index.js, a.js, b.js
  assert.equal(json.groups.app.loaded_files, 2);
  assert.equal(json.raw_files, 1);
  assert.match(stdout, /app:/);
  assert.doesNotMatch(stdout, /pass|fail/i);

  const text = fs.readFileSync(md, "utf8");
  assert.match(text, /Execution is not proof of behaviour/);
  assert.match(text, /unused \(L4-L6\)/);
});

test("merging two processes covers both branches", () => {
  const root = makeRepo();
  const v8 = path.join(tmp(), "cov");
  runNode(root, path.join(v8, "one"), { BRANCH: "x" });
  runNode(root, path.join(v8, "two"), { BRANCH: "y" });
  const { json } = report(root, v8);
  const a = file(json, "server/a.js");
  assert.equal(json.raw_files, 2);
  assert.deepEqual(a.uncovered_ranges, []);
  assert.deepEqual(a.unexecuted_functions.map((f) => f.name), ["unused"]);
});

test("--filter and --top shape the markdown", () => {
  const root = makeRepo();
  const v8 = path.join(tmp(), "cov");
  runNode(root, v8, { BRANCH: "x" });
  const md = path.join(tmp(), "r.md");
  report(root, v8, ["--md", md, "--filter", "server/controllers/", "--top", "1"]);
  assert.doesNotMatch(fs.readFileSync(md, "utf8"), /server\/a\.js/);
});

test("invalid input", () => {
  const missing = runTool(["--v8", path.join(tmp(), "nope")]);
  assert.equal(missing.status, 1);
  assert.match(missing.stderr, /not a directory/);

  const noArg = runTool([]);
  assert.equal(noArg.status, 1);

  const empty = tmp();
  const none = runTool(["--v8", empty, "--root", empty]);
  assert.equal(none.status, 1);
  assert.match(none.stderr, /no V8 coverage files/);

  const root = makeRepo();
  const v8 = path.join(tmp(), "cov");
  runNode(root, v8, { BRANCH: "x" });
  write(path.join(v8, "other.json"), '{"hello": 1}');
  write(path.join(v8, "broken.json"), "not json");
  const { json } = report(root, v8);
  assert.equal(json.ignored, 2);
  assert.equal(json.raw_files, 1);
});
