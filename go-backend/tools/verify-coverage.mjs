#!/usr/bin/env node
// Merge raw V8 coverage (NODE_V8_COVERAGE output) from many runs of the pinned
// upstream server and report which upstream source was never executed.
// Execution is not proof of asserted behaviour: this is an audit aid only and
// prints no pass/fail verdict.
//
// Usage: node verify-coverage.mjs --v8 <dir> [--root <repo>] [--out report.json]
//        [--md report.md] [--filter <path prefix>]... [--top N]
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

const GROUPS = ["app", "vendored", "migrations"];

function fail(message) {
  console.error(`verify-coverage: ${message}`);
  process.exit(1);
}

/** Classify a root-relative, forward-slash path. */
function groupOf(rel) {
  if (rel.startsWith("server/libs/")) return "vendored";
  if (rel.startsWith("server/migrations/")) return "migrations";
  return "app";
}

/** All files below dir (recursive), sorted; entries are absolute paths. */
function walk(dir, keep) {
  const found = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) found.push(...walk(full, keep));
    else if (entry.isFile() && keep(entry.name)) found.push(full);
  }
  return found.sort();
}

/** Root-relative path for a file: URL when it is index.js or server/**.js, else null. */
function relativeSource(url, root) {
  if (typeof url !== "string" || !url.startsWith("file:")) return null;
  let file;
  try {
    file = fileURLToPath(url);
  } catch {
    return null;
  }
  const rel = path.relative(root, file).split(path.sep).join("/");
  if (rel === "index.js") return rel;
  if (rel.startsWith("server/") && rel.endsWith(".js")) return rel;
  return null;
}

function isCoverageObject(value) {
  return (
    value !== null &&
    typeof value === "object" &&
    Array.isArray(value.result) &&
    value.result.every((script) => script && typeof script.url === "string" && Array.isArray(script.functions))
  );
}

/** Read every *.json under v8Dir. Returns {coverage: [objects], ignored}. */
function readCoverage(v8Dir) {
  const coverage = [];
  let ignored = 0;
  for (const file of walk(v8Dir, (name) => name.endsWith(".json"))) {
    let text;
    try {
      text = fs.readFileSync(file, "utf8");
    } catch (err) {
      fail(`cannot read ${file}: ${err.message}`);
    }
    let parsed;
    try {
      parsed = JSON.parse(text);
    } catch {
      ignored++; // not JSON, so not a coverage file
      continue;
    }
    if (isCoverageObject(parsed)) coverage.push(parsed);
    else ignored++;
  }
  return { coverage, ignored };
}

/** Start offset of every line, for offset-to-line lookups. */
function lineStartsOf(text) {
  const starts = [0];
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 10) starts.push(i + 1);
  return starts;
}

/** 1-based line containing offset (binary search over line starts). */
function lineAt(starts, offset) {
  let lo = 0;
  let hi = starts.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (starts[mid] <= offset) lo = mid;
    else hi = mid - 1;
  }
  return lo + 1;
}

function isBlank(code) {
  return code <= 32 || code === 0xa0 || code === 0xfeff || code === 0x2028 || code === 0x2029;
}

/** Number of lines in a text (a trailing newline does not add one). */
function countLines(text) {
  if (text === "") return 0;
  const starts = lineStartsOf(text);
  return text.endsWith("\n") ? starts.length - 1 : starts.length;
}

/** Per-file merge state: covered and known character bitmaps and function table. */
function newFileState(text) {
  return { text, covered: new Uint8Array(text.length), known: new Uint8Array(text.length), functions: new Map() };
}

/** Fold one process's script coverage into the file state. */
function mergeScript(state, script) {
  const length = state.text.length;
  const process = new Uint8Array(length); // this process only
  for (const fn of script.functions) {
    const ranges = Array.isArray(fn.ranges) ? fn.ranges : [];
    ranges.forEach((range, index) => {
      const start = Math.max(0, Math.min(range.startOffset, length));
      const end = Math.max(start, Math.min(range.endOffset, length));
      process.fill(range.count > 0 ? 1 : 0, start, end);
      state.known.fill(1, start, end);
      if (index === 0) {
        const key = `${fn.functionName}\0${range.startOffset}\0${range.endOffset}`;
        const entry = state.functions.get(key) ?? { name: fn.functionName, start, end, executed: false };
        if (range.count > 0) entry.executed = true;
        state.functions.set(key, entry);
      }
    });
  }
  for (let i = 0; i < length; i++) if (process[i]) state.covered[i] = 1;
}

