import test from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";

const { treeStateKey, folder, searchText, isReply, recentKey, byRecent, lastActive, shortAge, dotOpacity, pulses, treeRows, switcher,
  modelShort, duration, rowMeta, detailRows } =
  createRequire(import.meta.url)("../hub/internal/web/static/wslist.js");
const at = (s) => new Date(Date.parse("2027-01-15T08:00:00Z") + s * 1000).toISOString();
const term = (id, f = {}) => ({ id, updated: at(f.updated ?? 0), started: at(f.started ?? 0),
  ...(f.act !== undefined && { last_activity_at: at(f.act) }), ...(f.input !== undefined && { last_input_at: at(f.input) }), ...f.extra });
const ids = (xs, local = {}) => xs.slice().sort(byRecent((t) => recentKey(t, local[t.id]))).map((x) => x.id);

test("order: agent output and owner input move a row up", () => {
  const base = [term(1, { act: 10 }), term(2, { act: 20 }), term(3, { act: 30 })];
  assert.deepEqual(ids(base), [3, 2, 1]);
  assert.deepEqual(ids([term(1, { act: 40 }), term(2, { act: 20 }), term(3, { act: 30 })]), [1, 3, 2], "output");
  assert.deepEqual(ids([term(1, { act: 10, input: 50 }), term(2, { act: 20 }), term(3, { act: 30 })]), [1, 3, 2], "input");
  assert.deepEqual(ids(base, { 2: Date.parse(at(60)) }), [2, 3, 1], "input this page sent counts on an older hub too");
});

test("order: a spinner, a state flip or jitter under a second never moves a row; ties by the higher id", () => {
  const base = [term(1, { act: 10 }), term(2, { act: 20 })];
  assert.deepEqual(ids([term(1, { act: 10, updated: 99, extra: { state: "waiting" } }), term(2, { act: 20, extra: { state: "working" } })]), ids(base));
  assert.deepEqual(ids([term(1, { act: 20 }), term(2, { act: 20 })]), [2, 1]);
  const a = term(1), b = term(2);
  a.last_activity_at = "2027-01-15T08:00:20.900Z"; b.last_activity_at = "2027-01-15T08:00:20.100Z";
  assert.deepEqual(ids([a, b]), [2, 1]);
  assert.deepEqual(ids([term(1, { updated: 50 }), term(2, { updated: 40 })]), [1, 2], "an older hub: updated");
  const s9 = { id: "s9", sid: 9, kind: "session", updated: at(999) }, s5 = { id: "s5", sid: 5, kind: "session", updated: at(1000) };
  assert.deepEqual(ids([s9, term(1, { act: 1 }), s5]), [1, "s5", "s9"], "sessions running elsewhere follow, by their own activity");
});

test("recency: short times, the later of activity and input, the dot fades", () => {
  const now = Date.parse("2026-11-23T12:00:00Z");
  assert.equal(shortAge(now - 5000, now), "now");
  assert.equal(shortAge(now - 125000, now), "2m");
  assert.equal(shortAge(now - 3700000, now), "1h");
  assert.equal(shortAge(now - (3 * 86400 + 10) * 1000, now), "3d");
  assert.equal(shortAge(null, now), "");
  assert.equal(lastActive({ updated: "2026-11-23T11:00:00Z" }), Date.parse("2026-11-23T11:00:00Z"));
  assert.equal(lastActive({ updated: "2026-11-20T00:00:00Z", last_activity_at: "2026-11-23T11:00:00Z", last_input_at: "2026-11-23T11:58:00Z" }), Date.parse("2026-11-23T11:58:00Z"));
  assert.equal(dotOpacity(now, now), 1);
  assert.equal(dotOpacity(now - 90000 * 1000, now), 0.4);
});

test("only needs-you pulses, and it stops once opened", () => {
  assert.ok(pulses({ state: "waiting", ask_kind: "question" }, false));
  assert.ok(pulses({ blocked: { kind: "tcc" } }, false));
  assert.ok(!pulses({ state: "waiting", ask_kind: "error" }, false) && !pulses({ state: "waiting" }, false));
  assert.ok(pulses({ state: "waiting", ask_kind: "done", needs_owner: true }, false), "the hub's needs_owner decides");
  assert.ok(!pulses({ state: "waiting", ask_kind: "question", needs_owner: false }, false));
  assert.ok(!pulses({ state: "waiting", ask_kind: "done" }, false), "a finished turn is calm");
  assert.ok(!pulses({ state: "working" }, false) && !pulses({}, false));
  assert.ok(!pulses({ state: "waiting", ask_kind: "question" }, true));
});

