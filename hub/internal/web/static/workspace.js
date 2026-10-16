// Workspace (terminals.go): many terminals at once. Every open terminal stays attached in the background
// (TermPane), so switching is instant and the sidebar knows who is working and who is waiting for you.
// Panes are slots in a grid; a terminal's element moves between a slot and an off-screen parking spot.
(function () {
  "use strict";
  var grid = document.getElementById("wsgrid"), list = document.getElementById("wslist");
  if (!grid || !list || !window.TermPane) return;
  var KEY = "llmb.workspace.v1", MAX_ATTACHED = 12;
  var COUNTS = { "1": 1, "2": 2, "3": 3, "4": 4, "6": 6, "9": 9 }, ORDER = ["1", "2", "3", "4", "6", "9"];
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
      return { layout: COUNTS[s.layout] ? s.layout : "2", slots: Array.isArray(s.slots) ? s.slots : [], focus: s.focus | 0, zoom: !!s.zoom,
        views: s.views && typeof s.views === "object" ? s.views : {} };
    } catch (e) { return { layout: "2", slots: [], focus: 0, zoom: false, views: {} }; }
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
      var seen = {};
      order = ts.map(function (t) { return t.id; });
      ts.forEach(function (t) {
        seen[t.id] = true;
        var old = infos[t.id];
        infos[t.id] = t;
        if (old && old.title !== t.title && slotOf(t.id) >= 0) paintSlot(slotOf(t.id));
        attach(t);
      });
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

  var dotTitle = { working: "working", waiting: "waiting for you", idle: "idle", connecting: "connecting", ended: "ended" };
  function stateOf(id) {
    var p = panes[id], i = infos[id];
    if (!i) return "ended";
    if (i.kind === "session") return Date.now() - Date.parse(i.updated) < 30000 ? "working" : "idle";
    if (i.status === "ended" || i.status === "failed") return "ended";
    return p ? p.state() : "idle";
  }
  function renderList() {
    var q = (document.getElementById("wsq").value || "").toLowerCase();
    list.textContent = "";
    var header = false;
    order.forEach(function (id) {
      var t = infos[id];
      if (q && (t.title + " " + t.cwd + " " + t.cli + " " + t.host).toLowerCase().indexOf(q) < 0) return;
      if (t.kind === "session" && !header) {
        header = true;
        var h = el("li", "ws-sect", "Running elsewhere");
        h.title = "Claude/Codex sessions started outside the hub (your own terminal, an IDE). Read-only, updates live.";
        list.appendChild(h);
      }
      var li = items[id] || makeItem(id);
      list.appendChild(li);
      paintItem(id);
    });
  }
  function makeItem(id) {
    var li = el("li", "ws-item");
    li.tabIndex = 0;
    var top = el("div", "ws-it-top"), dot = el("span", "dot-s"), title = el("span", "ws-it-title"), num = el("span", "ws-it-num");
    top.append(dot, title, num);
    var badges = el("div", "ws-badges"), meta = el("div", "ws-it-meta"), prev = el("div", "ws-it-prev");
    li.append(top, badges, meta, prev);
    li.addEventListener("click", function () { show(id); });
    li.addEventListener("keydown", function (e) { if (e.key === "Enter") show(id); });
    title.addEventListener("dblclick", function (e) { e.stopPropagation(); rename(id, title); });
    var pen = el("button", "ws-pen", "✎"); pen.type = "button"; pen.title = "Rename";
    pen.addEventListener("click", function (e) { e.stopPropagation(); rename(id, title); });
    top.insertBefore(pen, num);
    top.insertBefore(closeBtn(function () { return id; }, "ws-pen ws-close", function (msg) { prev.textContent = msg; }), num);
    items[id] = li;
    li.parts = { dot: dot, title: title, meta: meta, prev: prev, num: num, badges: badges };
    return li;
  }
  function paintItem(id) {
    var li = items[id], t = infos[id];
    if (!li || !t) return;
    var s = stateOf(id), p = panes[id];
    li.className = "ws-item s-" + s + (t.kind === "session" ? " ext" : "") + (slotOf(id) === st.focus ? " on" : slotOf(id) >= 0 ? " shown" : "");
    badgesFor(li.parts.badges, t);
    li.parts.dot.title = dotTitle[s] || s;
    li.parts.dot.className = "dot-s " + s;
    if (li.parts.title.getAttribute("contenteditable") !== "true") li.parts.title.textContent = t.title || "terminal " + id;
    li.parts.meta.textContent = t.cli + " · " + t.cwd;
    var slot = slotOf(id);
    li.parts.num.textContent = slot >= 0 ? String(slot + 1) : "";
    li.parts.prev.textContent = s === "ended" ? (t.error || t.status) : p ? p.lastLine() : "";
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
      box.appendChild(el("span", "bdg hub", "HUB"));
      box.appendChild(el("span", "bdg host", shortHost(t.host)));
    }
  }
  function badge() {
    var n = order.filter(function (id) { return stateOf(id) === "waiting"; }).length;
    document.title = (n ? "(" + n + ") " : "") + "Workspace · llm-brain hub";
  }
  setInterval(function () { order.forEach(function (id) { if (stateOf(id) === "working") paintItem(id); }); }, 2000);

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
    head.append(num, dot, title, badges, meta, grow, seg, code, open, zoom, close, clear);
    var empty = el("div", "ws-slot-empty");
    root.append(head, body, chat, msg, empty);
    grid.appendChild(root);
    var s = { root: root, head: head, body: body, msg: msg, empty: empty, dot: dot, title: title, meta: meta, badges: badges, open: open, i: i,
      seg: seg, chat: chat, chatFrame: chatFrame, chatNote: chatNote, chatSrc: "" };
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
  // closeBtn: ⏻ ends the terminal or session (its CLI process). The first tap arms it ("Close?"), a second
  // within 4 seconds closes; no confirm() dialog.
  function closeBtn(idOf, cls, say) {
    var b = el("button", cls, "⏻"), armed = null;
    b.type = "button"; b.title = "Close: end its CLI process";
    b.setAttribute("aria-label", "Close");
    function disarm() { clearTimeout(armed); armed = null; b.textContent = "⏻"; b.classList.remove("armed"); }
    b.addEventListener("click", function (e) {
      e.stopPropagation();
      var id = idOf(), t = id && infos[id];
      if (!t) return;
      if (!armed) { b.textContent = "Close?"; b.classList.add("armed"); armed = setTimeout(disarm, 4000); return; }
      disarm();
      var url = t.kind === "session" ? "/api/sessions/" + t.sid + "/close" : "/api/terminals/" + id + "/close";
      fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })
        .then(function (r) { return r.json().catch(function () { return {}; }).then(function (j) { return { ok: r.ok, j: j }; }); })
        .then(function (x) {
          if (!x.ok) { say("Close failed: " + (x.j.error || "error")); return; }
          say(x.j.closing === "session" ? "Closing on " + t.host + "…" : "Closing…");
          refresh();
        })
        .catch(function () { say("Close failed: the hub didn't answer"); });
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

  function shortcut(e) { // xterm's key hook and the page's: true = let the terminal have it
    if (e.type !== "keydown" || !e.ctrlKey || !e.shiftKey || e.altKey || e.metaKey) return true;
    var k = e.key, n = /^Digit([1-9])$/.exec(e.code);
    if (n) { var i = +n[1] - 1; if (i < logical()) focusSlot(i, true); }
    else if (k === "ArrowLeft") step(-1);
    else if (k === "ArrowRight") step(1);
    else if (k === "ArrowUp") focusSlot((st.focus - 1 + logical()) % logical(), true);
    else if (k === "ArrowDown") focusSlot((st.focus + 1) % logical(), true);
    else if (k === "Enter") toggleZoom();
    else if (k === "L" || k === "l") setLayout(ORDER[(ORDER.indexOf(st.layout) + 1) % ORDER.length]);
    else if (k === "N" || k === "n") openNew();
    else if (k === "J" || k === "j") { var w = order.filter(function (id) { return stateOf(id) === "waiting"; }); if (w.length) show(w[0]); }
    else return true;
    e.preventDefault();
    return false;
  }
  // Capture phase on the document: runs before xterm sees the key, so a shortcut never reaches the shell.
  document.addEventListener("keydown", function (e) { if (!shortcut(e)) e.stopPropagation(); }, true);
  document.querySelectorAll(".ws-layouts button").forEach(function (b) { b.onclick = function () { setLayout(b.getAttribute("data-layout")); }; });
  document.getElementById("wsq").addEventListener("input", renderList);
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
})();