/** Summarise one loaded file: counts, unexecuted functions, uncovered line runs. */
function summariseFile(rel, group, state) {
  const { text, covered, known } = state;
  const starts = lineStartsOf(text);
  const lineCount = starts.length;
  const entries = [...state.functions.values()];

  // Module-level function: anonymous, starts at 0. Skipped when listing unexecuted
  // functions unless the whole file never ran.
  const isModule = (fn) => fn.name === "" && fn.start === 0;
  const moduleRan = entries.some((fn) => isModule(fn) && fn.executed);
  const unexecuted = entries
    .filter((fn) => !fn.executed && (!isModule(fn) || !moduleRan))
    .sort((a, b) => a.start - b.start);

  // Characters inside never-executed functions are not "branches not taken".
  const dead = new Uint8Array(text.length);
  for (const fn of entries) if (!fn.executed) dead.fill(1, fn.start, fn.end);

  const lineKnown = new Uint8Array(lineCount + 1);
  const lineCovered = new Uint8Array(lineCount + 1);
  const lineBranch = new Uint8Array(lineCount + 1);
  let line = 1;
  for (let i = 0; i < text.length; i++) {
    const code = text.charCodeAt(i);
    if (code === 10) {
      line++;
      continue;
    }
    if (isBlank(code) || !known[i]) continue;
    lineKnown[line] = 1;
    if (covered[i]) lineCovered[line] = 1;
    else if (!dead[i]) lineBranch[line] = 1;
  }

  let lines = 0;
  let coveredLines = 0;
  const ranges = [];
  let runStart = 0;
  let runEnd = 0;
  const flush = () => {
    if (runStart) ranges.push({ start_line: runStart, end_line: runEnd });
    runStart = 0;
  };
  for (let l = 1; l <= lineCount; l++) {
    if (!lineKnown[l]) continue; // blank or unknown lines do not break a run
    lines++;
    if (lineCovered[l]) {
      coveredLines++;
      flush();
    } else if (lineBranch[l]) {
      if (!runStart) runStart = l;
      runEnd = l;
    } else {
      flush(); // uncovered but inside an unexecuted function
    }
  }
  flush();

  return {
    path: rel,
    group,
    loaded: true,
    functions: entries.length,
    executed_functions: entries.filter((fn) => fn.executed).length,
    lines,
    covered_lines: coveredLines,
    unexecuted_functions: unexecuted.map((fn) => ({
      name: fn.name,
      start_line: lineAt(starts, fn.start),
      end_line: lineAt(starts, Math.max(fn.start, fn.end - 1)),
    })),
    uncovered_ranges: ranges,
  };
}

/** Build the full report object. */
export function buildReport(root, v8Dir) {
  const { coverage, ignored } = readCoverage(v8Dir);
  if (coverage.length === 0) fail(`no V8 coverage files found under ${v8Dir} (${ignored} ignored)`);

  const states = new Map();
  for (const proc of coverage) {
    for (const script of proc.result) {
      const rel = relativeSource(script.url, root);
      if (!rel) continue;
      let state = states.get(rel);
      if (!state) {
        let text;
        try {
          text = fs.readFileSync(path.join(root, rel), "utf8");
        } catch (err) {
          fail(`coverage mentions ${rel} but the source cannot be read: ${err.message}`);
        }
        state = newFileState(text);
        states.set(rel, state);
      }
      mergeScript(state, script);
    }
  }

  const sources = [];
  if (fs.existsSync(path.join(root, "index.js"))) sources.push("index.js");
  const serverDir = path.join(root, "server");
  if (fs.existsSync(serverDir)) {
    for (const file of walk(serverDir, (name) => name.endsWith(".js"))) {
      sources.push(path.relative(root, file).split(path.sep).join("/"));
    }
  }

  const files = [];
  const neverLoaded = [];
  for (const rel of sources) {
    const group = groupOf(rel);
    const state = states.get(rel);
    if (state) {
      files.push(summariseFile(rel, group, state));
      continue;
    }
    let text;
    try {
      text = fs.readFileSync(path.join(root, rel), "utf8");
    } catch (err) {
      fail(`cannot read ${rel}: ${err.message}`);
    }
    neverLoaded.push({ path: rel, group, lines: countLines(text) });
  }

  const groups = {};
  for (const name of GROUPS) {
    const inGroup = files.filter((f) => f.group === name);
    const missing = neverLoaded.filter((f) => f.group === name);
    const sum = (key) => inGroup.reduce((total, f) => total + f[key], 0);
    groups[name] = {
      files: inGroup.length + missing.length,
      loaded_files: inGroup.length,
      functions: sum("functions"),
      executed_functions: sum("executed_functions"),
      lines: sum("lines"),
      covered_lines: sum("covered_lines"),
      never_loaded_lines: missing.reduce((total, f) => total + f.lines, 0),
    };
  }
  return { root, raw_files: coverage.length, ignored, groups, files, never_loaded: neverLoaded };
}