test("session tree: a parent rises with its children; children follow their own activity; collapsed until opened", () => {
  const node = (id, hub, act, parent) => ({ ...term(id, { act }), hub, ...(parent && { parent_hub: parent[0], parent_id: parent[1] }) });
  const rows = [node(1, "studio", 1), node(2, "studio", 50, ["studio", 1]), node(3, "studio", 40, ["studio", 1]),
    node(1, "laptop", 2, ["studio", 2]), node(8, "studio", 30), node(9, "studio", 5, ["studio", 77])];
  const view = (open) => treeRows(rows, Object.fromEntries(Object.keys(open).flatMap(id => rows.filter(t => String(t.id) === id).map(t => [treeStateKey(t), true])))).map((r) => `${r.t.hub}#${r.t.id}/${r.depth}/${r.kids}${r.otherHub ? "@" + r.otherHub : ""}`);
  assert.deepEqual(view({}), ["studio#1/0/2", "studio#8/0/0", "studio#9/0/0"], "the conductor rises: its child worked at 50");
  assert.deepEqual(view({ 1: true, 2: true }), ["studio#1/0/2", "studio#2/1/1", "laptop#1/2/0@laptop", "studio#3/1/0", "studio#8/0/0", "studio#9/0/0"]);
  assert.deepEqual(treeRows([term(1, { act: 1 }), term(2, { act: 2 })], {}).map((r) => r.t.id), [2, 1], "an older hub: flat");
  const loop = [node(7, "s", 3, ["s", 7]), node(6, "s", 2, ["s", 5]), node(5, "s", 1, ["s", 6])];
  assert.deepEqual(treeRows(loop, Object.fromEntries(loop.map(t => [treeStateKey(t), true]))).map((r) => r.t.id).sort(), [5, 6, 7], "a loop never hides a session");
});

test("quick switcher: names that start with the query first, in list order", () => {
  const names = ["Memory sync", "DB migration", "Docs site", "scratch", "Old db"];
  assert.deepEqual(switcher(names, "db"), [1, 4]);
  assert.deepEqual(switcher(names, "s"), [3, 0, 2]);
  assert.deepEqual(switcher(names, "", 2), [0, 1]);
});

test("terminal replies are not owner input", () => {
  for (const r of ["\x1b[0n", "\x1b[12;5R", "\x1b[?1;1R", "\x1b[?62;22c", "\x1b[>0;10;1c", "\x1b[I", "\x1b[O",
    "\x1b]11;rgb:0d0d/1111/1717\x07", "\x1b]10;rgb:e6e6/eded/f3f3\x1b\\", "\x1b[I\x1b[12;5R", "\x1b[?2026;2$y", "\x1bP1$r0m\x1b\\", "\x1bP>|xterm.js(5.5)\x1b\\",
    "\x1b[<64;10;5M", "\x1b[<0;3;4m", "\x1b[M !!", "\x1b[<65;1;1M\x1b[<65;1;1M"]) assert.ok(isReply(r), JSON.stringify(r));
  for (const k of ["a", "\r", "\x1b[A", "\x1b[1;5D", "\x1b", "\x03", "hello\x1b[0n", "\x1b[200~text\x1b[201~"]) assert.ok(!isReply(k), JSON.stringify(k));
});


test("tree state distinguishes hubs and replacement sessions", () => {
  const a = {id: 1, hub: "studio", started: at(1)}, b = {...a, hub: "laptop"};
  assert.notEqual(treeStateKey(a), treeStateKey(b));
  assert.notEqual(treeStateKey(a), treeStateKey({...a, started: at(2)}));
  const rows = [a, b, {id: 2, hub: "studio", parent_hub: "studio", parent_id: 1}, {id: 3, hub: "laptop", parent_hub: "laptop", parent_id: 1}];
  const shown = treeRows(rows, {[treeStateKey(a)]: true});
  assert.ok(shown.some(r => r.t.id === 2));
  assert.ok(!shown.some(r => r.t.id === 3));
});
test("folders and search preserve full paths without missing-field words", () => {
  for (const [cwd, want] of [["/", "/"], ["~", "~"], ["/a/b/", "b"], ["", "~"]]) assert.equal(folder({cwd}), want);
  assert.ok(searchText({id: 1, cwd: "/work/brain/hub"}).includes("/work/brain/hub"));
  assert.ok(!searchText({id: 1}).includes("undefined"));
  assert.ok(searchText({id: 1}).includes("~"));
});

