import test from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";

const { treeStateKey, folder, searchText, isReply, recentKey, byRecent, lastActive, shortAge, dotOpacity, pulses, treeRows, switcher } =
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
    "\x1b]11;rgb:0d0d/1111/1717\x07", "\x1b]10;rgb:e6e6/eded/f3f3\x1b\\", "\x1b[I\x1b[12;5R", "\x1b[?2026;2$y", "\x1bP1$r0m\x1b\\", "\x1bP>|xterm.js(5.5)\x1b\\"]) assert.ok(isReply(r), JSON.stringify(r));
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
});