const CAVEAT = "Execution is not proof of behaviour";

/** Plain-text per-group totals. */
export function totalsText(report) {
  const rows = [`raw coverage files: ${report.raw_files}, ignored: ${report.ignored}`];
  for (const name of GROUPS) {
    const g = report.groups[name];
    rows.push(
      `${name}: files ${g.loaded_files}/${g.files} loaded, functions ${g.executed_functions}/${g.functions} executed, ` +
        `lines ${g.covered_lines}/${g.lines} covered, never-loaded lines ${g.never_loaded_lines}`,
    );
  }
  return rows.join("\n");
}

/** Markdown report for app files, filtered by path prefix and limited to top N. */
export function markdown(report, filters, top) {
  const out = [
    "# Upstream source execution summary",
    "",
    `**${CAVEAT}.** This lists upstream code the suite never ran; it says nothing about what was asserted.`,
    "",
    `Raw coverage files: ${report.raw_files} (ignored: ${report.ignored}).`,
    "",
    "| group | files | loaded | functions executed | lines covered | never-loaded lines |",
    "| --- | ---: | ---: | ---: | ---: | ---: |",
  ];
  for (const name of GROUPS) {
    const g = report.groups[name];
    out.push(
      `| ${name} | ${g.files} | ${g.loaded_files} | ${g.executed_functions}/${g.functions} | ${g.covered_lines}/${g.lines} | ${g.never_loaded_lines} |`,
    );
  }

  const matches = (p) => filters.length === 0 || filters.some((prefix) => p.startsWith(prefix));
  let app = report.files
    .filter((f) => f.group === "app" && matches(f.path) && f.unexecuted_functions.length + f.uncovered_ranges.length > 0)
    .sort((a, b) => b.unexecuted_functions.length - a.unexecuted_functions.length || a.path.localeCompare(b.path));
  if (top !== undefined) app = app.slice(0, top);

  out.push("", "## App files with unexecuted code", "");
  if (app.length === 0) out.push("(none)");
  for (const f of app) {
    out.push(`### ${f.path}`, "", `Functions executed: ${f.executed_functions}/${f.functions}`, "");
    if (f.unexecuted_functions.length > 0) {
      out.push("Unexecuted functions:");
      for (const fn of f.unexecuted_functions) {
        out.push(`- ${fn.name === "" ? "(anonymous)" : fn.name} (L${fn.start_line}-L${fn.end_line})`);
      }
      out.push("");
    }
    if (f.uncovered_ranges.length > 0) {
      out.push("Uncovered ranges: " + f.uncovered_ranges.map((r) => `L${r.start_line}-L${r.end_line}`).join(", "), "");
    }
  }

  const missing = report.never_loaded.filter((f) => f.group === "app" && matches(f.path));
  if (missing.length > 0) {
    out.push("## App files never loaded", "");
    for (const f of missing) out.push(`- ${f.path} (${f.lines} lines)`);
    out.push("");
  }
  return out.join("\n") + "\n";
}

function main() {
  let values;
  try {
    ({ values } = parseArgs({
      options: {
        v8: { type: "string" },
        root: { type: "string" },
        out: { type: "string" },
        md: { type: "string" },
        filter: { type: "string", multiple: true },
        top: { type: "string" },
      },
      strict: true,
    }));
  } catch (err) {
    fail(`${err.message}\nusage: verify-coverage.mjs --v8 <dir> [--root <repo>] [--out <json>] [--md <md>] [--filter <prefix>]... [--top N]`);
  }
  if (!values.v8) fail("--v8 <dir> is required");
  if (!fs.existsSync(values.v8) || !fs.statSync(values.v8).isDirectory()) fail(`--v8 is not a directory: ${values.v8}`);

  let top;
  if (values.top !== undefined) {
    top = Number(values.top);
    if (!Number.isInteger(top) || top <= 0) fail(`--top must be a positive integer, got ${values.top}`);
  }

  const defaultRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
  let root;
  try {
    root = fs.realpathSync(path.resolve(values.root ?? defaultRoot));
  } catch (err) {
    fail(`--root is not readable: ${err.message}`);
  }

  const report = buildReport(root, values.v8);
  try {
    if (values.out) fs.writeFileSync(values.out, JSON.stringify(report, null, 2) + "\n");
    if (values.md) fs.writeFileSync(values.md, markdown(report, values.filter ?? [], top));
  } catch (err) {
    fail(`cannot write report: ${err.message}`);
  }
  console.log(totalsText(report));
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) main();
