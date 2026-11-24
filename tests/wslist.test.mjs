import test from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";

const { newestFirst, lastActive, shortAge, dotOpacity, pulses, treeRows } = createRequire(import.meta.url)("../hub/internal/web/static/wslist.js");
const ids = (xs) => xs.slice().sort(newestFirst).map((x) => x.id);

test("hub terminals: newest opened first, ties by the higher id", () => {
  const ts = [
    { id: 1, started: "2026-11-23T10:00:01.000Z", updated: "2026-11-23T12:00:00.000Z" },
    { id: 2, started: "2026-11-23T10:00:03.000Z" },
    { id: 3, started: "2026-11-23T10:00:02.000Z" },
    { id: 4, started: "2026-11-23T10:00:02.000Z" },
  ];
  assert.deepEqual(ids(ts), [2, 4, 3, 1]);
  assert.deepEqual(ids([{ id: 1 }, { id: 3 }, { id: 2 }]), [3, 2, 1], "an older hub without started: by id");
});

test("activity and state never reorder; a new terminal goes on top; sessions elsewhere follow", () => {
  const a = { id: 1, started: "2026-11-23T10:00:01Z", updated: "2026-11-23T10:00:01Z", status: "live" };
  const b = { id: 2, started: "2026-11-23T10:00:02Z", updated: "2026-11-23T10:00:02Z", status: "live" };
  assert.deepEqual(ids([a, b]), [2, 1]);
  assert.deepEqual(ids([{ ...a, updated: "2026-11-24T00:00:00Z", blocked: true }, { ...b, status: "ended" }]), [2, 1]);
  const s5 = { id: "s5", sid: 5, kind: "session" }, s9 = { id: "s9", sid: 9, kind: "session" };
  assert.deepEqual(ids([s5, a, s9, b, { id: 3, started: "2026-11-23T10:00:03Z" }]), [3, 2, 1, "s9", "s5"]);
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

test("session tree: children under their parent, newest first, collapsed until opened", () => {
  const rows = [
    { id: 9, hub: "studio", parent_hub: "studio", parent_id: 77, started: "2026-01-01T00:00:05Z" }, // parent not listed
    { id: 1, hub: "laptop", parent_hub: "studio", parent_id: 2, started: "2026-01-01T00:00:04Z" }, // same id elsewhere
    { id: 3, hub: "studio", parent_hub: "studio", parent_id: 1, started: "2026-01-01T00:00:03Z" },
    { id: 2, hub: "studio", parent_hub: "studio", parent_id: 1, started: "2026-01-01T00:00:02Z" },
    { id: 1, hub: "studio", started: "2026-01-01T00:00:01Z" },
  ].sort(newestFirst);
  const view = (open) => treeRows(rows, open).map((r) => `${r.t.hub}#${r.t.id}/${r.depth}/${r.kids}${r.otherHub ? "@" + r.otherHub : ""}`);
  assert.deepEqual(view({}), ["studio#9/0/0", "studio#1/0/2"]);
  assert.deepEqual(view({ 1: true, 2: true }), ["studio#9/0/0", "studio#1/0/2", "studio#3/1/0", "studio#2/1/1", "laptop#1/2/0@laptop"]);
  assert.deepEqual(treeRows([{ id: 2 }, { id: 1 }], {}).map((r) => r.t.id), [2, 1], "an older hub: flat");
  const loop = [{ id: 7, hub: "s", parent_hub: "s", parent_id: 7 }, { id: 6, hub: "s", parent_hub: "s", parent_id: 5 }, { id: 5, hub: "s", parent_hub: "s", parent_id: 6 }];
  assert.deepEqual(treeRows(loop, {}).map((r) => r.t.id), [7, 6]);
  assert.deepEqual(treeRows(loop, { 7: true, 6: true, 5: true }).map((r) => r.t.id).sort(), [5, 6, 7], "a loop in bad data never hides a session");
});
