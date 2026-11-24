// The session list rules (docs/design.md), shared with the Mac app's BrainHubCore. Plain script for the workspace;
// also loads as a CommonJS module for tests.
// Order, strictly: the latest activity or interaction, most recent first; ties: the higher id. Sessions running
// elsewhere follow the hub's own terminals. A parent ranks by the most recent row under it.
(function (root) {
  function ms(v) { var t = v ? Date.parse(v) : NaN; return isNaN(t) ? null : t; }
  // The order key in whole seconds: the agent's last output or state change (last_activity_at) and the owner's last
  // input (last_input_at, or `local`: what this page sent). An older hub has neither: its `updated` time. Start
  // time only when there is nothing else. Whole seconds, so jitter under a second never reorders.
  function recentKey(t, local) {
    var hub = [ms(t.last_activity_at), ms(t.last_input_at)].filter(function (v) { return v !== null; });
    var base = hub.length ? Math.max.apply(null, hub) : ms(t.updated);
    var xs = [base, local || null].filter(function (v) { return v !== null; });
    var k = xs.length ? Math.max.apply(null, xs) : ms(t.started);
    return k === null ? -Infinity : Math.floor(k / 1000) * 1000;
  }
  // A comparator, most recent first, for `keyOf(t)`. Hub terminals before sessions running elsewhere.
  function byRecent(keyOf) {
    return function (a, b) {
      var ea = a.kind === "session", eb = b.kind === "session";
      if (ea !== eb) return ea ? 1 : -1;
      var d = keyOf(b) - keyOf(a);
      return d > 0 ? 1 : d < 0 ? -1 : Number(ea ? b.sid : b.id) - Number(ea ? a.sid : a.id);
    };
  }
  // When it last ran or got input, for the age label and the dot (milliseconds).
  function lastActive(t, local) { var k = recentKey(t, local); return k === -Infinity ? null : k; }
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
  // The dot pulses red only while the agent waits for the owner (a question or a permission prompt) and the owner has
  // not opened it yet. The hub's needs_owner decides; an older hub: blocked, or waiting on a question. A finished turn
  // or an error is a report: it never pulses.
  function pulses(t, opened) {
    if (opened) return false;
    if (typeof t.needs_owner === "boolean") return t.needs_owner;
    return !!t.blocked || t.state === "blocked" || (t.state === "waiting" && t.ask_kind === "question");
  }
  // The session tree: each row under its parent, (parent_hub, parent_id) = (hub, id). A hub without these fields, or a
  // child whose parent is not listed, gives a flat list. Children follow their own activity; a parent ranks by the
  // most recent row under it (`keyOf`, default recentKey). `open` holds the ids of the parents whose children show.
  // Returns the rows to show: {t, depth, kids, otherHub}.
  function treeRows(rows, open, keyOf) {
    keyOf = keyOf || function (t) { return recentKey(t); };
    function key(h, id) { return h + "#" + id; }
    var have = {}, kids = {}, out = [], placed = [], own = byRecent(keyOf);
    rows.forEach(function (t) { if (t.hub && t.kind !== "session") have[key(t.hub, t.id)] = true; });
    function parentOf(t) { return t.parent_hub && t.parent_id > 0 && have[key(t.parent_hub, t.parent_id)] ? key(t.parent_hub, t.parent_id) : null; }
    rows.forEach(function (t) { var p = parentOf(t); if (p) (kids[p] = kids[p] || []).push(t); });
    function build(t) { // {t, kids: [...], rank}
      placed.push(t);
      var k = t.hub ? key(t.hub, t.id) : null;
      var ks = (k && kids[k] || []).filter(function (c) { return placed.indexOf(c) < 0; }).map(build);
      ks.sort(function (a, b) { return own(a.t, b.t); });
      return { t: t, kids: ks, rank: Math.max.apply(null, [keyOf(t)].concat(ks.map(function (c) { return c.rank; }))) };
    }
    var top = rows.filter(function (t) { return !parentOf(t); }).map(build);
    // Bad data (a session that is its own ancestor) has no root: show those sessions at the top level, never hide them.
    rows.forEach(function (t) { if (placed.indexOf(t) < 0) top.push(build(t)); });
    var rankOf = new Map(top.map(function (n) { return [n.t, n.rank]; }));
    var byRank = byRecent(function (t) { return rankOf.get(t); });
    top.sort(function (a, b) { return byRank(a.t, b.t); });
    function walk(n, depth, parentHub, show) {
      if (show) out.push({ t: n.t, depth: depth, kids: n.kids.length, otherHub: depth > 0 && n.t.hub !== parentHub ? n.t.hub : "" });
      n.kids.forEach(function (c) { walk(c, depth + 1, n.t.hub, show && !!open[n.t.id]); });
    }
    top.forEach(function (n) { walk(n, 0, "", true); });
    return out;
  }
  // The quick switcher: indices of the names that match, those that start with the query first, in list order.
  function switcher(names, q, limit) {
    q = (q || "").trim().toLowerCase(); limit = limit || 8;
    var idx = names.map(function (_, i) { return i; });
    if (!q) return idx.slice(0, limit);
    var low = names.map(function (n) { return n.toLowerCase(); });
    var starts = idx.filter(function (i) { return low[i].indexOf(q) === 0; });
    var inside = idx.filter(function (i) { return low[i].indexOf(q) > 0; });
    return starts.concat(inside).slice(0, limit);
  }
  var api = { recentKey: recentKey, byRecent: byRecent, lastActive: lastActive, shortAge: shortAge, dotOpacity: dotOpacity,
    pulses: pulses, treeRows: treeRows, switcher: switcher };
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.wsList = api;
})(this);
