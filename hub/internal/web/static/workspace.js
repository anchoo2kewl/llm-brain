// Workspace (terminals.go): many terminals at once. Every open terminal stays attached in the background
// (TermPane), so switching is instant and the sidebar knows who is working and who is waiting for you.
// Panes are slots in a grid; a terminal's element moves between a slot and an off-screen parking spot.
(function () {
  "use strict";
  var grid = document.getElementById("wsgrid"), list = document.getElementById("wslist");
  if (!grid || !list || !window.TermPane) return;
  var KEY = "llmb.workspace.v1", MAX_ATTACHED = 12;
  var COUNTS = { "1": 1, "2": 2, "stack": 2, "3": 3, "4": 4, "6": 6, "9": 9 }, ORDER = ["1", "2", "stack", "3", "4", "6", "9"];
  var narrow = window.matchMedia("(max-width: 760px)");
  var st = load();
  var infos = {}, panes = {}, items = {}, order = [];
  var park = document.createElement("div");
  park.className = "ws-park";
  document.body.appendChild(park);
  var slotEls = [];

  function load() {
    try {
      var s = JSON.parse(localStorage.getItem(KEY) || "{}");
      return { layout: COUNTS[s.layout] ? s.layout : "2", slots: Array.isArray(s.slots) ? s.slots : [], focus: s.focus | 0, zoom: !!s.zoom, sizes: s.sizes && typeof s.sizes === "object" && !Array.isArray(s.sizes) ? s.sizes : {},
        views: s.views && typeof s.views === "object" ? s.views : {} };
    } catch (e) { return { layout: "2", slots: [], focus: 0, zoom: false, sizes: {}, views: {} }; }
  }
  // viewOf: Terminal or Chat for a pane's terminal/session: its own choice, else the viewer's (app.js).
  function viewOf(id) {
    var v = id != null && st.views[id];
    if (v === "term" || v === "chat") return v;
    return window.hubView ? window.hubView.get() : "term";
  }
  function save() { try { localStorage.setItem(KEY, JSON.stringify(st)); } catch (e) { /* private mode */ } }
  function logical() { return narrow.matches ? 1 : COUNTS[st.layout]; } // panes in the layout
  function count() { return st.zoom ? 1 : logical(); }                 // panes on screen
  function el(tag, cls, text) { var e = document.createElement(tag); if (cls) e.className = cls; if (text !== undefined) e.textContent = text; return e; }

  // ------------------------------------------------------------ terminals

  function drop(id) { if (panes[id]) { panes[id].dispose(); delete panes[id]; } }
  // attach connects a terminal; at the cap, force evicts one that isn't on screen.
  function attach(info, force) {
    if (info && info.kind === "session") { if (!panes[info.id]) panes[info.id] = sessionPane(info); return; }
    if (!info || panes[info.id] || !(info.status === "live" || info.status === "starting")) return;
    if (Object.keys(panes).length >= MAX_ATTACHED) {
      if (!force) return;
      var on = st.slots.slice(0, logical()).map(String); // keys are numbers (terminals) or "s<id>" (sessions)
      var off = Object.keys(panes).filter(function (id) { return on.indexOf(id) < 0; });
      if (!off.length) return;
      drop(off[off.length - 1]);
    }
    var p = new window.TermPane({
      id: info.id, host: info.host, status: info.status, parent: park, visible: false,
      isFocused: function () { return document.hasFocus() && st.slots[st.focus] === info.id; },
      onSay: function (t) { var s = shownAt(info.id); if (s >= 0 && slotEls[s]) slotEls[s].msg.textContent = t; },
      onState: function () { paintItem(info.id); paintSlot(shownAt(info.id)); badge(); },
      onInput: function () { noteInput(info.id); },
      onStatus: function (s) {
        if (!s.status) return;
        info.status = s.status; info.error = s.error; paintItem(info.id);
        if (s.status === "ended" || s.status === "failed") setTimeout(function () { drop(info.id); }, 0); // frees its slot in the cap
      }
    });
    p.term.textarea && p.term.textarea.addEventListener("focus", function () { var s = slotOf(info.id); if (s >= 0 && s !== st.focus) focusSlot(s, true); });
    panes[info.id] = p;
  }

  // sessionPane: a live session from elsewhere, as its transcript page (embed mode, refreshes itself).
  // The page loads the first time the pane is shown.
  function sessionPane(info) {
    var wrap = el("div", "termpane-x sesspane"), frame = null, shownView = "";
    park.appendChild(wrap);
    function src() { shownView = viewOf(info.id); return "/sessions/" + info.sid + "?embed=1&view=" + shownView; }
    return {
      el: wrap,
      show: function (on) {
        if (on && !frame) {
          frame = document.createElement("iframe");
          frame.src = src();
          frame.title = info.title;
          wrap.appendChild(frame);
        } else if (on && frame && shownView !== viewOf(info.id)) {
          frame.src = src(); // the view changed while it was parked
        }
      },
      setView: function () { if (frame) frame.src = src(); },
      focus: function () { if (frame) frame.focus(); },
      markSeen: function () {},
      state: function () { return stateOf(info.id); },
      lastLine: function () { return info.updated ? "active " + Math.max(0, Math.round((Date.now() - Date.parse(info.updated)) / 1000)) + "s ago" : ""; },
      stop: function () {},
      dispose: function () { wrap.remove(); }
    };
  }

  function refresh(then) {
    Promise.all([
      fetch("/api/terminals", { cache: "no-store" }).then(function (r) { if (!r.ok) throw new Error("terminals " + r.status); return r.json(); }),
      fetch("/api/live-sessions", { cache: "no-store" }).then(function (r) { return r.ok ? r.json() : []; }).catch(function () { return []; })
    ]).then(function (both) {
      if (!Array.isArray(both[0])) throw new Error("bad terminal list"); // keep the workspace as it is
      var ts = both[0], live = Array.isArray(both[1]) ? both[1] : [];
      // Sessions running elsewhere (the laptop's own terminal, an IDE): keyed "s<id>", shown read-only.
      // One already in a pane stays listed after it goes quiet.
      live.forEach(function (x) { ts.push({ id: "s" + x.id, sid: x.id, kind: "session", title: x.title, host: x.host, cli: x.cli, cwd: x.cwd, status: "elsewhere", updated: x.updated }); });
      Object.keys(infos).forEach(function (k) {
        if (infos[k].kind === "session" && st.slots.indexOf(k) >= 0 && !live.some(function (x) { return "s" + x.id === k; })) ts.push(infos[k]);
      });
      var seen = {}, seenTree = {};
      ts.sort(window.wsList.byRecent(keyOf)); // strictly the latest activity or interaction
      order = ts.map(function (t) { return t.id; });
      ts.forEach(function (t) {
        seen[t.id] = true; seenTree[window.wsList.treeStateKey(t)] = true;
        var old = infos[t.id];
        infos[t.id] = t;
        if (old && old.title !== t.title && slotOf(t.id) >= 0) paintSlot(slotOf(t.id));
        attach(t);
      });
      var pruned = false;
      Object.keys(treeOpen).forEach(function (id) { if (!seenTree[id]) { delete treeOpen[id]; pruned = true; } });
      if (pruned) setPref("ws.treeOpen", treeOpen);
      Object.keys(inputs).forEach(function (id) { if (!seen[id]) delete inputs[id]; });
      Object.keys(infos).forEach(function (id) {
        if (!seen[id]) { delete infos[id]; drop(id); }
        else if (infos[id].status === "ended" || infos[id].status === "failed") drop(id);
      });
      if (!slotEls.length) layout(); // slots exist before any pane reports into them
      if (!st.slots.length || st.slots.every(function (s) { return !infos[s]; })) { // first visit: fill with the newest open ones
        st.slots = order.filter(function (id) { return panes[id]; }).slice(0, COUNTS[st.layout]);
      }
      st.slots = st.slots.map(function (id) { return infos[id] ? id : null; });
      renderList();
      layout();
      document.getElementById("wsempty").hidden = order.length > 0;
      if (then) then();
    }).catch(function () { /* the hub restarting: try again on the next tick */ });
  }

  // ------------------------------------------------------------ sidebar

  var dotTitle = { working: "working", waiting: "waiting for you", idle: "idle", connecting: "connecting", ended: "ended",
    blocked: "blocked: waiting on a macOS permission prompt" };
  function stateOf(id) {
    var p = panes[id], i = infos[id];
    if (!i) return "ended";
    if (i.kind === "session") return Date.now() - Date.parse(i.updated) < 30000 ? "working" : "idle";
    if (i.status === "ended" || i.status === "failed") return "ended";
    if (i.blocked) return "blocked";
    return p ? p.state() : "idle";
  }
  // When the owner last typed or pasted into each terminal from this page (an older hub does not report input).
  // Input times live in memory, as in the Mac app: a reused id after a hub reset never inherits an old time.
  var inputs = {}, treeOpen = pref("ws.treeOpen", {}), endedOpen = pref("ws.endedOpen", false);
  function pref(k, d) { try { var v = JSON.parse(localStorage.getItem(k)); return v === null ? d : v; } catch (e) { return d; } }
  function setPref(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch (e) { /* private mode: in memory only */ } }
  function keyOf(t) { return window.wsList.recentKey(t, inputs[t.id]); }
  var inputTimer = null;
  function noteInput(id) { // at most once a second
    var now = Date.now();
    if (inputs[id] && now - inputs[id] < 1000) return;
    inputs[id] = now;
    if (inputTimer) return; // one update per second while the owner types
    inputTimer = setTimeout(function () {
      inputTimer = null;
      order = order.filter(function (k) { return infos[k]; }); // a row closed meanwhile
      order.sort(function (a, b) { return window.wsList.byRecent(keyOf)(infos[a], infos[b]); });
      renderList();
    }, 1000);
  }
  var calm = window.matchMedia ? window.matchMedia("(prefers-reduced-motion: reduce)") : { matches: false };
  function renderList() {
    var q = (document.getElementById("wsq").value || "").trim().toLowerCase();
    var top = list.scrollTop; // emptying the list would scroll it to the top
    var focused = list.contains(document.activeElement) ? document.activeElement : null; // and drop the focus
    var before = {}; // where each row was: a moved row glides from there
    Object.keys(items).forEach(function (k) { if (items[k].isConnected) before[k] = items[k].getBoundingClientRect().top; });
    function liveOrder() { // the live rows in list order: only a change here moves the view to the selection
      return Array.prototype.filter.call(list.children, function (li) { return li.dataset.id && !li.ended; }).map(function (li) { return li.dataset.id; }).join(",");
    }
    var was = liveOrder(), hadRows = Array.prototype.map.call(list.children, function (li) { return li.dataset.id; }).join(",");
    list.textContent = "";
    var header = false;
    var all = order.map(function (id) { return infos[id]; });
    var live = all.filter(function (t) { return t.status !== "ended" && t.status !== "failed"; });
    var ended = all.filter(function (t) { return t.status === "ended" || t.status === "failed"; });
    // A search shows every match flat; otherwise the session tree, children collapsed until opened.
    // The filter is the quick switcher: names that start with the text first, then the rest that match.
    var rows = q ? window.wsList.switcher(all.map(function (t) { return window.wsList.searchText(t); }), q, all.length)
        .map(function (i) { return { t: all[i], depth: 0, kids: 0, otherHub: "" }; })
      : window.wsList.treeRows(live, treeOpen, keyOf);
    function put(r, inEnded) {
      var t = r.t, id = t.id;
      if (t.kind === "session" && !header && !inEnded && !q) { // a search is one flat list
        header = true;
        var h = el("li", "ws-sect", "Running elsewhere");
        h.setAttribute("role", "presentation");
        h.title = "Claude/Codex sessions started outside the hub (your own terminal, an IDE). Read-only, updates live.";
        list.appendChild(h);
      }
      var li = items[id] || makeItem(id);
      li.dataset.id = id;
      list.appendChild(li);
      paintItem(id);
      li.style.paddingLeft = r.depth ? 8 + r.depth * 16 + "px" : "";
      li.parts.kids.hidden = !r.kids;
      li.parts.kids.textContent = (treeOpen[window.wsList.treeStateKey(infos[id])] ? "▾ " : "▸ ") + r.kids;
      li.parts.kids.title = treeOpen[window.wsList.treeStateKey(infos[id])] ? "Hide the child sessions (←)" : "Show the " + r.kids + " child session" + (r.kids === 1 ? "" : "s") + " (→)";
      li.parts.kids.setAttribute("aria-expanded", treeOpen[window.wsList.treeStateKey(infos[id])] ? "true" : "false");
      li.parts.hub.textContent = r.otherHub;
      li.parts.hub.hidden = !r.otherHub;
      li.kids = r.kids;
      li.ended = !!inEnded;
    }
    rows.forEach(function (r) { put(r, false); });
    // Ended and failed sessions leave the live list: one collapsed group at the bottom.
    var eh = null;
    if (ended.length && !q) {
      eh = el("li", "ws-sect ws-ended", (endedOpen ? "▾ " : "▸ ") + "Ended (" + ended.length + ")");
      eh.setAttribute("role", "button"); eh.tabIndex = 0; eh.setAttribute("aria-expanded", endedOpen ? "true" : "false");
      eh.onclick = eh.onkeydown = function (e) {
        if (e.type === "keydown" && e.key !== "Enter" && e.key !== " ") return;
        e.preventDefault(); endedOpen = !endedOpen; setPref("ws.endedOpen", endedOpen); renderList();
      };
      list.appendChild(eh);
      if (endedOpen) ended.forEach(function (t) { put({ t: t, depth: 0, kids: 0, otherHub: "" }, true); });
    }
    list.scrollTop = top;
    if (focused && list.contains(focused) && document.activeElement !== focused) focused.focus({ preventScroll: true });
    else if (focused && focused.classList.contains("ws-ended") && eh) eh.focus({ preventScroll: true }); // a new header element
    if (Array.prototype.map.call(list.children, function (li) { return li.dataset.id; }).join(",") === hadRows) return;
    // The live order changed: the selected row stays in view (before the glide is measured). Then each moved row
    // glides to its new place (none with reduced motion).
    var on = list.querySelector(".ws-item.on");
    if (on && !q && liveOrder() !== was) on.scrollIntoView({ block: "nearest" });
    if (!calm.matches) Object.keys(before).forEach(function (k) {
      var li = items[k];
      if (!li || !li.isConnected) return;
      var dy = before[k] - li.getBoundingClientRect().top;
      if (!dy) return;
      li.style.transition = "none"; li.style.transform = "translateY(" + dy + "px)";
      li.getBoundingClientRect(); // apply the start position before the transition
      li.style.transition = "transform 200ms ease"; li.style.transform = "";
      setTimeout(function () { li.style.transition = ""; }, 250); // back to the CSS transition
    });
  }
  function setTree(id, open) {
    if (open) treeOpen[window.wsList.treeStateKey(infos[id])] = true; else delete treeOpen[window.wsList.treeStateKey(infos[id])];
    setPref("ws.treeOpen", treeOpen); renderList(); if (items[id]) items[id].focus();
  }
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", "Terminals");
  // Up and Down move the selection through the list, like a native sidebar.
  list.addEventListener("keydown", function (e) {
    if (["ArrowDown", "ArrowUp", "ArrowRight", "ArrowLeft"].indexOf(e.key) < 0) return;
    var rows = Array.prototype.filter.call(list.children, function (li) { return li.classList.contains("ws-item"); });
    var at = rows.indexOf(e.target); // only from a row itself: not while renaming, not from its buttons
    if (at < 0) return;
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") { // open or close a parent's child sessions
      var pid = order.filter(function (k) { return items[k] === e.target; })[0];
      if (pid === undefined || !e.target.kids || !!treeOpen[window.wsList.treeStateKey(infos[pid])] === (e.key === "ArrowRight")) return;
      e.preventDefault();
      setTree(pid, e.key === "ArrowRight");
      return;
    }
    var next = rows[at + (e.key === "ArrowDown" ? 1 : -1)];
    if (!next) return;
    e.preventDefault();
    var id = order.filter(function (k) { return items[k] === next; })[0]; // the id as the list keeps it (a number)
    if (id !== undefined) show(id);
    next.focus({ preventScroll: false });
  });
  // Two short lines per row (docs/design.md): dot, name and age; then the tool, the model and the context used.
  // The selected row adds the context bar and the details grid. The rest is in the tooltip.
  function makeItem(id) {
    var li = el("li", "ws-item");
    li.tabIndex = 0;
    li.setAttribute("role", "option");
    var top = el("div", "ws-it-top"), dot = el("span", "dot-s"), title = el("span", "ws-it-title");
    var num = el("span", "ws-it-num"), age = el("span", "ws-it-age"), prov = el("span", "ws-prov");
    var hub = el("span", "ws-it-hub"); hub.hidden = true; hub.title = "This child session runs on this hub";
    var kids = el("button", "ws-kids"); kids.type = "button"; kids.hidden = true;
    kids.addEventListener("click", function (e) { e.stopPropagation(); setTree(id, !treeOpen[window.wsList.treeStateKey(infos[id])]); });
    var pen = el("button", "ws-pen", "✎"); pen.type = "button"; pen.title = "Rename";
    pen.addEventListener("click", function (e) { e.stopPropagation(); rename(id, title); });
    var prev = el("div", "ws-it-prev"); // only a close error, for a moment
    var x = closeBtn(function () { return id; }, "ws-pen ws-close", function (msg) { prev.textContent = msg; setTimeout(function () { prev.textContent = ""; }, 6000); });
    // The age and the buttons share one place: the buttons show over the age on hover or focus (no dead space).
    var acts = el("span", "ws-acts"), btns = el("span", "ws-btns");
    btns.append(pen, x); acts.append(age, btns);
    top.append(dot, title, hub, el("span", "grow"), kids, num, acts);
    var meta = el("div", "ws-it-meta"), model = el("span", "ws-it-model"), pct = el("span", "ws-it-pct");
    meta.append(prov, model, pct);
    var det = el("div", "ws-det"), bar = el("div", "ws-bar"), fill = el("i"), grid = el("dl"), cells = []; // cells update in place: a tooltip stays open
    bar.setAttribute("role", "meter"); bar.setAttribute("aria-label", "Context used"); bar.setAttribute("aria-valuemin", "0"); bar.setAttribute("aria-valuemax", "100");
    bar.append(fill); det.append(bar, grid); det.hidden = true;
    li.append(top, meta, det, prev);
    li.addEventListener("click", function () { show(id); });
    if (infos[id].kind !== "session") {
      li.draggable = true;
      li.addEventListener("dragstart", function (e) { e.dataTransfer.setData("text/x-brain-terminal", id); e.dataTransfer.effectAllowed = "copyMove"; });
    }
    li.addEventListener("keydown", function (e) { if (e.key === "Enter") show(id); });
    title.addEventListener("dblclick", function (e) { e.stopPropagation(); rename(id, title); });
    items[id] = li;
    li.parts = { dot: dot, title: title, model: model, pct: pct, det: det, bar: bar, fill: fill, grid: grid, cells: cells, prev: prev, num: num, close: x, age: age, kids: kids, hub: hub, prov: prov };
    return li;
  }
  // The selected row's /detail from the hub, asked again every 5 seconds while it shows.
  var details = {};
  function loadDetail(id) {
    if (!infos[id] || infos[id].kind === "session") return;
    function got(d) { if (infos[id] && (d || !details[id])) { details[id] = d || {}; paintItem(id); } } // a failure keeps the last answer, else shows "–"
    fetch("/api/terminals/" + encodeURIComponent(id) + "/detail", { cache: "no-store" }).then(function (r) { return r.ok ? r.json() : null; })
      .then(got).catch(function () { got(null); });
  }
  setInterval(function () { order.forEach(function (id) { if (slotOf(id) === st.focus) { paintItem(id); loadDetail(id); } }); }, 5000);
  var seenIds = {}, seenKey = {};
  function paintItem(id) {
    var li = items[id], t = infos[id];
    if (!li || !t) return;
    var s = stateOf(id), p = panes[id];
    var on = slotOf(id) === st.focus;
    li.className = "ws-item s-" + s + (t.kind === "session" ? " ext" : "") + (on ? " on" : slotOf(id) >= 0 ? " shown" : "");
    li.setAttribute("aria-selected", on ? "true" : "false");
    // Opened since its state last changed: it stops pulsing. A new question makes it pulse again.
    var key = [t.state || "", t.ask_kind || "", t.blocked ? 1 : 0, t.needs_owner ? 1 : 0, t.ask || ""].join("/");
    if (seenKey[id] !== key) { seenKey[id] = key; delete seenIds[id]; }
    if (on) seenIds[id] = true;
    var when = window.wsList.lastActive(t, inputs[id]), pulse = window.wsList.pulses(t, on || seenIds[id]);
    li.parts.dot.title = pulse ? "waiting for you" : dotTitle[s] || s;
    li.parts.dot.className = "dot-s " + s + (pulse ? " needs" : "");
    li.parts.dot.style.opacity = pulse || s === "working" ? "" : String(window.wsList.dotOpacity(when));
    li.parts.age.textContent = window.wsList.shortAge(when);
    if (li.parts.title.getAttribute("contenteditable") !== "true") li.parts.title.textContent = t.title || "terminal " + id;
    var m = window.wsList.rowMeta(t);
    li.parts.prov.textContent = m.mark; li.parts.prov.title = m.tool;
    li.parts.model.textContent = m.model;
    li.parts.pct.textContent = m.pct === null ? "" : m.pct + "%";
    li.parts.pct.title = "Context used";
    li.parts.pct.className = "ws-it-pct" + (m.pct >= 90 ? " hi" : "");
    if (on && li.parts.det.hidden) loadDetail(id); // just selected: fresh numbers now
    li.parts.det.hidden = !on;
    if (on) {
      li.parts.fill.style.width = (m.pct || 0) + "%";
      li.parts.fill.className = m.pct >= 90 ? "hi" : m.pct >= 70 ? "mid" : "";
      li.parts.bar.setAttribute("aria-valuenow", String(m.pct || 0));
      li.parts.bar.setAttribute("aria-valuetext", m.pct === null ? "not reported" : m.pct + "%");
      var known = t.state ? t : Object.assign({}, t, { state: s }); // an older hub sends no state: the pane's
      window.wsList.detailRows(known, t.kind === "session" ? {} : details[id] || null).forEach(function (r, i) { // no /detail outside the hub
        if (!li.parts.cells[i]) { li.parts.cells[i] = [el("dt"), el("dd")]; li.parts.grid.append(li.parts.cells[i][0], li.parts.cells[i][1]); }
        var dt = li.parts.cells[i][0], dd = li.parts.cells[i][1];
        if (dt.textContent !== r.label) dt.textContent = r.label;
        if (dd.textContent !== r.value) dd.textContent = r.value;
        if (dd.title !== r.title) dd.title = r.title;
      });
    }
    li.parts.close.sync();
    var slot = slotOf(id);
    li.parts.num.hidden = true; // the pane header shows its number; the tooltip says it here
    // Everything the one line leaves out.
    var ask = s === "ended" ? (t.error || t.status) : s === "blocked" ? "Waiting on a macOS permission prompt on " + shortHost(t.host)
      : t.ask ? t.ask : p ? p.lastLine() : "";
    li.title = [(t.title || "terminal " + id) + " · " + (pulse ? "waiting for you" : dotTitle[s] || s), ask,
      (t.kind === "session" ? "Outside the hub, read-only · " : "") + t.cli + " on " + shortHost(t.host) + " · " + t.cwd,
      slot >= 0 ? "In pane " + (slot + 1) : ""].filter(Boolean).join("\n");
  }
  // badgesFor: where it runs. Hub terminals: the Mac. Sessions started elsewhere: OUTSIDE HUB, the Mac, read-only.
  function shortHost(h) { return /macbook/i.test(h) ? "MacBook" : /studio/i.test(h) ? "Studio" : h; }
  function badgesFor(box, t) {
    box.textContent = "";
    if (t.kind === "session") {
      box.appendChild(el("span", "bdg ext", "OUTSIDE HUB"));
      box.appendChild(el("span", "bdg host", shortHost(t.host)));
      box.appendChild(el("span", "bdg ro", "read-only"));
    } else {
      box.appendChild(el("span", "bdg host", shortHost(t.host))); // a hub terminal is the normal case: no "HUB" chip
    }
  }
  function badge() {
    var n = order.filter(function (id) { return stateOf(id) === "waiting"; }).length;
    document.title = (n ? "(" + n + ") " : "") + "Workspace · llm-brain hub";
  }
  setInterval(function () { order.forEach(function (id) { if (stateOf(id) === "working") paintItem(id); }); }, 2000);
  setInterval(function () { order.forEach(paintItem); }, 30000); // the "2m" ages and the dot's fade

  function rename(id, span) {
    span.setAttribute("contenteditable", "true");
    span.focus();
    document.getSelection().selectAllChildren(span);
    function done(ok) {
      span.removeAttribute("contenteditable");
      var title = span.textContent.trim();
      if (!ok || !title || title === infos[id].title) { span.textContent = infos[id].title; return; }
      var body = new URLSearchParams(); body.set("title", title);
      fetch(infos[id].kind === "session" ? "/sessions/" + infos[id].sid + "/rename" : "/terminals/" + id + "/rename", { method: "POST", body: body }).then(function (r) { return r.json(); }).then(function (j) {
        if (j.title) { infos[id].title = j.title; paintSlot(shownAt(id)); }
        paintItem(id);
      });
    }
    span.onkeydown = function (e) { if (e.key === "Enter") { e.preventDefault(); span.onkeydown = null; span.onblur = null; done(true); } if (e.key === "Escape") { span.onkeydown = null; span.onblur = null; done(false); } };
    span.onblur = function () { span.onkeydown = null; span.onblur = null; done(true); };
  }

  // ------------------------------------------------------------ panes

  function slotOf(id) { for (var i = 0; i < logical(); i++) if (st.slots[i] === id) return i; return -1; } // its pane in the layout
  function shownAt(id) { var i = slotOf(id); return st.zoom ? (i === st.focus ? 0 : -1) : i; }       // the slot it's drawn in
  function layout() {
    var n = count();
    grid.setAttribute("data-layout", narrow.matches || st.zoom ? "1" : st.layout);
    applySizes();
    document.querySelectorAll(".ws-layouts button").forEach(function (b) { b.classList.toggle("on", b.getAttribute("data-layout") === st.layout); });
    if (st.focus >= logical()) st.focus = 0;
    while (slotEls.length < n) slotEls.push(makeSlot(slotEls.length));
    slotEls.forEach(function (s, i) { s.root.hidden = i >= n; });
    // Zoomed: show the focused pane's terminal in slot 0.
    var shown = {};
    for (var i = 0; i < n; i++) {
      var id = st.zoom ? st.slots[st.focus] : st.slots[i];
      var s = slotEls[i];
      s.root.classList.toggle("focus", st.zoom || i === st.focus);
      if (id && panes[id] && !shown[id]) {
        shown[id] = true;
        if (panes[id].el.parentNode !== s.body) s.body.appendChild(panes[id].el);
        panes[id].show(true);
        s.empty.hidden = true;
      } else {
        s.empty.hidden = false;
        fillEmpty(s, i);
      }
      if (s.id !== id && s.hideEditors) s.hideEditors(); // another terminal here: its editor isn't this one's
      s.id = id;
      paintSlot(i);
    }
    Object.keys(panes).forEach(function (id) {
      if (!shown[id] && panes[id].el.parentNode !== park) { park.appendChild(panes[id].el); panes[id].show(false); }
    });
    save();
    order.forEach(paintItem);
  }
  function makeSlot(i) {
    var root = el("section", "ws-slot"), head = el("header", "ws-slot-head"), body = el("div", "ws-slot-body"), msg = el("div", "ws-slot-msg");
    var num = el("span", "ws-num", String(i + 1)), dot = el("span", "dot-s"), title = el("span", "ws-slot-title"), meta = el("span", "ws-slot-meta"), badges = el("span", "ws-badges");
    var grow = el("span", "grow");
    var grip = el("span", "ws-grip", "⠿"); grip.draggable = true;
    grip.title = "Drag onto a machine to move this terminal there (hold ⌥/Alt to clone it)";
    grip.addEventListener("dragstart", function (e) {
      var id = s0.s && s0.s.id;
      if (!id || !infos[id] || infos[id].kind === "session") { e.preventDefault(); return; }
      e.dataTransfer.setData("text/x-brain-terminal", id); e.dataTransfer.effectAllowed = "copyMove";
    });
    var open = el("a", "ws-btn", "↗"); open.title = "Open on its own page"; open.target = "_blank"; open.rel = "noopener";
    var code = el("button", "ws-btn", "</>"); code.type = "button"; code.title = "Edit this folder's files (toggle)";
    var zoom = el("button", "ws-btn ws-zoom", "⤢"); zoom.type = "button"; zoom.title = "Zoom (Ctrl+Shift+Enter)";
    var clear = el("button", "ws-btn", "✕"); clear.type = "button"; clear.title = "Take it out of this pane (it keeps running)";
    var s0 = {};
    var close = closeBtn(function () { return s0.s && s0.s.id; }, "ws-btn ws-close", function (m) { msg.textContent = m; });
    // Terminal | Chat for this pane only (it overrides the viewer's choice).
    var seg = el("span", "seg viewseg ws-viewseg");
    seg.setAttribute("role", "group"); seg.setAttribute("aria-label", "Show as");
    var vt = el("button", "", "Term"), vc = el("button", "", "Chat");
    vt.type = vc.type = "button"; vt.title = "Terminal: as the CLI shows it"; vc.title = "Chat: cards, rendered markdown";
    vt.dataset.view = "term"; vc.dataset.view = "chat";
    seg.append(vt, vc);
    [vt, vc].forEach(function (b) {
      b.addEventListener("click", function (e) {
        e.stopPropagation();
        var id = s0.s && s0.s.id;
        if (!id) return;
        st.views[id] = b.dataset.view; save();
        if (panes[id] && panes[id].setView) panes[id].setView();
        paintSlot(s0.s.i);
      });
    });
    // A hub terminal's Chat view: its transcript over the live terminal (which keeps its size), and a line
    // that still types into the terminal.
    var chat = el("div", "ws-chat"), chatFrame = el("iframe"), chatNote = el("p", "ws-chat-note mut small");
    chatFrame.title = "Transcript";
    var say = el("form", "ws-chat-say"), sayIn = el("input"), sayBtn = el("button", "primary small", "Send");
    sayIn.placeholder = "Type into the terminal…"; sayIn.setAttribute("aria-label", "Send to the terminal"); sayIn.autocomplete = "off"; sayIn.enterKeyHint = "send";
    sayBtn.type = "submit";
    say.append(sayIn, sayBtn);
    chat.append(chatFrame, chatNote, say);
    chat.hidden = true;
    say.addEventListener("submit", function (e) {
      e.preventDefault();
      var id = s0.s && s0.s.id;
      if (!id || !sayIn.value) return;
      fetch("/api/terminals/" + id + "/send", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ text: sayIn.value, keys: ["Enter"] }) })
        .then(function (r) { if (r.ok) sayIn.value = ""; else msg.textContent = "Couldn't send to the terminal"; })
        .catch(function () { msg.textContent = "Couldn't send to the terminal"; });
    });
    head.append(grip, num, dot, title, badges, meta, grow, seg, code, open, zoom, close, clear);
    var empty = el("div", "ws-slot-empty");
    // Frozen on a macOS prompt: what happened, what to do, and Restart CLI (two taps).
    var stuck = el("div", "ws-stuck"), stuckText = el("p", "small"), stuckActs = el("div", "ws-stuck-acts");
    var restart = el("button", "small primary", "Restart CLI…"), screen = el("a", "btn small", "Screen Sharing ↗"), armedR = null;
    restart.type = "button"; restart.title = "End the frozen CLI and start it again in this terminal, continuing its conversation";
    stuckActs.append(screen, restart);
    stuck.setAttribute("role", "alert");
    stuck.append(stuckText, stuckActs);
    stuck.hidden = true;
    function disarmR() { clearTimeout(armedR); armedR = null; restart.textContent = "Restart CLI…"; restart.classList.remove("armed"); }
    restart.addEventListener("click", function (e) {
      e.stopPropagation();
      var id = s0.s && s0.s.id;
      if (!id) return;
      if (!armedR) { restart.textContent = "Restart?"; restart.classList.add("armed"); armedR = setTimeout(disarmR, 4000); return; }
      disarmR();
      fetch("/api/terminals/" + id + "/restart", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })
        .then(function (r) { return r.json().catch(function () { return {}; }).then(function (j) { return { ok: r.ok, j: j }; }); })
        .then(function (x) { msg.textContent = x.ok ? "Restarting…" : "Restart failed: " + (x.j.error || "error"); if (x.ok) setTimeout(refresh, 3000); })
        .catch(function () { msg.textContent = "Restart failed: the hub didn't answer"; });
    });
    root.append(head, stuck, body, chat, msg, empty);
    grid.appendChild(root);
    var s = { root: root, head: head, body: body, msg: msg, empty: empty, dot: dot, title: title, meta: meta, badges: badges, open: open, close: close, i: i,
      seg: seg, chat: chat, chatFrame: chatFrame, chatNote: chatNote, chatSrc: "", stuck: stuck, stuckText: stuckText, screen: screen };
    s0.s = s;
    root.addEventListener("mousedown", function () { if (!st.zoom && s.i !== st.focus) focusSlot(s.i, false); });
    title.title = "Click to rename";
    title.addEventListener("click", function (e) { e.stopPropagation(); if (title.getAttribute("contenteditable") !== "true" && s.id && infos[s.id]) rename(s.id, title); });
    zoom.onclick = function (e) { e.stopPropagation(); focusSlot(s.i, false); toggleZoom(); };
    // </>: the editor on this pane's folder, over the terminal; again to go back. One editor per pane, kept
    // while it's open so switching back and forth is instant.
    // One editor per folder, kept (hidden) when the pane moves on, so unsaved work is never navigated away.
    s.editors = {};
    s.hideEditors = function () { Object.keys(s.editors).forEach(function (k) { s.editors[k].hidden = true; }); code.classList.remove("on"); };
    code.onclick = function (e) {
      e.stopPropagation();
      var t = s.id && infos[s.id];
      if (!t) return;
      var src = "/edit?embed=1&host=" + encodeURIComponent(t.host) + "&root=" + encodeURIComponent(t.cwd);
      var ed = s.editors[src];
      if (ed && !ed.hidden) { s.hideEditors(); return; }
      s.hideEditors();
      if (!ed) { ed = document.createElement("iframe"); ed.className = "ws-editor"; ed.title = "Editor"; ed.src = src; root.appendChild(ed); s.editors[src] = ed; }
      ed.hidden = false; code.classList.add("on");
    };
    clear.onclick = function (e) { e.stopPropagation(); var at = st.zoom ? st.focus : s.i; st.slots[at] = null; st.zoom = false; layout(); };
    return s;
  }
  // closeBtn: × ends the terminal or session (its CLI process on its machine) in one click. Its label names
  // the session, so a screen reader says what it ends; it shows on hover and on keyboard focus.
  function closeBtn(idOf, cls, say) {
    var b = el("button", cls, "×"), busy = false;
    b.type = "button";
    b.sync = function () {
      var id = idOf(), t = id && infos[id], n = t ? (t.title || "terminal " + id) : "terminal";
      b.setAttribute("aria-label", "End " + n);
      b.title = "End " + n + ": stops its CLI on " + (t ? shortHost(t.host) : "its machine");
    };
    b.sync();
    b.addEventListener("focus", b.sync);
    b.addEventListener("mouseenter", b.sync);
    b.addEventListener("click", function (e) {
      e.stopPropagation();
      var id = idOf(), t = id && infos[id];
      if (!t || busy) return;
      busy = true; b.disabled = true;
      var url = t.kind === "session" ? "/api/sessions/" + t.sid + "/close" : "/api/terminals/" + id + "/close";
      fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })
        .then(function (r) { return r.json().catch(function () { return {}; }).then(function (j) { return { ok: r.ok, j: j }; }); })
        .then(function (x) {
          if (!x.ok) { say("End failed: " + (x.j.error || "error")); return; }
          say(x.j.closing === "session" ? "Ending on " + shortHost(t.host) + "…" : "Ending…");
          refresh();
        })
        .catch(function () { say("End failed: the hub didn't answer"); })
        .then(function () { busy = false; b.disabled = false; });
    });
    return b;
  }
  function fillEmpty(s, i) {
    s.empty.textContent = "";
    var free = order.filter(function (id) { return infos[id] && (infos[id].status === "live" || infos[id].status === "starting" || infos[id].kind === "session") && st.slots.slice(0, logical()).indexOf(id) < 0; });
    s.empty.appendChild(el("p", "mut", free.length ? "Pick a terminal for pane " + (i + 1) + ":" : "Every terminal is already on screen."));
    free.slice(0, 8).forEach(function (id) {
      var b = el("button", "chip", (infos[id].title || "terminal " + id).slice(0, 60));
      b.type = "button";
      b.onclick = function () { attach(infos[id], true); st.slots[i] = id; st.focus = i; layout(); focusSlot(i, true); };
      s.empty.appendChild(b);
    });
    var nb = el("button", "primary small", "+ New terminal here");
    nb.type = "button";
    nb.onclick = function () { st.focus = i; openNew(); };
    s.empty.appendChild(nb);
  }
  function paintSlot(i) {
    if (i < 0 || !slotEls[i]) return;
    var s = slotEls[i], id = s.id, t = id && infos[id];
    s.close.sync();
    s.root.classList.toggle("ext", !!(t && t.kind === "session"));
    var v = viewOf(id);
    s.seg.hidden = !t;
    [].forEach.call(s.seg.children, function (b) { var on = b.dataset.view === v; b.classList.toggle("on", on); b.setAttribute("aria-pressed", on ? "true" : "false"); });
    // A hub terminal in Chat view: its transcript over it.
    var chatOn = !!(t && t.kind !== "session" && v === "chat");
    s.chat.hidden = !chatOn;
    if (chatOn) {
      var src = t.session_id ? "/sessions/" + t.session_id + "?embed=1&view=chat" : "";
      if (src !== s.chatSrc) { s.chatSrc = src; s.chatFrame.hidden = !src; if (src) s.chatFrame.src = src; else s.chatFrame.removeAttribute("src"); }
      s.chat.style.top = s.head.offsetHeight + "px";
      s.chatNote.textContent = src ? "" : (t.provider === "shell" ? "A shell has no transcript." : "No transcript yet: it shows after the first message.") + " Lines you send still go to the terminal.";
    }
    var blocked = !!(t && t.kind !== "session" && t.blocked && t.status === "live");
    s.stuck.hidden = !blocked;
    s.root.classList.toggle("blocked", blocked);
    if (blocked) {
      s.stuckText.textContent = t.fix || "Its CLI is waiting on a macOS permission prompt on " + t.host + ".";
      s.screen.href = "vnc://" + encodeURIComponent(t.host);
    }
    if (!t) { s.title.textContent = "empty"; s.meta.textContent = ""; s.badges.textContent = ""; s.dot.className = "dot-s"; s.open.removeAttribute("href"); s.msg.textContent = ""; return; }
    badgesFor(s.badges, t);
    var stt = stateOf(id);
    if (s.title.getAttribute("contenteditable") !== "true") s.title.textContent = t.title || "terminal " + id;
    s.meta.textContent = t.cli + " · " + t.cwd;
    s.dot.className = "dot-s " + stt;
    s.dot.title = dotTitle[stt] || stt;
    s.open.href = t.kind === "session" ? "/sessions/" + t.sid : "/terminals/" + id;
    s.root.classList.toggle("waiting", stt === "waiting");
  }
  function focusSlot(i, focusTerm) {
    var moved = st.focus !== i;
    st.focus = i;
    if (st.zoom && moved) layout(); // zoomed: the one pane on screen follows the focus
    slotEls.forEach(function (s, j) { s.root.classList.toggle("focus", st.zoom ? j === 0 : j === i); });
    var id = st.slots[i];
    if (id && panes[id]) { panes[id].markSeen(); if (focusTerm) panes[id].focus(); }
    save();
    order.forEach(paintItem);
    badge();
  }
  // show: put a terminal in the focused pane (or go to its pane if it's on screen already).
  function show(id) {
    var at = slotOf(id);
    if (at >= 0) { focusSlot(at, true); return; } // already in a pane (zoomed: focusSlot brings it up)
    if (!panes[id]) attach(infos[id], true);
    if (!panes[id]) return;
    st.slots[st.focus] = id;
    layout();
    focusSlot(st.focus, true);
  }
  function toggleZoom() { st.zoom = !st.zoom; layout(); focusSlot(st.focus, true); }
  function setLayout(l) { st.layout = l; st.zoom = false; layout(); focusSlot(Math.min(st.focus, logical() - 1), true); }
  function step(d) { // previous / next terminal in the focused pane
    var live = order.filter(function (id) { return panes[id]; });
    if (!live.length) return;
    var cur = live.indexOf(st.slots[st.focus]);
    show(live[(cur + d + live.length) % live.length]);
  }

  // ------------------------------------------------------------ keys

  var sidebarButton = document.getElementById("ws-sidebar-toggle");
  function toggleSidebar() {
    var collapsed = document.getElementById("ws").classList.toggle("sidebar-collapsed");
    sidebarButton.setAttribute("aria-expanded", String(!collapsed));
    sidebarButton.textContent = collapsed ? "Show sidebar" : "Hide sidebar";
  }
  sidebarButton.onclick = toggleSidebar;

  // Read at most nine rectangles, only when a directional action is requested.
  function adjacent(direction) {
    if (st.zoom || logical() < 2) return -1;
    var origin = slotEls[st.focus].root.getBoundingClientRect();
    var horizontal = direction === "left" || direction === "right";
    var forward = direction === "right" || direction === "down";
    var start = horizontal ? "left" : "top", end = horizontal ? "right" : "bottom";
    var crossStart = horizontal ? "top" : "left", crossEnd = horizontal ? "bottom" : "right";
    var best = -1, distance = Infinity, overlap = 0;
    for (var i = 0; i < logical(); i++) {
      if (i === st.focus) continue;
      var box = slotEls[i].root.getBoundingClientRect();
      var gap = forward ? box[start] - origin[end] : origin[start] - box[end];
      var shared = Math.min(box[crossEnd], origin[crossEnd]) - Math.max(box[crossStart], origin[crossStart]);
      if (gap >= 0 && shared > 0 && (gap < distance || (gap === distance && shared > overlap))) {
        best = i; distance = gap; overlap = shared;
      }
    }
    return best;
  }

  function applySizes() {
    var sizes = !st.zoom && !narrow.matches && st.sizes[st.layout];
    ["gridTemplateColumns", "gridTemplateRows"].forEach(function (axis) {
      var values = sizes && sizes[axis];
      grid.style[axis] = Array.isArray(values) && values.length <= 3 &&
        values.every(function (n) { return Number.isFinite(n) && n > 0; })
        ? values.map(function (n) { return "minmax(0," + n + "fr)"; }).join(" ") : "";
    });
  }

  function resizeTrack(direction) {
    var dest = adjacent(direction);
    if (dest < 0) return;
    var horizontal = direction === "left" || direction === "right";
    var axis = horizontal ? "gridTemplateColumns" : "gridTemplateRows";
    var values = getComputedStyle(grid)[axis].split(/\s+/).map(parseFloat);
    if (values.length < 2 || !values.every(Number.isFinite)) return;
    var key = horizontal ? "left" : "top";
    var starts = slotEls.slice(0, logical()).map(function (slot) {
      return slot.root.getBoundingClientRect()[key];
    });
    var tracks = Array.from(new Set(starts)).sort(function (a, b) { return a - b; });
    var from = tracks.indexOf(starts[dest]), to = tracks.indexOf(starts[st.focus]);
    if (tracks.length !== values.length || from === to) return;
    var delta = Math.min((values[from] + values[to]) * 0.05, values[from] - 48);
    if (delta <= 0) return;
    values[from] -= delta;
    values[to] += delta;
    if (!st.sizes[st.layout] || typeof st.sizes[st.layout] !== "object") st.sizes[st.layout] = {};
    st.sizes[st.layout][axis] = values;
    applySizes();
    save();
  }

  function runAction(action) {
    if (action === "new") return openNew();
    if (action === "previous" || action === "next") return step(action === "next" ? 1 : -1);
    if (action === "auto") return setLayout(grid.clientWidth >= grid.clientHeight ? "2" : "stack");
    if (action === "side" || action === "stack") return setLayout(action === "side" ? "2" : "stack");
    if (action === "zoom") return toggleZoom();
    var id = st.slots[st.focus];
    if (action === "rename") {
      var slot = slotEls[st.zoom ? 0 : st.focus];
      if (slot && infos[id]) rename(id, slot.title);
      return;
    }
    var parts = action.split("-");
    if (action === "balance") { delete st.sizes[st.layout]; applySizes(); save(); return; }
    if (parts[0] === "resize" && slotEls[st.focus]) return resizeTrack(parts[1]);
    if (!slotEls[st.focus] || (parts[0] !== "move" && parts[0] !== "focus")) return;
    var dest = adjacent(parts[1]);
    if (dest < 0) return;
    if (parts[0] === "move") {
      st.slots[st.focus] = st.slots[dest] || null;
      st.slots[dest] = id || null;
      layout();
    }
    focusSlot(dest, true);
  }

  // Move to another machine: drag a terminal onto a machine in the sidebar, or Actions → Move focused terminal.
  // The hub runs the move as a job (it goes on if this page closes) and this shows its phases as they happen;
  // the source ends only after the resumed copy is live. Only the conversation moves, not uncommitted files.
  var moveBox = document.getElementById("wsmove"), moveList = document.getElementById("wsmachines"), moveSay = document.getElementById("wsmovestatus");
  var machines = [], moving = false, PHASE = { check: "Check readiness", "export": "Export the conversation", copy: "Copy it to ", resume: "Resume on ", verify: "Check it is live", close: "End the source" };
  var canMove = null; // the hub serves moves: its unknown-job reply is JSON
  function loadMachines() {
    if (canMove === null) {
      fetch("/api/migrations/-", { cache: "no-store" }).then(function (r) { canMove = /json/.test(r.headers.get("Content-Type") || ""); if (canMove) loadMachines(); }).catch(function () {});
      return;
    }
    if (!canMove) return;
    fetch("/api/cluster/nodes", { cache: "no-store" }).then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; }).then(function (n) {
      var names = n && n.self ? [n.self.node].concat((n.peers || []).filter(function (p) { return p.ok; }).map(function (p) { return p.info.node; })) : [];
      order.forEach(function (id) { var t = infos[id]; if (t && t.kind !== "session" && t.host && names.indexOf(t.host) < 0) names.push(t.host); });
      machines = names;
      paintMachines();
    });
  }
  function paintMachines() {
    moveBox.hidden = machines.length < 2;
    moveList.textContent = "";
    machines.forEach(function (m) {
      var li = el("li", "ws-machine", shortHost(m));
      li.title = "Drop a terminal here to move its conversation to " + m + " (hold ⌥/Alt to clone: the source keeps running)";
      li.addEventListener("dragover", function (e) { if (e.dataTransfer.types.indexOf("text/x-brain-terminal") >= 0) { e.preventDefault(); li.classList.add("over"); } });
      li.addEventListener("dragleave", function () { li.classList.remove("over"); });
      li.addEventListener("drop", function (e) { e.preventDefault(); li.classList.remove("over"); migrate(e.dataTransfer.getData("text/x-brain-terminal"), m, e.altKey); });
      moveList.appendChild(li);
    });
    document.querySelectorAll(".ws-move-group").forEach(function (g) { g.remove(); });
    if (machines.length < 2) return;
    [["move", "Move focused terminal to"], ["clone", "Clone focused terminal to (keeps the source)"]].forEach(function (k) {
      var g = el("optgroup"); g.className = "ws-move-group"; g.label = k[1];
      machines.forEach(function (m) { var o = el("option", "", shortHost(m)); o.value = k[0] + ":" + m; g.appendChild(o); });
      document.getElementById("ws-actions").appendChild(g);
    });
  }
  function migrate(id, to, clone) {
    var t = infos[id];
    if (!t || moving) return;
    if (t.kind === "session") { sayMove("Only hub terminals can move. Continue this session in a terminal first."); return; }
    if (t.host === to) { sayMove((t.title || "This terminal") + " already runs on " + shortHost(to) + "."); return; }
    var folder = window.prompt("Optional folder on " + shortHost(to) + ":\nLeave blank for the pinned/automatic folder. Enter ~ to use its home folder.\nCode files are not copied.", "");
    if (folder === null) return;
    folder = folder.trim();
    var body = clone ? { to: to, clone: true } : { to: to, close_source: true };
    if (folder === "~") body.without_folder = true;
    else if (folder) { body.destination_path = folder; body.pin_path = window.confirm("Pin this folder for future moves from this codebase to " + shortHost(to) + "?"); }
    moving = true;
    var verb = clone ? "Cloning " : "Moving ";
    sayMove(verb + (t.title || "terminal " + id) + " to " + shortHost(to) + "…");
    (folder ? fetch("/api/move-folders").then(function (r) { if (!r.ok) throw new Error("update the hub before selecting a folder"); }) : Promise.resolve())
      .then(function () { return fetch("/api/terminals/" + id + "/migrate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); })
      .then(function (r) { return r.json().catch(function () { return {}; }).then(function (j) { return { code: r.status, j: j }; }); })
      .then(function (x) {
        if (x.code === 404 || x.code === 405) throw new Error("this hub can't move terminals yet (update the hub)");
        if (x.code !== 202) throw new Error(x.j.error || "error " + x.code);
        poll(x.j.job, t, to, verb);
      })
      .catch(function (e) { moving = false; sayMove((clone ? "Clone" : "Move") + " failed: " + e.message + ". Nothing changed."); });
  }
  function poll(job, t, to, verb) {
    fetch("/api/migrations/" + job, { cache: "no-store" }).then(function (r) {
      if (r.status === 404) { moving = false; sayMove("The hub lost track of the move (it restarted). Check the terminal list: the source ends only after a move finished."); refresh(); return null; }
      if (!r.ok) throw new Error("status " + r.status);
      return r.json();
    }).then(function (j) {
      if (!j) return;
      var box = el("div");
      box.appendChild(el("b", "", verb + (t.title || "terminal") + " to " + shortHost(to)));
      var ol = el("ol", "ws-phases");
      (j.phases || []).forEach(function (p) {
        var mark = { done: "✓", running: "…", failed: "✗", skipped: "–", pending: "·" }[p.state] || "·";
        var li = el("li", "ph-" + p.state, mark + " " + (PHASE[p.name] || p.name) + (p.name === "copy" || p.name === "resume" ? shortHost(to) : ""));
        if (p.detail) li.appendChild(el("span", "mut", " — " + p.detail));
        ol.appendChild(li);
      });
      box.appendChild(ol);
      if (j.done) box.appendChild(el("p", j.error ? "bad" : "", j.error ? "Failed: " + j.error + ". " + (t.title || "The terminal") + " still runs on " + shortHost(t.host) + "."
        : j.mode === "clone" ? "Cloned. The copy runs on " + shortHost(to) + "; the source keeps running." : "Moved. " + shortHost(to) + " owns the conversation now."));
      moveSay.hidden = false; moveSay.textContent = ""; moveSay.appendChild(box);
      if (!j.done) { setTimeout(function () { poll(job, t, to, verb); }, 1000); return; }
      moving = false;
      refresh(function () { if (!j.error && j.dest_terminal && infos[String(j.dest_terminal)]) show(String(j.dest_terminal)); });
    }).catch(function () { setTimeout(function () { poll(job, t, to, verb); }, 3000); }); // the hub restarting
  }
  function sayMove(msg) { moveSay.hidden = false; moveSay.textContent = msg; }

  document.getElementById("ws-actions").onchange = function () {
    var action = this.value;
    this.value = "";
    if (action.indexOf("move:") === 0) { migrate(st.slots[st.focus], action.slice(5)); return; }
    if (action.indexOf("clone:") === 0) { migrate(st.slots[st.focus], action.slice(6), true); return; }
    runAction(action);
  };

  function shortcut(e) { // xterm's key hook and the page's: true = let the terminal have it
    if (e.type !== "keydown" || !e.ctrlKey || !e.shiftKey || e.altKey || e.metaKey) return true;
    var k = e.key, n = /^Digit([1-9])$/.exec(e.code);
    if (n) { var i = +n[1] - 1; if (i < logical()) focusSlot(i, true); }
    else if (k === "ArrowLeft") step(-1);
    else if (k === "ArrowRight") step(1);
    else if (k === "ArrowUp") focusSlot((st.focus - 1 + logical()) % logical(), true);
    else if (k === "ArrowDown") focusSlot((st.focus + 1) % logical(), true);
    else if (k.toLowerCase() === "b") { if (!e.repeat) toggleSidebar(); }
    else if (k === "Enter") toggleZoom();
    else if (k === "L" || k === "l") setLayout(ORDER[(ORDER.indexOf(st.layout) + 1) % ORDER.length]);
    else if (k === "N" || k === "n") openNew();
    else if (k === "K" || k === "k") { wsq.focus(); wsq.select(); }
    else if (k === "J" || k === "j") { var w = order.filter(function (id) { return stateOf(id) === "waiting"; }); if (w.length) show(w[0]); }
    else return true;
    e.preventDefault();
    return false;
  }
  // Capture phase on the document: runs before xterm sees the key, so a shortcut never reaches the shell.
  document.addEventListener("keydown", function (e) { if (!shortcut(e)) e.stopPropagation(); }, true);
  document.querySelectorAll(".ws-layouts button").forEach(function (b) { b.onclick = function () { setLayout(b.getAttribute("data-layout")); }; });
  var wsq = document.getElementById("wsq");
  wsq.addEventListener("input", renderList);
  // The quick switcher: Ctrl+Shift+K (Cmd+K on a Mac) goes to the filter; Enter opens the first match, Down the list.
  wsq.placeholder = "Go to a session…  (Ctrl+Shift+K)";
  wsq.addEventListener("keydown", function (e) {
    var first = list.querySelector(".ws-item");
    if (e.key === "Enter" && first) { // the shown row, ended ones too
      e.preventDefault();
      var id = Object.keys(infos).map(function (k) { return infos[k].id; }).filter(function (k) { return items[k] === first; })[0];
      if (id !== undefined) show(id);
    }
    else if (e.key === "ArrowDown" && first) { e.preventDefault(); first.focus(); }
    else if (e.key === "Escape") { wsq.value = ""; renderList(); }
  });
  document.addEventListener("keydown", function (e) {
    if ((e.key || "").toLowerCase() === "k" && e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey) { e.preventDefault(); e.stopPropagation(); wsq.focus(); wsq.select(); }
  }, true);
  narrow.addEventListener && narrow.addEventListener("change", layout);

  // ------------------------------------------------------------ new terminal

  var dlg = document.getElementById("wsnew"), form = document.getElementById("wsnewform"), err = document.getElementById("wsnewerr");
  function openNew() { err.textContent = ""; if (dlg.showModal) dlg.showModal(); else dlg.setAttribute("open", ""); var c = form.querySelector('input[name="path"]'); if (c) c.focus(); }
  document.getElementById("wsnewbtn").onclick = openNew;
  document.getElementById("wsnewbtn2").onclick = openNew;
  document.getElementById("wsnewclose").onclick = function () { dlg.close(); };
  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var btn = form.querySelector('button[type="submit"]');
    btn.disabled = true; err.textContent = "";
    fetch("/terminals/new", { method: "POST", body: new FormData(form) }).then(function (r) { return r.json(); }).then(function (j) {
      btn.disabled = false;
      if (j.error) { err.textContent = j.error; return; }
      dlg.close();
      form.querySelector('textarea[name="prompt"]').value = "";
      refresh(function () { show(j.id); });
    }).catch(function (x) { btn.disabled = false; err.textContent = String(x); });
  });

  refresh(function () { var id = st.slots[st.focus]; if (id && panes[id]) setTimeout(function () { panes[id].focus(); }, 300); });
  setInterval(refresh, 10000);
  setTimeout(loadMachines, 1500);
  // Agents steer the workspace through the hub (workspace_ctl.go): apply its requests after the last one seen,
  // and tell it what this window shows. The first answer only sets where to start, so old requests don't replay.
  var me = "web:main", wsSeq = -1, lastState = "";
  var follower = 0;
  function follow() {
    fetch("/api/workspace?since=" + Math.max(wsSeq, 0), { cache: "no-store" }).then(function (r) {
      if (r.status === 404 || r.status === 405) clearInterval(follower); // a hub without workspace control
      return r.ok ? r.json() : null;
    }).catch(function () { return null; }).then(function (w) {
      if (!w) return;
      if (wsSeq >= 0) (w.intents || []).forEach(function (i) {
        if (i.client && i.client !== me) return;
        if ((i.action === "open" || i.action === "focus") && infos[String(i.terminal)]) show(String(i.terminal));
        else if (i.action === "layout" && COUNTS[i.layout]) setLayout(i.layout);
        else if (i.action === "zoom" && !st.zoom || i.action === "unzoom" && st.zoom) toggleZoom();
      });
      wsSeq = w.seq;
      var slots = st.slots.slice(0, logical()).map(Number).filter(function (n) { return n > 0; });
      var now = JSON.stringify({ client: me, layout: st.layout, slots: slots, focus: Number(st.slots[st.focus]) || 0, zoom: st.zoom });
      if (now === lastState) return;
      lastState = now;
      fetch("/api/workspace/state", { method: "POST", headers: { "Content-Type": "application/json" }, body: now }).catch(function () {});
    });
  }
  setTimeout(follow, 1200);
  follower = setInterval(follow, 3000);
  setInterval(loadMachines, 30000);
})();
