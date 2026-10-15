// The hub's editor: Monaco on a folder of a Mac, over a live file channel (agent/fsbridge.go via /fs/ws),
// laid out and keyed like Sublime Text: side bar (open files + folders, resizable, ⌘K ⌘B), Goto Anything
// (⌘P with file:line, file@symbol, :line, @symbol, #text, >command), Find in Files (⌘⇧F), two columns
// (⌘⌥2), and Sublime's editing keys. Edits autosave; a file changed on disk reloads, or asks if you have
// unsaved edits. Text from files goes through textContent or Monaco only.
(function () {
  "use strict";
  var root = document.getElementById("ed");
  if (!root || !window.require) return;
  var host = root.dataset.host, folder = root.dataset.root;
  var $ = function (id) { return document.getElementById(id); };
  var mac = /Mac|iPhone|iPad/.test(navigator.platform);
  var LS = "llmb.editor.";
  var ws = null, seq = 0, waiting = {}, M = null;
  var groups = [], focusG = 0;         // Monaco editors (columns); focusG gets the next file
  var files = {};                       // path -> {model, sha, saved, timer, tab, binary, size}
  var order = [], current = null, allFiles = null, closedStack = [];

  function el(tag, cls, text) { var e = document.createElement(tag); if (cls) e.className = cls; if (text !== undefined) e.textContent = text; return e; }
  function say(t) { $("edstate").textContent = t; }
  function store(k, v) { try { localStorage.setItem(LS + k, v); } catch (e) { /* private mode */ } }
  function load(k) { try { return localStorage.getItem(LS + k); } catch (e) { return null; } }
  function base(p) { return p.split("/").pop(); }
  function size(n) { return n == null ? "" : n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(1) + " KB" : (n / 1048576).toFixed(1) + " MB"; }
  function ed() { return groups[focusG]; }

  // ------------------------------------------------------------ channel
  function call(op, args) {
    return new Promise(function (resolve, reject) {
      if (!ws || ws.readyState !== 1) { reject(new Error("not connected")); return; }
      var id = ++seq;
      waiting[id] = { resolve: resolve, reject: reject };
      ws.send(JSON.stringify(Object.assign({ id: id, op: op }, args || {})));
    });
  }
  var tries = 0;
  function connect() {
    say(tries ? "reconnecting…" : "connecting…");
    var proto = location.protocol === "https:" ? "wss://" : "ws://";
    ws = new WebSocket(proto + location.host + "/fs/ws?host=" + encodeURIComponent(host) + "&root=" + encodeURIComponent(folder));
    ws.onopen = function () {
      call("hello").then(function (r) {
        tries = 0;
        $("edroot").title = r.root || folder;
        $("edbranch").textContent = r.branch ? "⎇ " + r.branch : "";
        say("connected");
        loadDir("", $("edtree"), 0);
        watch();
        Object.keys(files).forEach(refreshFromDisk);
        if (!order.length && root.dataset.file) open(root.dataset.file);
      });
    };
    ws.onmessage = function (ev) {
      var m = JSON.parse(ev.data);
      if (m.op === "changed") { onChanged(m.path, m.sha); return; }
      var w = waiting[m.id];
      if (!w) return;
      delete waiting[m.id];
      if (m.error) w.reject(new Error(m.error)); else w.resolve(m);
    };
    ws.onclose = function (ev) {
      Object.keys(waiting).forEach(function (k) { waiting[k].reject(new Error("disconnected")); delete waiting[k]; });
      ws = null;
      say(ev.code === 4000 && ev.reason ? ev.reason : "disconnected · retrying");
      tries++;
      setTimeout(connect, Math.min(1000 * tries, 8000));
    };
  }

  // ------------------------------------------------------------ side bar: tree, open files, resize, menu
  var tree = $("edtree"), openDirs = {};
  function join(dir, name) { return dir ? dir + "/" + name : name; }
  function ext(name) { var i = name.lastIndexOf("."); return i > 0 ? name.slice(i + 1).toLowerCase() : ""; }
  function loadDir(dir, box, depth) {
    return call("list", { path: dir }).then(function (r) {
      box.textContent = "";
      (r.entries || []).forEach(function (en) {
        var p = join(dir, en.name);
        var row = el("div", "ed-node" + (en.dir ? " dir" : "") + (en.name[0] === "." ? " dim" : ""));
        row.style.paddingLeft = (6 + depth * 14) + "px";
        row.setAttribute("role", "treeitem");
        row.dataset.path = p;
        var tw = el("span", "tw", en.dir ? "▸" : ""), ic = el("span", "ic ic-" + (en.dir ? "dir" : ext(en.name) || "txt"), en.dir ? "" : (ext(en.name) || "·").slice(0, 3));
        var nm = el("span", "nm", en.name), sz = el("span", "sz", en.dir ? "" : size(en.size));
        row.title = p + (en.dir ? "" : "  ·  " + size(en.size));
        row.append(tw, ic, nm, sz);
        box.appendChild(row);
        if (en.dir) {
          var kids = el("div"); kids.hidden = !openDirs[p]; box.appendChild(kids);
          row.onclick = function () {
            kids.hidden = !kids.hidden; openDirs[p] = !kids.hidden;
            tw.textContent = kids.hidden ? "▸" : "▾";
            if (!kids.hidden && !kids.dataset.loaded) { kids.dataset.loaded = "1"; loadDir(p, kids, depth + 1); }
          };
          if (openDirs[p]) { tw.textContent = "▾"; kids.dataset.loaded = "1"; loadDir(p, kids, depth + 1); }
        } else {
          row.onclick = function () { open(p); };
        }
        row.oncontextmenu = function (e) { e.preventDefault(); menu(e, p, en.dir); };
      });
      markTree();
    }).catch(function (e) { box.textContent = ""; box.appendChild(el("div", "ed-node dim", e.message)); });
  }
  function refreshTree() { allFiles = null; return loadDir("", tree, 0); }
  function markTree() { tree.querySelectorAll(".ed-node").forEach(function (n) { n.classList.toggle("on", n.dataset.path === current); }); }
  function renderOpen() {
    var ul = $("edopen"); ul.textContent = "";
    order.forEach(function (p) {
      var f = files[p], li = el("li", (p === current ? "on" : "") + (isDirty(f) ? " dirty" : ""));
      li.append(el("span", "nm", base(p)), el("span", "dirp", p.indexOf("/") > 0 ? p.slice(0, p.lastIndexOf("/")) : ""));
      var x = el("button", "x", "✕"); x.type = "button"; x.title = "Close (⌥W)";
      x.onclick = function (e) { e.stopPropagation(); close(p); };
      li.appendChild(x);
      li.onclick = function () { show(p); };
      ul.appendChild(li);
    });
  }
  // Resize and hide (⌘K ⌘B), remembered.
  var side = $("edside"), handle = $("edresize");
  function setSide(w, hidden) {
    root.style.setProperty("--side", Math.max(150, Math.min(600, w)) + "px");
    root.classList.toggle("noside", !!hidden);
    store("side", w); store("sidehidden", hidden ? "1" : "");
    setTimeout(layoutEditors, 30);
  }
  setSide(+load("side") || 260, !!load("sidehidden"));
  function toggleSide() { setSide(parseInt(getComputedStyle(root).getPropertyValue("--side"), 10) || 260, !root.classList.contains("noside")); }
  handle.addEventListener("pointerdown", function (e) {
    e.preventDefault(); handle.setPointerCapture(e.pointerId);
    var start = e.clientX, w0 = side.getBoundingClientRect().width;
    root.classList.add("resizing");
    handle.onpointermove = function (m) { setSide(w0 + m.clientX - start, false); };
    handle.onpointerup = function () { handle.onpointermove = null; root.classList.remove("resizing"); };
  });
  handle.addEventListener("dblclick", toggleSide);
  // Context menu: new, rename, delete (to the Trash), copy path.
  var menuEl = $("edmenu");
  function menu(e, p, isDir) {
    var dir = isDir ? p : (p.indexOf("/") > 0 ? p.slice(0, p.lastIndexOf("/")) : "");
    var items = [
      ["New File…", function () { newItem(dir, false); }], ["New Folder…", function () { newItem(dir, true); }], null,
      ["Rename…", function () { renameItem(p); }], ["Delete (to Trash)…", function () { deleteItem(p, isDir); }], null,
      ["Copy Path", function () { copy(($("edroot").title || folder).replace(/\/$/, "") + "/" + p); }], ["Copy Relative Path", function () { copy(p); }]
    ];
    menuEl.textContent = "";
    items.forEach(function (it) {
      if (!it) { menuEl.appendChild(el("li", "sep")); return; }
      var li = el("li", "", it[0]); li.onclick = function () { hideMenu(); it[1](); }; menuEl.appendChild(li);
    });
    menuEl.style.left = Math.min(e.clientX, innerWidth - 220) + "px"; menuEl.style.top = Math.min(e.clientY, innerHeight - 240) + "px";
    menuEl.hidden = false;
  }
  function hideMenu() { menuEl.hidden = true; }
  document.addEventListener("click", hideMenu);
  function copy(t) { if (navigator.clipboard) navigator.clipboard.writeText(t).then(function () { say("copied " + t); }); }
  // ask: a one-line input in the palette spot (no browser prompt()).
  function ask(label, value, done) {
    pal.hidden = false; pl.textContent = ""; $("edph").textContent = label + " · Enter to confirm, Esc to cancel";
    pq.value = value; pq.focus(); pq.setSelectionRange(value.length, value.length);
    asking = function (v) { asking = null; if (v !== null && v.trim()) done(v.trim()); };
  }
  var asking = null;
  function newItem(dir, isDir) {
    ask(isDir ? "New folder" : "New file", dir ? dir + "/" : "", function (name) {
      call("create", { path: name, dir: isDir }).then(function () { refreshTree(); if (!isDir) open(name); }).catch(function (e) { say(e.message); });
    });
  }
  function renameItem(p) {
    ask("Rename " + p + " to", p, function (to) {
      if (to === p) return;
      Object.keys(files).forEach(function (op) { if ((op === p || op.indexOf(p + "/") === 0) && isDirty(files[op])) save(op); });
      call("rename", { path: p, to: to }).then(function () {
        Object.keys(files).forEach(function (op) { // re-open moved tabs under the new name
          if (op === p || op.indexOf(p + "/") === 0) { var np = to + op.slice(p.length), f = files[op]; f.saved = f.model.getAlternativeVersionId(); close(op); if (!f.binary) open(np); }
        });
        refreshTree(); say("renamed to " + to);
      }).catch(function (e) { say(e.message); });
    });
  }
  function deleteItem(p, isDir) {
    ask("Move " + p + " to the Trash? Type its name (" + base(p) + ") to confirm", "", function (typed) {
      if (typed !== base(p)) { say("not deleted: the name didn't match"); return; }
      call("delete", { path: p }).then(function () {
        Object.keys(files).forEach(function (op) { if (op === p || op.indexOf(p + "/") === 0) { files[op].saved = files[op].model.getAlternativeVersionId(); close(op); } });
        refreshTree(); say((isDir ? "folder " : "") + base(p) + " moved to the Trash");
      }).catch(function (e) { say(e.message); });
    });
  }
  side.addEventListener("click", function (e) {
    var a = e.target.getAttribute && e.target.getAttribute("data-act");
    if (a === "newfile") newItem(current && current.indexOf("/") > 0 ? current.slice(0, current.lastIndexOf("/")) : "", false);
    if (a === "newdir") newItem("", true);
    if (a === "refresh") refreshTree();
    if (a === "collapse") { openDirs = {}; refreshTree(); }
  });

  // ------------------------------------------------------------ files, tabs, crumbs
  function open(path, then) {
    if (files[path]) { show(path); if (then) then(); return; }
    say("opening " + path + "…");
    call("read", { path: path }).then(function (r) {
      var f = { sha: r.sha, binary: r.binary };
      if (r.binary) f.model = M.editor.createModel("(binary or non-UTF-8 file: not shown, so a save can't damage it)", "plaintext");
      else {
        f.model = M.editor.createModel(r.content, undefined, M.Uri.file("/" + path));
        f.model.onDidChangeContent(function () { dirty(path); });
      }
      f.saved = f.model.getAlternativeVersionId();
      files[path] = f;
      order.push(path);
      addTab(path);
      show(path);
      watch();
      say("connected");
      call("stat", { path: path }).then(function (s) { f.size = s.size; f.mtime = s.mtime; if (current === path) status(); }).catch(function () {});
      if (then) then();
    }).catch(function (e) { say(path + ": " + e.message); });
  }
  function addTab(path) {
    var t = el("div", "ed-tab"), name = el("span", "", base(path)), x = el("button", "x", "✕");
    t.title = path; x.type = "button"; x.title = "Close (⌥W)";
    t.append(name, x);
    t.onclick = function () { show(path); };
    t.onauxclick = function (e) { if (e.button === 1) close(path); };
    x.onclick = function (e) { e.stopPropagation(); close(path); };
    $("edtabs").appendChild(t);
    files[path].tab = t;
  }
  function show(path) {
    var f = files[path];
    if (!f) return;
    if (current !== path) remember();
    current = path;
    var e = ed();
    e.setModel(f.model);
    e.updateOptions({ readOnly: !!f.binary });
    Object.keys(files).forEach(function (p) { files[p].tab.classList.toggle("on", p === path); });
    hideBanner(); markTree(); renderOpen(); crumbs(); status();
    try { history.replaceState(null, "", location.pathname + "?host=" + encodeURIComponent(host) + "&root=" + encodeURIComponent(folder) + "&file=" + encodeURIComponent(path) + (document.body.dataset.embed ? "&embed=1" : "")); } catch (x) { /* ok */ }
    e.focus();
  }
  function crumbs() {
    var c = $("edcrumbs"); c.textContent = "";
    if (!current) return;
    var parts = current.split("/"), acc = "";
    parts.forEach(function (seg, i) {
      acc = acc ? acc + "/" + seg : seg;
      var to = acc, a = el("span", i === parts.length - 1 ? "last" : "", seg);
      if (i < parts.length - 1) { a.onclick = function () { reveal(to); }; a.title = "Reveal in the side bar"; }
      c.appendChild(a);
      if (i < parts.length - 1) c.appendChild(el("span", "sep", "›"));
    });
  }
  function reveal(dir) {
    var parts = dir.split("/"), acc = "";
    parts.forEach(function (seg) { acc = acc ? acc + "/" + seg : seg; openDirs[acc] = true; });
    refreshTree();
    if (root.classList.contains("noside")) toggleSide();
  }
  function close(path) {
    var f = files[path];
    if (!f) return;
    if (isDirty(f) || f.saving) { f.closing = true; if (!f.saving) save(path); return; } // save first, close when clean
    f.tab.remove();
    groups.forEach(function (g) { if (g.getModel() === f.model) g.setModel(null); });
    setTimeout(function () { f.model.dispose(); }, 2000);
    delete files[path];
    order = order.filter(function (p) { return p !== path; });
    closedStack.push(path); if (closedStack.length > 30) closedStack.shift();
    watch();
    if (current === path) { if (order.length) show(order[order.length - 1]); else { current = null; markTree(); renderOpen(); crumbs(); status(); } }
    else renderOpen();
  }
  function isDirty(f) { return !!f && !f.binary && f.model.getAlternativeVersionId() !== f.saved; }
  function dirty(path) {
    var f = files[path];
    f.tab.classList.toggle("dirty", isDirty(f));
    renderOpenSoon();
    clearTimeout(f.timer);
    f.timer = setTimeout(function () { save(path); }, 600);
  }
  var ro = null;
  function renderOpenSoon() { clearTimeout(ro); ro = setTimeout(renderOpen, 150); }
  function save(path) {
    var f = files[path];
    if (!f || !isDirty(f) || f.saving) return;
    var version = f.model.getAlternativeVersionId(), content = f.model.getValue();
    f.saving = true;
    say("saving " + base(path) + "…");
    call("write", { path: path, content: content, base: f.sha }).then(function (r) {
      f.saving = false;
      if (r.conflict) { f.closing = false; conflict(path, r); return; } // keep the tab: the person decides
      f.sha = r.sha; f.saved = version; f.size = new Blob([content]).size;
      f.tab.classList.toggle("dirty", isDirty(f));
      renderOpen();
      say("saved " + base(path) + " · " + new Date().toLocaleTimeString());
      if (path === current) status();
      if (isDirty(f)) { dirty(path); return; } // typed more while it was saving
      if (f.closing) close(path);
    }).catch(function (e) { f.saving = false; f.closing = false; say("couldn't save " + path + ": " + e.message + " · retrying"); f.timer = setTimeout(function () { save(path); }, 3000); });
  }
  function watch() { if (ws && ws.readyState === 1) call("watch", { paths: Object.keys(files) }).catch(function () {}); }
  function onChanged(path, sha) {
    var f = files[path];
    if (!f || sha === f.sha) return;
    if (!isDirty(f)) { refreshFromDisk(path); return; }
    conflict(path, { sha: sha });
  }
  function refreshFromDisk(path) {
    var f = files[path];
    if (!f) return;
    call("read", { path: path }).then(function (r) {
      if (r.sha === f.sha || r.binary || isDirty(f)) return;
      replace(f, r.content, r.sha);
      if (path === current) say(base(path) + " changed on disk · reloaded");
    }).catch(function () {});
  }
  function replace(f, content, sha) {
    var positions = groups.map(function (g) { return g.getModel() === f.model ? g.getPosition() : null; });
    f.model.pushEditOperations([], [{ range: f.model.getFullModelRange(), text: content }], function () { return null; });
    f.sha = sha; f.saved = f.model.getAlternativeVersionId();
    f.tab.classList.remove("dirty"); renderOpen();
    groups.forEach(function (g, i) { if (positions[i]) g.setPosition(positions[i]); });
  }
  function conflict(path, r) {
    var f = files[path], b = $("edbanner"), gone = r.content === "";
    show(path);
    b.textContent = "";
    b.appendChild(el("span", "", path + (gone ? " was deleted on disk" : " changed on disk") + " while you were editing."));
    var mine = el("button", "", gone ? "Recreate it with my text" : "Keep mine"), theirs = el("button", "", gone ? "Close it" : "Load the disk version");
    mine.type = theirs.type = "button";
    mine.onclick = function () { f.sha = gone ? "" : r.sha; hideBanner(); save(path); }; // overwrite on purpose
    theirs.onclick = function () {
      hideBanner();
      if (gone) { f.saved = f.model.getAlternativeVersionId(); close(path); return; }
      call("read", { path: path }).then(function (d) { replace(f, d.content || "", d.sha); });
    };
    b.append(mine, theirs);
    b.hidden = false;
  }
  function hideBanner() { $("edbanner").hidden = true; $("edbanner").textContent = ""; }

  // ------------------------------------------------------------ status bar
  function status() {
    var e = ed(), m = e && e.getModel(), f = current && files[current];
    if (!m) { ["edpos", "edindent", "edeol", "edlang", "edsize"].forEach(function (id) { $(id).textContent = ""; }); return; }
    var sel = e.getSelections() || [], p = e.getPosition() || { lineNumber: 1, column: 1 };
    var chars = sel.reduce(function (n, s) { return n + m.getValueLengthInRange(s); }, 0);
    $("edpos").textContent = "Ln " + p.lineNumber + ", Col " + p.column + (sel.length > 1 ? " · " + sel.length + " cursors" : "") + (chars ? " (" + chars + " selected)" : "");
    var o = m.getOptions();
    $("edindent").textContent = (o.insertSpaces ? "Spaces: " : "Tab Size: ") + o.tabSize;
    $("edeol").textContent = m.getEOL() === "\r\n" ? "CRLF" : "LF";
    $("edlang").textContent = m.getLanguageId();
    $("edsize").textContent = (f && f.size != null ? size(f.size) + " · " : "") + m.getLineCount() + " lines" + (f && f.mtime ? " · modified " + new Date(f.mtime).toLocaleString() : "");
  }
  $("edindent").onclick = function () {
    var m = ed().getModel(); if (!m) return;
    var o = m.getOptions(), next = o.insertSpaces ? (o.tabSize === 2 ? { insertSpaces: true, tabSize: 4 } : { insertSpaces: false, tabSize: 4 }) : { insertSpaces: true, tabSize: 2 };
    m.updateOptions(next); status();
  };
  $("edeol").onclick = function () { var m = ed().getModel(); if (m) { m.pushEOL(m.getEOL() === "\n" ? 1 : 0); status(); } };
  $("edlang").onclick = function () { palette(">syntax "); };
  $("edpos").onclick = function () { ed().trigger("kb", "editor.action.gotoLine"); };

  // ------------------------------------------------------------ Goto Anything (⌘P) and the command palette
  var pal = $("edpal"), pq = $("edpq"), pl = $("edpl"), picked = 0, shown = [];
  function score(name, needle) {
    var i = 0, last = -1, s = 0;
    for (var c = 0; c < needle.length; c++) {
      var at = name.indexOf(needle[c], i);
      if (at < 0) return null; // no match (a good match can score below zero)
      s += at - last; last = at; i = at + 1;
    }
    var b = name.lastIndexOf("/");
    return s + name.length / 100 - (name.slice(b + 1).indexOf(needle) === 0 ? 50 : 0);
  }
  function palette(prefix) { asking = null; pal.hidden = false; pq.value = prefix || ""; pq.focus(); filter(); }
  function closePal() { pal.hidden = true; var e = ed(); if (e) e.focus(); }
  function filter() {
    if (asking) return;
    var v = pq.value, hint = $("edph");
    shown = []; picked = 0;
    if (v[0] === ">") {
      var q = v.slice(1).trim().toLowerCase();
      if (q.indexOf("syntax") === 0) {
        var lq = q.slice(6).trim();
        M.languages.getLanguages().map(function (l) { return l.id; }).filter(function (l) { return !lq || l.indexOf(lq) >= 0; }).slice(0, 60).forEach(function (l) {
          shown.push({ label: "Set Syntax: " + l, run: function () { var m = ed().getModel(); if (m) { M.editor.setModelLanguage(m, l); status(); } } });
        });
      } else {
        commands().filter(function (c) { return !q || c.label.toLowerCase().indexOf(q) >= 0; }).forEach(function (c) { shown.push(c); });
      }
      hint.textContent = "Commands · ↑↓ and Enter";
    } else if (v[0] === ":") {
      hint.textContent = "Go to line in this file"; shown.push({ label: "Go to line " + v.slice(1), run: function () { gotoLine(+v.slice(1)); } });
    } else if (v[0] === "@") {
      closePal(); ed().trigger("kb", "editor.action.quickOutline"); return;
    } else if (v[0] === "#") {
      hint.textContent = "Find in this file"; shown.push({ label: "Find “" + v.slice(1) + "”", run: function () { findInFile(v.slice(1)); } });
    } else {
      hint.textContent = "Files · name:line · name@symbol · :line · @symbol · #text · >command";
      var m = /^(.*?)(?::(\d+)|@(.*))?$/.exec(v), needle = (m[1] || "").toLowerCase().replace(/\s+/g, ""), line = +m[2], sym = m[3];
      var go = function () {
        var list = needle ? allFiles.map(function (p) { return [score(p.toLowerCase(), needle), p]; }).filter(function (x) { return x[0] !== null; })
          .sort(function (a, b) { return a[0] - b[0]; }).slice(0, 60).map(function (x) { return x[1]; })
          : order.slice().reverse().concat(allFiles.filter(function (p) { return !files[p]; }).slice(0, 40));
        list.forEach(function (p) {
          shown.push({ label: base(p), sub: p, run: function () { open(p, function () { if (line) gotoLine(line); else if (sym !== undefined) setTimeout(function () { ed().trigger("kb", "editor.action.quickOutline"); }, 60); }); } });
        });
        draw();
      };
      if (allFiles) go(); else { pl.textContent = ""; pl.appendChild(el("li", "mut", "indexing files…")); call("find").then(function (r) { allFiles = r.files || []; if (!pal.hidden) filter(); }); }
      return;
    }
    draw();
  }
  function draw() {
    pl.textContent = "";
    shown.slice(0, 60).forEach(function (s, i) {
      var li = el("li", i === picked ? "on" : "");
      li.append(el("span", "lbl", s.label));
      if (s.sub) li.append(el("span", "sub", s.sub));
      if (s.keys) li.append(el("kbd", "", s.keys));
      li.onmousedown = function (e) { e.preventDefault(); picked = i; run(); };
      pl.appendChild(li);
    });
  }
  function run() { var s = shown[picked]; closePal(); if (s) s.run(); }
  pq.addEventListener("input", filter);
  pq.addEventListener("keydown", function (e) {
    if (asking && (e.key === "Enter" || e.key === "Escape")) { e.preventDefault(); var a = asking, v = pq.value; pal.hidden = true; a(e.key === "Enter" ? v : null); var g = ed(); if (g) g.focus(); return; }
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault(); picked = Math.max(0, Math.min(shown.length - 1, picked + (e.key === "ArrowDown" ? 1 : -1))); draw();
      var on = pl.querySelector(".on"); if (on) on.scrollIntoView({ block: "nearest" });
    } else if (e.key === "Enter") { e.preventDefault(); run(); }
    else if (e.key === "Escape") { e.preventDefault(); closePal(); }
  });
  pq.addEventListener("blur", function () { setTimeout(function () { if (document.activeElement !== pq) { pal.hidden = true; asking = null; } }, 150); });
  function gotoLine(n) { var e = ed(); if (!e.getModel() || !n) return; remember(); e.setPosition({ lineNumber: n, column: 1 }); e.revealLineInCenter(n); e.focus(); }
  function findInFile(t) { var e = ed(); e.focus(); e.trigger("kb", "actions.find"); setTimeout(function () { var c = e.getContribution("editor.contrib.findController"); if (c) c.setSearchString(t); }, 30); }

  // ------------------------------------------------------------ Find in Files (⌘⇧F)
  var fp = $("edfind"), fq = $("edfq"), fres = $("edfres");
  function openFind() {
    fp.hidden = false; layoutEditors();
    var e = ed(), m = e && e.getModel(), s = m && e.getSelection();
    if (s && !s.isEmpty()) fq.value = m.getValueInRange(s).split("\n")[0];
    fq.focus(); fq.select();
  }
  function closeFind() { fp.hidden = true; layoutEditors(); var e = ed(); if (e) e.focus(); }
  function grep() {
    var q = fq.value; if (!q.trim()) return;
    $("edfcount").textContent = "searching…"; fres.textContent = "";
    var re = $("edfre").checked;
    call("grep", { query: q, regex: re, case: $("edfcase").checked }).then(function (r) {
      var hits = r.hits || [], byFile = {};
      hits.forEach(function (h) { (byFile[h.path] = byFile[h.path] || []).push(h); });
      var nf = Object.keys(byFile).length;
      $("edfcount").textContent = hits.length + " match" + (hits.length === 1 ? "" : "es") + " in " + nf + " file" + (nf === 1 ? "" : "s") + (r.more ? " (stopped early)" : "");
      Object.keys(byFile).forEach(function (p) {
        var g = el("div", "fgrp"), head = el("div", "fhead", p + "  (" + byFile[p].length + ")"), list = el("div");
        head.onclick = function () { list.hidden = !list.hidden; };
        byFile[p].forEach(function (h) {
          var row = el("div", "fhit");
          row.append(el("span", "ln", String(h.line)), el("span", "tx", h.text.trim()));
          row.onclick = function () {
            open(p, function () {
              var e = ed(), ln = h.line;
              e.setSelection({ startLineNumber: ln, startColumn: h.col, endLineNumber: ln, endColumn: h.col + (re ? 0 : q.length) });
              e.revealLineInCenter(ln); e.focus();
            });
          };
          list.appendChild(row);
        });
        g.append(head, list); fres.appendChild(g);
      });
    }).catch(function (e) { $("edfcount").textContent = e.message; });
  }
  $("edfgo").onclick = grep;
  fq.addEventListener("keydown", function (e) { if (e.key === "Enter") { e.preventDefault(); grep(); } if (e.key === "Escape") { e.preventDefault(); closeFind(); } });
  $("edfclose").onclick = closeFind;

  // ------------------------------------------------------------ columns (⌘⌥1 / ⌘⌥2)
  function setColumns(n) {
    $("edm1").hidden = n < 2; root.classList.toggle("two", n >= 2);
    if (n < 2 && focusG === 1) focusG = 0;
    if (n >= 2 && groups[1] && !groups[1].getModel() && current) groups[1].setModel(files[current].model);
    store("cols", n); layoutEditors();
  }
  function layoutEditors() { groups.forEach(function (g) { g.layout(); }); }
  function focusGroup(i) {
    if (!groups[i] || $("edm" + i).hidden) return;
    focusG = i; groups[i].focus();
    var m = groups[i].getModel();
    current = Object.keys(files).filter(function (p) { return files[p].model === m; })[0] || current;
    Object.keys(files).forEach(function (p) { files[p].tab.classList.toggle("on", p === current); });
    markTree(); renderOpen(); crumbs(); status();
  }

  // ------------------------------------------------------------ commands and keys
  var fontSize = +load("font") || 13;
  function fontBy(d) { fontSize = Math.max(9, Math.min(28, fontSize + d)); groups.forEach(function (g) { g.updateOptions({ fontSize: fontSize }); }); store("font", fontSize); }
  function reopen() { var p = closedStack.pop(); if (p) open(p); }
  function cycle(d) { if (!order.length) return; var i = order.indexOf(current); show(order[(i + d + order.length) % order.length]); }
  function zen() { root.classList.toggle("zen"); setTimeout(layoutEditors, 30); }
  function toggleOpt(name, store_) {
    return function () {
      var g = ed(), cur = g.getOption(M.editor.EditorOption[name]), next;
      if (name === "wordWrap") next = { wordWrap: cur === "off" ? "on" : "off" };
      else if (name === "minimap") next = { minimap: { enabled: !cur.enabled } };
      else if (name === "renderWhitespace") next = { renderWhitespace: cur === "all" ? "selection" : "all" };
      groups.forEach(function (x) { x.updateOptions(next); });
      if (store_) store(store_, JSON.stringify(next));
    };
  }
  // ------------------------------------------------------------ Sublime-only behaviour
  // Bookmarks (⌘F2 toggle, F2 / ⇧F2 next / previous, ⌘⇧F2 clear), per file, as glyph-margin marks.
  var marks = {}; // path -> {line: decorationId}
  function markDecos(path) {
    var f = files[path], m = marks[path] || {};
    if (!f) return;
    var lines = Object.keys(m).map(Number);
    f.markIds = f.model.deltaDecorations(f.markIds || [], lines.map(function (ln) {
      return { range: new M.Range(ln, 1, ln, 1), options: { isWholeLine: true, glyphMarginClassName: "ed-mark", overviewRuler: { color: "#d29922", position: 4 } } };
    }));
  }
  function toggleMark() {
    if (!current) return;
    var ln = ed().getPosition().lineNumber, m = marks[current] = marks[current] || {};
    if (m[ln]) delete m[ln]; else m[ln] = 1;
    markDecos(current);
  }
  function nextMark(d) {
    if (!current || !marks[current]) return;
    var ln = ed().getPosition().lineNumber, all = Object.keys(marks[current]).map(Number).sort(function (a, b) { return a - b; });
    if (!all.length) return;
    var to = d > 0 ? (all.filter(function (x) { return x > ln; })[0] || all[0]) : (all.filter(function (x) { return x < ln; }).pop() || all[all.length - 1]);
    gotoLine(to);
  }
  function clearMarks() { if (current) { marks[current] = {}; markDecos(current); } }
  // Jump back / forward (⌃- / ⌃⇧-): where the cursor was before each file switch or jump.
  var back = [], fwd = [];
  function remember() { var e = ed(); if (current && e && e.getPosition()) { back.push({ path: current, pos: e.getPosition() }); if (back.length > 100) back.shift(); fwd = []; } }
  function jump(from, to) {
    var spot = from.pop(); if (!spot) return;
    var e = ed(); if (current && e.getPosition()) to.push({ path: current, pos: e.getPosition() });
    open(spot.path, function () { ed().setPosition(spot.pos); ed().revealPositionInCenter(spot.pos); ed().focus(); });
  }
  function saveAll() { Object.keys(files).forEach(save); }
  function closeAll() { order.slice().forEach(close); }
  function saveAs() {
    if (!current) return;
    var f = files[current], text = f.model.getValue(), from = current;
    ask("Save as", from, function (to) {
      if (to === from) { save(from); return; }
      call("write", { path: to, content: text, base: "" }).then(function () { refreshTree(); open(to); }).catch(function (e) { say(e.message); });
    });
  }
  function toGroup(i) { // move the current file into column i (opening two columns if needed)
    if (!current) return;
    if (i === 1 && $("edm1").hidden) setColumns(2);
    groups[i].setModel(files[current].model); focusGroup(i);
  }
  function fullscreen() { if (document.fullscreenElement) document.exitFullscreen(); else root.requestFullscreen && root.requestFullscreen(); }
  function center() { var e = ed(); if (e.getPosition()) e.revealPositionInCenter(e.getPosition()); }

  // ------------------------------------------------------------ Sublime Text's macOS key map
  // One table drives the editor's key bindings, the command palette, the shortcut sheet and the self-test:
  // [section, label, keys as shown, bindings (KeyMod|KeyCode, or chords), Monaco command id or function].
  // ⌘W/⌘T/⌘N/⌘⇧T belong to the browser, so those use ⌥ instead (app-level keys are handled further down).
  function sublimeMap() {
    var KM = M.KeyMod, KC = M.KeyCode, C = KM.CtrlCmd, W = KM.WinCtrl, S = KM.Shift, A = KM.Alt, K = function (k) { return KM.chord(C | KC.KeyK, k); };
    return [
      ["Selection", "Add next occurrence", "⌘D", [C | KC.KeyD], "editor.action.addSelectionToNextFindMatch"],
      ["Selection", "Skip occurrence", "⌘K ⌘D", [K(C | KC.KeyD)], "editor.action.moveSelectionToNextFindMatch"],
      ["Selection", "Select all occurrences", "⌃⌘G", [C | W | KC.KeyG], "editor.action.selectHighlights"],
      ["Selection", "Undo selection (soft undo)", "⌘U", [C | KC.KeyU], "cursorUndo"],
      ["Selection", "Redo selection (soft redo)", "⌘⇧U", [C | S | KC.KeyU], "cursorRedo"],
      ["Selection", "Expand selection to line", "⌘L", [C | KC.KeyL], "expandLineSelection"],
      ["Selection", "Split selection into lines", "⌘⇧L", [C | S | KC.KeyL], "editor.action.insertCursorAtEndOfEachLineSelected"],
      ["Selection", "Expand selection to scope", "⌘⇧Space", [C | S | KC.Space], "editor.action.smartSelect.expand"],
      ["Selection", "Shrink selection", "⌘⇧⌥Space", [C | S | A | KC.Space], "editor.action.smartSelect.shrink"],
      ["Selection", "Expand selection to brackets", "⌃⇧M", [W | S | KC.KeyM], "editor.action.selectToBracket"],
      ["Selection", "Expand selection to indentation", "⌘⇧J", [C | S | KC.KeyJ], "editor.action.smartSelect.expand"],
      ["Selection", "Add cursor above", "⌃⇧↑", [W | S | KC.UpArrow], "editor.action.insertCursorAbove"],
      ["Selection", "Add cursor below", "⌃⇧↓", [W | S | KC.DownArrow], "editor.action.insertCursorBelow"],
      ["Selection", "Select word / subword left", "⌥⇧← / ⌃⇧←", [W | S | KC.LeftArrow], "cursorWordPartLeftSelect"],
      ["Selection", "Select word / subword right", "⌥⇧→ / ⌃⇧→", [W | S | KC.RightArrow], "cursorWordPartRightSelect"],
      ["Selection", "Move by subword left", "⌃←", [W | KC.LeftArrow], "cursorWordPartLeft"],
      ["Selection", "Move by subword right", "⌃→", [W | KC.RightArrow], "cursorWordPartRight"],
      ["Selection", "Single selection", "Esc", [], "removeSecondaryCursors"],
      ["Selection", "Add cursor / column select", "⌘ click / ⌥ drag", [], null],
      ["Find", "Find", "⌘F", [C | KC.KeyF], "actions.find"],
      ["Find", "Incremental find", "⌘I", [C | KC.KeyI], "actions.find"],
      ["Find", "Replace", "⌘⌥F", [C | A | KC.KeyF], "editor.action.startFindReplaceAction"],
      ["Find", "Find next", "⌘G", [C | KC.KeyG], "editor.action.nextMatchFindAction"],
      ["Find", "Find previous", "⌘⇧G", [C | S | KC.KeyG], "editor.action.previousMatchFindAction"],
      ["Find", "Use selection for find", "⌘E", [C | KC.KeyE], "actions.findWithSelection"],
      ["Find", "Find next of selection", "⌘⌥G", [C | A | KC.KeyG], "editor.action.nextSelectionMatchFindAction"],
      ["Find", "Find previous of selection", "⌘⌥⇧G", [C | A | S | KC.KeyG], "editor.action.previousSelectionMatchFindAction"],
      ["Find", "Replace next", "⌘⌥E", [C | A | KC.KeyE], "editor.action.replaceOne"],
      ["Find", "Find in files", "⌘⇧F", [], openFind],
      ["Lines", "Duplicate line", "⌘⇧D", [C | S | KC.KeyD], "editor.action.copyLinesDownAction"],
      ["Lines", "Delete line", "⌃⇧K", [W | S | KC.KeyK], "editor.action.deleteLines"],
      ["Lines", "Swap line up", "⌃⌘↑", [C | W | KC.UpArrow], "editor.action.moveLinesUpAction"],
      ["Lines", "Swap line down", "⌃⌘↓", [C | W | KC.DownArrow], "editor.action.moveLinesDownAction"],
      ["Lines", "Join lines", "⌘J", [C | KC.KeyJ], "editor.action.joinLines"],
      ["Lines", "Insert line after", "⌘↩", [C | KC.Enter], "editor.action.insertLineAfter"],
      ["Lines", "Insert line before", "⌘⇧↩", [C | S | KC.Enter], "editor.action.insertLineBefore"],
      ["Lines", "Indent", "⌘]", [C | KC.BracketRight], "editor.action.indentLines"],
      ["Lines", "Unindent", "⌘[", [C | KC.BracketLeft], "editor.action.outdentLines"],
      ["Lines", "Toggle comment", "⌘/", [C | KC.Slash], "editor.action.commentLine"],
      ["Lines", "Toggle block comment", "⌘⌥/", [C | A | KC.Slash], "editor.action.blockComment"],
      ["Lines", "Delete to end of line", "⌘K ⌘K", [K(C | KC.KeyK)], "deleteAllRight"],
      ["Lines", "Delete to start of line", "⌘⌫ / ⌘K ⌘⌫", [C | KC.Backspace, K(C | KC.Backspace)], "deleteAllLeft"],
      ["Lines", "Delete word", "⌥⌫", [A | KC.Backspace], "deleteWordLeft"],
      ["Lines", "Transpose", "⌃T", [W | KC.KeyT], "editor.action.transposeLetters"],
      ["Lines", "Sort lines", "F5 / ⌃F5", [KC.F5, W | KC.F5], "editor.action.sortLinesAscending"],
      ["Lines", "Upper case", "⌘K ⌘U", [K(C | KC.KeyU)], "editor.action.transformToUppercase"],
      ["Lines", "Lower case", "⌘K ⌘L", [K(C | KC.KeyL)], "editor.action.transformToLowercase"],
      ["Lines", "Title case", "", [], "editor.action.transformToTitlecase"],
      ["Lines", "Trim trailing whitespace", "", [], "editor.action.trimTrailingWhitespace"],
      ["Lines", "Format document", "⌥⇧F", [A | S | KC.KeyF], "editor.action.formatDocument"],
      ["Lines", "Redo", "⌘⇧Z / ⌘Y", [C | KC.KeyY], "redo"],
      ["Goto", "Goto anything", "⌘P / ⌘T", [], function () { palette(""); }],
      ["Goto", "Command palette", "⌘⇧P", [], function () { palette(">"); }],
      ["Goto", "Goto symbol", "⌘R", [C | KC.KeyR], "editor.action.quickOutline"],
      ["Goto", "Goto symbol in project", "⌘⇧R", [C | S | KC.KeyR], "editor.action.quickOutline"],
      ["Goto", "Goto line", "⌃G", [W | KC.KeyG], "editor.action.gotoLine"],
      ["Goto", "Goto definition", "F12 / ⌘⌥↓", [KC.F12, C | A | KC.DownArrow], "editor.action.revealDefinition"],
      ["Goto", "Goto reference", "⇧F12", [S | KC.F12], "editor.action.goToReferences"],
      ["Goto", "Jump to matching bracket", "⌃M", [W | KC.KeyM], "editor.action.jumpToBracket"],
      ["Goto", "Jump back", "⌃-", [W | KC.Minus], function () { jump(back, fwd); }],
      ["Goto", "Jump forward", "⌃⇧-", [W | S | KC.Minus], function () { jump(fwd, back); }],
      ["Goto", "Center on cursor", "⌘K ⌘C", [K(C | KC.KeyC)], center],
      ["Goto", "Scroll line up / down", "⌃⌥↑ / ⌃⌥↓", [W | A | KC.UpArrow], "scrollLineUp"],
      ["Goto", "Scroll line down", "", [W | A | KC.DownArrow], "scrollLineDown"],
      ["Bookmarks", "Toggle bookmark", "⌘F2", [C | KC.F2], toggleMark],
      ["Bookmarks", "Next bookmark", "F2", [KC.F2], function () { nextMark(1); }],
      ["Bookmarks", "Previous bookmark", "⇧F2", [S | KC.F2], function () { nextMark(-1); }],
      ["Bookmarks", "Clear bookmarks", "⌘⇧F2", [C | S | KC.F2], clearMarks],
      ["Folding", "Fold", "⌘⌥[", [C | A | KC.BracketLeft], "editor.fold"],
      ["Folding", "Unfold", "⌘⌥]", [C | A | KC.BracketRight], "editor.unfold"],
      ["Folding", "Fold level 1…7", "⌘K ⌘1…⌘7", [1, 2, 3, 4, 5, 6, 7].map(function (n) { return K(C | KC["Digit" + n]); }), "editor.foldLevel"],
      ["Folding", "Unfold all", "⌘K ⌘0 / ⌘K ⌘J", [K(C | KC.Digit0), K(C | KC.KeyJ)], "editor.unfoldAll"],
      ["Folding", "Fold all", "", [], "editor.foldAll"],
      ["Files", "Save", "⌘S", [], function () { if (current) save(current); }],
      ["Files", "Save all", "⌘⌥S", [], saveAll],
      ["Files", "Save as", "⌘⇧S", [], saveAs],
      ["Files", "New file", "⌥N", [], function () { newItem("", false); }],
      ["Files", "Close file", "⌥W", [], function () { if (current) close(current); }],
      ["Files", "Close all", "⌘K ⌘W", [], closeAll],
      ["Files", "Reopen closed file", "⌥⇧T", [], reopen],
      ["Files", "Switch to file 1…9", "⌘1…⌘9", [], null],
      ["Files", "Next file", "⌘⌥→ / ⌘⇧]", [], function () { cycle(1); }],
      ["Files", "Previous file", "⌘⌥← / ⌘⇧[", [], function () { cycle(-1); }],
      ["View", "Toggle side bar", "⌘K ⌘B", [], toggleSide],
      ["View", "Single column", "⌘⌥1", [], function () { setColumns(1); }],
      ["View", "Two columns", "⌘⌥2", [], function () { setColumns(2); }],
      ["View", "Focus column 1 / 2", "⌃1 / ⌃2", [], function () { focusGroup(focusG ? 0 : 1); }],
      ["View", "Move file to column 1 / 2", "⌃⇧1 / ⌃⇧2", [], function () { toGroup(focusG ? 0 : 1); }],
      ["View", "Full screen", "⌃⌘F", [], fullscreen],
      ["View", "Distraction free", "⌃⌘⇧F", [], zen],
      ["View", "Font bigger / smaller / reset", "⌘= / ⌘- / ⌘0", [], function () { fontBy(1); }],
      ["View", "Toggle word wrap", "⌥Z", [A | KC.KeyZ], toggleOpt("wordWrap", "wrap")],
      ["View", "Toggle minimap", "", [], toggleOpt("minimap", "minimap")],
      ["View", "Toggle whitespace", "", [], toggleOpt("renderWhitespace")],
      ["View", "Keyboard shortcuts", "⌘K ⌘S", [], showKeys]
    ];
  }
  function runEntry(e) {
    var what = e[4];
    if (typeof what === "function") return what();
    if (!what) return;
    var g = ed(); g.focus();
    if (what === "editor.foldLevel") { g.trigger("sublime", "editor.foldLevel1", null); return; }
    g.trigger("sublime", what, null);
  }
  function commands() {
    return sublimeMap().filter(function (e) { return e[4]; }).map(function (e) { return { label: e[0] + ": " + e[1], keys: e[2], run: function () { runEntry(e); } }; });
  }
  function showKeys() {
    var box = $("edkeyslist"); box.textContent = "";
    var sect = "";
    sublimeMap().forEach(function (e) {
      if (!e[2]) return;
      if (e[0] !== sect) { sect = e[0]; box.appendChild(el("h3", "ks", sect)); }
      var r = el("div", "kr"); r.append(el("span", "", e[1]), el("kbd", "", e[2])); box.appendChild(r);
    });
    $("edkeys").showModal();
  }
  $("edkeysbtn").onclick = showKeys;
  $("edkeysclose").onclick = function () { $("edkeys").close(); };

  // App keys run before the browser and Monaco see them (also when the editor isn't focused).
  var chordK = false, chordT = null;
  document.addEventListener("keydown", function (e) {
    var cmd = mac ? e.metaKey : e.ctrlKey, k = e.key.length === 1 ? e.key.toLowerCase() : e.key;
    var hit = function (f) { e.preventDefault(); e.stopPropagation(); f(); };
    if (chordK && cmd && !e.shiftKey) {
      chordK = false; clearTimeout(chordT);
      if (e.code === "KeyB") return hit(toggleSide);
      if (e.code === "KeyS") return hit(showKeys);
      if (e.code === "KeyW") return hit(closeAll);
      return; // the other ⌘K chords are the editor's (⌘K ⌘U, ⌘K ⌘K, ⌘K ⌘1…)
    }
    if (cmd && !e.shiftKey && !e.altKey && e.code === "KeyK") { chordK = true; clearTimeout(chordT); chordT = setTimeout(function () { chordK = false; }, 1500); return; }
    if (cmd && !e.shiftKey && !e.altKey && (e.code === "KeyP" || e.code === "KeyT")) return hit(function () { palette(""); });
    if (cmd && e.shiftKey && !e.altKey && e.code === "KeyP") return hit(function () { palette(">"); });
    if (cmd && e.shiftKey && !e.altKey && !e.ctrlKey && e.code === "KeyF") return hit(openFind);
    if (cmd && !e.shiftKey && !e.altKey && e.code === "KeyS") return hit(function () { if (current) save(current); });
    if (cmd && e.altKey && !e.shiftKey && e.code === "KeyS") return hit(saveAll);
    if (cmd && e.shiftKey && !e.altKey && e.code === "KeyS") return hit(saveAs);
    if (cmd && e.altKey && /^Digit[1-4]$/.test(e.code)) return hit(function () { setColumns(e.code === "Digit1" ? 1 : 2); });
    if (cmd && e.altKey && (k === "ArrowRight" || k === "ArrowLeft")) return hit(function () { cycle(k === "ArrowRight" ? 1 : -1); });
    if (cmd && e.shiftKey && (e.code === "BracketRight" || e.code === "BracketLeft")) return hit(function () { cycle(e.code === "BracketRight" ? 1 : -1); });
    if (cmd && !e.altKey && !e.shiftKey && !e.ctrlKey && /^Digit[1-9]$/.test(e.code)) return hit(function () { var p = order[+e.code.slice(5) - 1]; if (p) show(p); });
    if (cmd && !e.altKey && !e.shiftKey && e.code === "Digit0") return hit(function () { fontSize = 13; fontBy(0); });
    if (cmd && !e.shiftKey && !e.altKey && e.code === "KeyR" && !(document.activeElement && document.activeElement.closest(".monaco-editor"))) return hit(function () { ed().trigger("kb", "editor.action.quickOutline"); });
    if (mac && e.ctrlKey && !e.metaKey && !e.altKey && /^Digit[12]$/.test(e.code)) return hit(function () { if (e.shiftKey) toGroup(e.code === "Digit2" ? 1 : 0); else focusGroup(e.code === "Digit2" ? 1 : 0); });
    // ⌥ keys by physical key (e.code), so they still work where ⌥ types a character on the layout.
    if (e.altKey && !cmd && !e.ctrlKey && !e.shiftKey && e.code === "KeyW") return hit(function () { if (current) close(current); });
    if (e.altKey && !cmd && !e.ctrlKey && e.shiftKey && e.code === "KeyT") return hit(reopen);
    if (e.altKey && !cmd && !e.ctrlKey && !e.shiftKey && e.code === "KeyN") return hit(function () { newItem("", false); });
    if (cmd && !e.shiftKey && !e.altKey && (e.code === "Equal" || e.code === "Minus")) return hit(function () { fontBy(e.code === "Minus" ? -1 : 1); });
    if (e.ctrlKey && e.metaKey && !e.shiftKey && e.code === "KeyF") return hit(fullscreen);
    if (e.ctrlKey && e.metaKey && e.shiftKey && e.code === "KeyF") return hit(zen);
  }, true);
  window.addEventListener("beforeunload", function (e) {
    var unsaved = Object.keys(files).filter(function (p) { return isDirty(files[p]); });
    unsaved.forEach(save);
    if (unsaved.length) { e.preventDefault(); e.returnValue = ""; }
  });

  // Each Monaco column gets the table's editor bindings (they win over Monaco's own defaults).
  function sublimeKeys(g) {
    sublimeMap().forEach(function (e, i) {
      if (!e[3].length || !e[4]) return;
      if (e[4] === "editor.foldLevel") { // one binding per level
        e[3].forEach(function (b, n) { g.addAction({ id: "sublime.fold" + (n + 1), label: "Fold level " + (n + 1), keybindings: [b], run: function (x) { x.trigger("sublime", "editor.foldLevel" + (n + 1), null); } }); });
        return;
      }
      g.addAction({ id: "sublime." + i, label: e[0] + ": " + e[1], keybindings: e[3], run: function (x) {
        if (typeof e[4] === "function") e[4](); else x.trigger("sublime", e[4], null);
      } });
    });
  }

  // Self-test: every binding in the table resolves to its own action, and every Monaco command it names
  // exists. window.hubEditorSelfTest() → {ok, bad: [...]}.
  window.hubEditorSelfTest = function () {
    var g = ed(), svc = g._standaloneKeybindingService, bad = [], ok = 0, known = {};
    g.getSupportedActions().forEach(function (a) { known[a.id] = true; });
    ["cursorUndo", "cursorRedo", "expandLineSelection", "cursorWordPartLeft", "cursorWordPartRight", "cursorWordPartLeftSelect", "cursorWordPartRightSelect",
      "removeSecondaryCursors", "deleteAllRight", "deleteAllLeft", "deleteWordLeft", "redo", "scrollLineUp", "scrollLineDown", "actions.find", "actions.findWithSelection"].forEach(function (c) { known[c] = true; }); // core commands, not "actions"
    sublimeMap().forEach(function (e, i) {
      if (typeof e[4] === "string" && e[4] !== "editor.foldLevel" && !known[e[4]] && !g.getAction(e[4])) bad.push(e[1] + ": no such command " + e[4]);
      if (!e[3].length || !e[4]) return;
      if (e[4] === "editor.foldLevel") { ok++; return; }
      var kb = svc && svc.lookupKeybinding("sublime." + i);
      if (!kb) bad.push(e[1] + ": not bound");
      else ok++;
    });
    return { ok: ok, bad: bad };
  };
  window.hubEditor = { editor: function () { return ed(); }, open: function (p) { open(p); } }; // for tests

  // ------------------------------------------------------------ Monaco
  window.MonacoEnvironment = { getWorkerUrl: function () { return "/static/monaco/vs/base/worker/workerMain.js"; } };
  window.require.config({ paths: { vs: "/static/monaco/vs" } });
  window.require(["vs/editor/editor.main"], function () {
    M = window.monaco;
    var dark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
    var saved = function (k) { try { return JSON.parse(load(k) || "null"); } catch (e) { return null; } };
    var opts = Object.assign({
      model: null, theme: dark ? "vs-dark" : "vs", automaticLayout: true, fontSize: fontSize, minimap: { enabled: true },
      fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace', smoothScrolling: true, scrollBeyondLastLine: false,
      renderWhitespace: "selection", bracketPairColorization: { enabled: true }, guides: { bracketPairs: true, indentation: true },
      tabSize: 2, detectIndentation: true, wordWrap: "off", stickyScroll: { enabled: true }, cursorSmoothCaretAnimation: "on",
      multiCursorModifier: "ctrlCmd", linkedEditing: true, renderLineHighlight: "all", mouseWheelZoom: true, glyphMargin: true
    }, saved("wrap") || {}, saved("minimap") || {});
    [0, 1].forEach(function (i) {
      var g = M.editor.create($("edm" + i), opts);
      g.onDidFocusEditorText(function () { if (focusG !== i) focusGroup(i); });
      g.onDidChangeCursorSelection(function () { if (focusG === i) status(); });
      sublimeKeys(g);
      groups.push(g);
    });
    if (+load("cols") === 2) setColumns(2);
    connect();
  });
})();