test("model short names: Claude, GPT and Gemini ids; anything else as it is", () => {
  const cases = { "claude-opus-5-5": "Opus 5.5", "claude-sonnet-4-5-20250929": "Sonnet 4.5", "claude-3-5-sonnet-20241022": "Sonnet 3.5",
    "claude-opus-5-5[1m]": "Opus 5.5", "claude-fable-5-2": "Fable 5.2", "gpt-6.1-sol": "GPT-6.1-Sol", "gpt-5-codex": "GPT-5-Codex",
    "gemini-4-carbon": "Gemini 4 Carbon", "gemini-2.5-pro": "Gemini 2.5 Pro", "claude-2.1": "Claude 2.1", "o3": "o3", "Custom Model": "Custom Model", "": "" };
  for (const [id, want] of Object.entries(cases)) assert.equal(modelShort(id), want, id);
  assert.equal(modelShort(undefined), "");
});

test("durations: seconds, minutes, hours and days", () => {
  assert.equal(duration(0), "0s");
  assert.equal(duration(45.9), "45s");
  assert.equal(duration(180), "3m");
  assert.equal(duration(3600), "1h");
  assert.equal(duration(2 * 3600 + 5 * 60), "2h 5m");
  assert.equal(duration(3 * 86400 + 4 * 3600 + 59), "3d 4h");
  assert.equal(duration(-5), "0s");
});

test("row meta: tool mark, model or tool name, context percent", () => {
  assert.deepEqual(rowMeta({ provider: "claude", model: "claude-opus-5-5", context_used: 92608, context_window: 200000 }),
    { mark: "CC", tool: "Claude Code", model: "Opus 5.5", pct: 46 });
  assert.deepEqual(rowMeta({ provider: "codex", cli: "Codex", context_used: 0, context_window: 475000 }), { mark: "CX", tool: "Codex", model: "Codex", pct: null });
  assert.equal(rowMeta({ cli: "Claude Code" }).mark, "CC", "a session running elsewhere has only cli");
  assert.equal(rowMeta({ provider: "claude", context_used: 300, context_window: 200 }).pct, 100);
});

test("details: the same eight rows before and after the hub answers", () => {
  const now = Date.parse("2026-11-25T12:00:00Z");
  const t = { id: 7, host: "studio", cwd: "/work/app", branch: "feat/x", provider: "claude", model: "claude-opus-5-5", state: "working",
    state_since: "2026-11-25T11:57:00Z", started: "2026-11-25T10:00:00Z", context_used: 92608, context_window: 200000, subagents: 2 };
  const before = detailRows(t, null, now);
  assert.deepEqual(before.map((r) => r.label), ["Context", "State", "Where", "Account", "Totals", "You", "Agent", "Started"]);
  assert.equal(before[0].value, "92k of 200k · 46%");
  assert.equal(before[1].value, "Working · 3m · 2 subagents");
  assert.equal(before[2].value, "studio · app · feat/x");
  assert.equal(before[2].title, "studio · /work/app · feat/x");
  assert.equal(before[3].value, "default login");
  assert.equal(before[5].value, "…");
  assert.equal(before[7].value, "2h ago");
  const d = { tool: 'Bash { "command": "ls" }', last_prompt: "Fix the\nbuild", last_reply: "",
    stats: { dollars: 0.589, tokens: { input: 32, output: 3770, cache_read: 1110178, cache_write: 58295 }, prompts: 1, tool_calls: 11, tool_errors: 1 } };
  const after = detailRows(t, d, now);
  assert.equal(after.length, before.length);
  assert.equal(after[1].value, "Working · 3m · 2 subagents · Bash");
  assert.equal(after[1].title, 'Bash { "command": "ls" }');
  assert.equal(after[4].value, "$0.59 · 1.2M tokens · 1 prompt · 11 tool calls (1 failed)");
  assert.equal(after[5].value, "Fix the build");
  assert.equal(after[6].value, "–");
  const waits = detailRows({ ...t, state: "waiting", needs_owner: true, branch: "main", context_used: undefined }, d, now);
  assert.equal(waits[1].value.split(" · ")[0], "Waiting for you");
  assert.equal(waits[0].value, "not reported");
  assert.equal(waits[2].value, "studio · app", "the default branch is left out");
  assert.equal(detailRows({ ...t, state: "waiting", ask_kind: "done", needs_owner: false }, d, now)[1].value.split(" · ")[0], "Finished its turn");
  for (const status of ["ended", "failed"])
    assert.equal(detailRows({ ...t, status, needs_owner: true }, d, now)[1].value.split(" · ")[0], "Ended", "an ended row never asks for you");
});
