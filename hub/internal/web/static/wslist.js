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
  function treeStateKey(t) { return (t.hub || "") + "#" + t.id + "#" + (t.started || ""); }
  function folder(t) {
    var raw = t.cwd || "", c = raw.replace(/\/+$/, "");
    if (!c) return /^\/+$/.test(raw) ? "/" : "~";
    return c === "~" ? "~" : c.split("/").pop();
  }
  function searchText(t) { return [t.title || "terminal " + t.id, t.cwd || folder(t), t.cli, t.host].filter(Boolean).join(" "); }
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
      n.kids.forEach(function (c) { walk(c, depth + 1, n.t.hub, show && !!open[treeStateKey(n.t)]); });
    }
    top.forEach(function (n) { walk(n, 0, "", true); });
    return out;
  }
  // A model id as a short name: "claude-opus-5-5" -> "Opus 5.5", "gpt-6.1-sol" -> "GPT-6.1-Sol",
  // "gemini-4-carbon" -> "Gemini 4 Carbon". Date stamps and "[1m]" go. Any other id stays as it is.
  function modelShort(id) {
    var raw = String(id || "").trim(), m = raw.toLowerCase().replace(/\[[^\]]*\]$/, "");
    var parts = m.split("-").filter(function (p) { return p && !/^\d{8}$/.test(p); });
    function cap(p) { return /^[a-z]/.test(p) ? p[0].toUpperCase() + p.slice(1) : p; }
    var num = function (p) { return /^\d+(\.\d+)?$/.test(p); };
    if (parts[0] === "claude" && parts.length > 1) {
      var words = parts.slice(1).filter(function (p) { return !num(p); }), nums = parts.slice(1).filter(num);
      return words.map(cap).join(" ") + (nums.length ? " " + nums.join(".") : "");
    }
    if (parts[0] === "gpt" && parts.length > 1) return "GPT-" + parts.slice(1).map(cap).join("-");
    if (parts[0] === "gemini" && parts.length > 1) return parts.map(cap).join(" ");
    return raw;
  }
  // A length of time: "45s", "3m", "2h 5m", "3d 4h".
  function duration(sec) {
    var s = Math.max(0, Math.floor(sec || 0));
    if (s < 60) return s + "s";
    if (s < 3600) return Math.floor(s / 60) + "m";
    if (s < 86400) { var m = Math.floor(s % 3600 / 60); return Math.floor(s / 3600) + "h" + (m ? " " + m + "m" : ""); }
    var h = Math.floor(s % 86400 / 3600);
    return Math.floor(s / 86400) + "d" + (h ? " " + h + "h" : "");
  }
  var TOOLS = { claude: "Claude Code", codex: "Codex", antigravity: "Antigravity", shell: "Shell" };
  var MARKS = { claude: "CC", codex: "CX", antigravity: "AG" };
  function provider(t) { var p = String(t.provider || t.cli || "").toLowerCase(); return p === "claude code" ? "claude" : p; }
  // The second line of a row: the tool's mark, the model (else the tool's name) and the context used.
  function rowMeta(t) {
    var p = provider(t), used = t.context_used, win = t.context_window;
    var pct = used > 0 && win > 0 ? Math.min(100, Math.round(used * 100 / win)) : null;
    return { mark: MARKS[p] || ">_", tool: TOOLS[p] || t.cli || p, model: modelShort(t.model) || TOOLS[p] || t.cli || p, pct: pct };
  }
  function count(n) { return n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1000 ? Math.floor(n / 1000) + "k" : String(n); }
  function one(s) { return String(s || "").replace(/\s+/g, " ").trim(); }
  function plural(n, w) { return n + " " + w + (n === 1 ? "" : "s"); }
  function stateLabel(t) {
    if (pulses(t, false)) return "Waiting for you";
    switch (t.state) {
      case "working": return "Working";
      case "waiting": return t.ask_kind === "done" ? "Finished its turn" : t.ask_kind === "error" ? "Stopped on an error" : "Waiting";
      case "blocked": return "Blocked";
      case "idle": return "Idle";
      case "ended": return "Ended";
      default: return t.state ? String(t.state) : "Unknown";
    }
  }
  // The selected row's details: always the same rows, so its height never changes when data arrives. `t` is the
  // list's row, `d` the hub's /api/terminals/{id}/detail (null until it comes). {label, value, title}.
  function detailRows(t, d, now) {
    now = now === undefined ? Date.now() : now;
    var wait = d ? "–" : "…", since = ms(t.state_since), started = ms(t.started), m = rowMeta(t);
    var tool = d && one(d.tool), branch = t.branch && ["HEAD", "main", "master"].indexOf(t.branch) < 0 ? t.branch : "";
    var state = [stateLabel(t), since !== null ? duration((now - since) / 1000) : "",
      t.subagents > 0 ? plural(t.subagents, "subagent") : "", tool ? tool.split(/[\s{(]/)[0] : ""].filter(Boolean).join(" · ");
    var s = d && d.stats, tok = 0;
    if (s && s.tokens) Object.keys(s.tokens).forEach(function (k) { tok += Number(s.tokens[k]) || 0; });
    var totals = s ? [s.dollars > 0 ? "$" + s.dollars.toFixed(2) : "", tok ? count(tok) + " tokens" : "", plural(s.prompts || 0, "prompt"),
      plural(s.tool_calls || 0, "tool call") + (s.tool_errors > 0 ? " (" + s.tool_errors + " failed)" : "")].filter(Boolean).join(" · ") : wait;
    var acct = [t.profile, t.account].filter(Boolean).join(" · ");
    return [
      { label: "Context", value: m.pct === null ? "not reported" : count(t.context_used) + " of " + count(t.context_window) + " · " + m.pct + "%" },
      { label: "State", value: state, title: tool || "" },
      { label: "Where", value: [t.host, folder(t), branch].filter(Boolean).join(" · "), title: [t.host, t.cwd, t.branch].filter(Boolean).join(" · ") },
      { label: "Account", value: acct || "default login" },
      { label: "Totals", value: totals },
      { label: "You", value: d ? one(d.last_prompt) || "–" : wait, title: d ? one(d.last_prompt) : "" },
      { label: "Agent", value: d ? one(d.last_reply) || "–" : wait, title: d ? one(d.last_reply) : "" },
      { label: "Started", value: started !== null ? duration((now - started) / 1000) + " ago" : "–", title: t.started || "" },
    ].map(function (r) { r.title = r.title || r.value; return r; });
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
  // A reply the terminal sends by itself, not the owner's input: device status and cursor position reports
  // (ESC[0n, ESC[12;5R, ESC[?1;1R), device attributes (ESC[?62c, ESC[>0;10;1c), focus in and out (ESC[I, ESC[O)
  // and OSC answers (ESC]…BEL or ESC]…ESC\).
  function isReply(data) {
    return typeof data === "string" && /^(\x1b\[\??\d+(;\d+)*[Rn]|\x1b\[[?>][\d;]*c|\x1b\[\??\d+(;\d+)*\$y|\x1bP[^\x1b]*\x1b\\|\x1b\[[IO]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b\[<\d+;\d+;\d+[Mm]|\x1b\[M[\s\S]{3})+$/.test(data);
  }
  var api = { treeStateKey: treeStateKey, folder: folder, searchText: searchText, isReply: isReply, recentKey: recentKey, byRecent: byRecent, lastActive: lastActive, shortAge: shortAge, dotOpacity: dotOpacity,
    pulses: pulses, treeRows: treeRows, switcher: switcher, modelShort: modelShort, duration: duration, rowMeta: rowMeta, detailRows: detailRows };
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.wsList = api;
})(this);
