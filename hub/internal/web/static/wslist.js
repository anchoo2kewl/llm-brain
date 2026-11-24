// The session list rules (docs/design.md), shared with the Mac app's BrainHubCore. Plain script for the workspace;
// also loads as a CommonJS module for tests.
// Order: hub terminals newest first by when they opened (`started`, else the higher id), then sessions running
// elsewhere, higher id first. Activity, state, recency and selection never move a row.
(function (root) {
  function opened(t) { var s = t.started ? Date.parse(t.started) : NaN; return isNaN(s) ? -Infinity : s; }
  function newestFirst(a, b) {
    var ea = a.kind === "session", eb = b.kind === "session";
    if (ea !== eb) return ea ? 1 : -1;
    if (ea) return b.sid - a.sid;
    var d = opened(b) - opened(a);
    return d > 0 ? 1 : d < 0 ? -1 : Number(b.id) - Number(a.id);
  }
  // When it last ran or got input: the later of last_activity_at and last_input_at, else updated. Milliseconds.
  function lastActive(t) {
    var xs = [t.last_activity_at, t.last_input_at].map(function (v) { return v ? Date.parse(v) : NaN; }).filter(function (v) { return !isNaN(v); });
    if (xs.length) return Math.max.apply(null, xs);
    var u = t.updated ? Date.parse(t.updated) : NaN;
    return isNaN(u) ? null : u;
  }
  // "now", "2m", "1h", "3d".
  function shortAge(ms, now) {
    if (ms === null || ms === undefined) return "";
    var s = Math.max(0, Math.floor(((now === undefined ? Date.now() : now) - ms) / 1000));
    return s < 60 ? "now" : s < 3600 ? Math.floor(s / 60) + "m" : s < 86400 ? Math.floor(s / 3600) + "h" : Math.floor(s / 86400) + "d";
  }
  // The dot fades with age: bright when active now, dimmer as it gets older.
  function dotOpacity(ms, now) {
    if (ms === null || ms === undefined) return 0.4;
    var s = ((now === undefined ? Date.now() : now) - ms) / 1000;
    return s < 60 ? 1 : s < 600 ? 0.85 : s < 3600 ? 0.7 : s < 86400 ? 0.55 : 0.4;
  }
  // The dot pulses red only while the agent waits for the owner (a question, a permission prompt, an error) and the
  // owner has not opened it yet. A finished turn ("done") is a report: it never pulses.
  function pulses(t, opened) {
    if (opened) return false;
    if (t.blocked || t.state === "blocked") return true;
    return t.state === "waiting" && !!t.ask_kind && t.ask_kind !== "done";
  }
  var api = { newestFirst: newestFirst, lastActive: lastActive, shortAge: shortAge, dotOpacity: dotOpacity, pulses: pulses };
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.wsList = api;
})(this);
