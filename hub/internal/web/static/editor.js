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
      if (at < 0) return -1;
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
        var list = needle ? allFiles.map(function (p) { return [score(p.toLowerCase(), needle), p]; }).filter(function (x) { return x[0] >= 0; })
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
  function gotoLine(n) { var e = ed(); if (!e.getModel() || !n) return; e.setPosition({ lineNumber: n, column: 1 }); e.revealLineInCenter(n); e.focus(); }
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
  // The palette's commands and the shortcut sheet: label, keys, what it does.
  function commands() {
    var t = function (id) { return function () { var e = ed(); e.focus(); e.trigger("kb", id, null); }; };
    return [
      { label: "Goto Anything", keys: "⌘P", run: function () { palette(""); } },
      { label: "Command Palette", keys: "⌘⇧P", run: function () { palette(">"); } },
      { label: "Goto Symbol", keys: "⌘R", run: t("editor.action.quickOutline") },
      { label: "Goto Line", keys: "⌃G", run: t("editor.action.gotoLine") },
      { label: "Goto Definition", keys: "F12", run: t("editor.action.revealDefinition") },
      { label: "Find in Files", keys: "⌘⇧F", run: openFind },
      { label: "Find", keys: "⌘F", run: t("actions.find") },
      { label: "Replace", keys: "⌘⌥F", run: t("editor.action.startFindReplaceAction") },
      { label: "Find Next / Previous", keys: "⌘G / ⌘⇧G", run: t("editor.action.nextMatchFindAction") },
      { label: "Toggle Side Bar", keys: "⌘K ⌘B", run: toggleSide },
      { label: "Single Column", keys: "⌘⌥1", run: function () { setColumns(1); } },
      { label: "Two Columns", keys: "⌘⌥2", run: function () { setColumns(2); } },
      { label: "Focus Column 1 / 2", keys: "⌃1 / ⌃2", run: function () { focusGroup(focusG ? 0 : 1); } },
      { label: "Save", keys: "⌘S", run: function () { if (current) save(current); } },
      { label: "Close File", keys: "⌥W", run: function () { if (current) close(current); } },
      { label: "Reopen Closed File", keys: "⌥⇧T", run: reopen },
      { label: "New File", keys: "⌥N", run: function () { newItem("", false); } },
      { label: "Next / Previous File", keys: "⌘⌥→ / ⌘⌥←", run: function () { cycle(1); } },
      { label: "Switch to File 1…9", keys: "⌘1…⌘9", run: function () { cycle(0); } },
      { label: "Add Next Occurrence", keys: "⌘D", run: t("editor.action.addSelectionToNextFindMatch") },
      { label: "Skip Occurrence", keys: "⌘K ⌘D", run: t("editor.action.moveSelectionToNextFindMatch") },
      { label: "Select All Occurrences", keys: "⌃⌘G", run: t("editor.action.selectHighlights") },
      { label: "Split Selection into Lines", keys: "⌘⇧L", run: t("editor.action.insertCursorAtEndOfEachLineSelected") },
      { label: "Expand Selection to Line", keys: "⌘L", run: t("expandLineSelection") },
      { label: "Expand Selection to Scope", keys: "⌘⇧Space", run: t("editor.action.smartSelect.expand") },
      { label: "Jump to Matching Bracket", keys: "⌃M", run: t("editor.action.jumpToBracket") },
      { label: "Add Cursor Above / Below", keys: "⌃⇧↑ / ⌃⇧↓", run: t("editor.action.insertCursorBelow") },
      { label: "Column Selection", keys: "⌥ drag", run: function () {} },
      { label: "Duplicate Line", keys: "⌘⇧D", run: t("editor.action.copyLinesDownAction") },
      { label: "Delete Line", keys: "⌃⇧K", run: t("editor.action.deleteLines") },
      { label: "Swap Line Up / Down", keys: "⌃⌘↑ / ⌃⌘↓", run: t("editor.action.moveLinesDownAction") },
      { label: "Join Lines", keys: "⌘J", run: t("editor.action.joinLines") },
      { label: "Insert Line After / Before", keys: "⌘↩ / ⌘⇧↩", run: t("editor.action.insertLineAfter") },
      { label: "Toggle Comment", keys: "⌘/", run: t("editor.action.commentLine") },
      { label: "Toggle Block Comment", keys: "⌘⌥/", run: t("editor.action.blockComment") },
      { label: "Indent / Unindent", keys: "⌘] / ⌘[", run: t("editor.action.indentLines") },
      { label: "Upper Case", keys: "⌘K ⌘U", run: t("editor.action.transformToUppercase") },
      { label: "Lower Case", keys: "⌘K ⌘L", run: t("editor.action.transformToLowercase") },
      { label: "Title Case", keys: "", run: t("editor.action.transformToTitlecase") },
      { label: "Sort Lines", keys: "F5", run: t("editor.action.sortLinesAscending") },
      { label: "Fold / Unfold", keys: "⌘⌥[ / ⌘⌥]", run: t("editor.fold") },
      { label: "Fold All", keys: "⌘K ⌘0", run: t("editor.foldAll") },
      { label: "Unfold All", keys: "⌘K ⌘J", run: t("editor.unfoldAll") },
      { label: "Trim Trailing Whitespace", keys: "", run: t("editor.action.trimTrailingWhitespace") },
      { label: "Format Document", keys: "⌥⇧F", run: t("editor.action.formatDocument") },
      { label: "Toggle Word Wrap", keys: "⌥Z", run: toggleOpt("wordWrap", "wrap") },
      { label: "Toggle Minimap", keys: "", run: toggleOpt("minimap", "minimap") },
      { label: "Toggle Whitespace", keys: "", run: toggleOpt("renderWhitespace") },
      { label: "Distraction Free", keys: "⌃⌘⇧F", run: zen },
      { label: "Font Bigger / Smaller", keys: "⌘= / ⌘-", run: function () { fontBy(1); } },
      { label: "Keyboard Shortcuts", keys: "⌘K ⌘S", run: showKeys }
    ];
  }
  function showKeys() {
    var box = $("edkeyslist"); box.textContent = "";
    commands().forEach(function (c) { if (!c.keys) return; var r = el("div", "kr"); r.append(el("span", "", c.label), el("kbd", "", c.keys)); box.appendChild(r); });
    $("edkeys").showModal();
  }
  $("edkeysbtn").onclick = showKeys;
  $("edkeysclose").onclick = function () { $("edkeys").close(); };

  // App keys run before the browser and Monaco see them.
  var chordK = false, chordT = null;
  document.addEventListener("keydown", function (e) {
    var cmd = mac ? e.metaKey : e.ctrlKey, k = e.key.length === 1 ? e.key.toLowerCase() : e.key;
    var hit = function (f) { e.preventDefault(); e.stopPropagation(); f(); };
    if (chordK && cmd && !e.shiftKey) {
      chordK = false; clearTimeout(chordT);
      if (k === "b") return hit(toggleSide);
      if (k === "s") return hit(showKeys);
      return; // other ⌘K chords belong to Monaco (⌘K ⌘U, ⌘K ⌘J, …)
    }
    if (cmd && !e.shiftKey && !e.altKey && k === "k") { chordK = true; clearTimeout(chordT); chordT = setTimeout(function () { chordK = false; }, 1500); return; }
    if (cmd && !e.shiftKey && !e.altKey && (k === "p" || k === "t")) return hit(function () { palette(""); });
    if (cmd && e.shiftKey && k === "p") return hit(function () { palette(">"); });
    if (cmd && e.shiftKey && k === "f") return hit(openFind);
    if (cmd && !e.shiftKey && !e.altKey && k === "s") return hit(function () { if (current) save(current); });
    if (cmd && e.altKey && (e.code === "Digit1" || e.code === "Digit2")) return hit(function () { setColumns(e.code === "Digit2" ? 2 : 1); });
    if (cmd && e.altKey && (k === "ArrowRight" || k === "ArrowLeft")) return hit(function () { cycle(k === "ArrowRight" ? 1 : -1); });
    if (cmd && !e.altKey && !e.shiftKey && /^Digit[1-9]$/.test(e.code)) return hit(function () { var p = order[+e.code.slice(5) - 1]; if (p) show(p); });
    if (mac && e.ctrlKey && !e.metaKey && !e.shiftKey && (e.code === "Digit1" || e.code === "Digit2")) return hit(function () { focusGroup(e.code === "Digit2" ? 1 : 0); });
    if (e.altKey && !cmd && !e.shiftKey && e.code === "KeyW") return hit(function () { if (current) close(current); });
    if (e.altKey && !cmd && e.shiftKey && e.code === "KeyT") return hit(reopen);
    if (e.altKey && !cmd && !e.shiftKey && e.code === "KeyN") return hit(function () { newItem("", false); });
    if (cmd && !e.shiftKey && (k === "=" || k === "+" || k === "-")) return hit(function () { fontBy(k === "-" ? -1 : 1); });
    if (e.ctrlKey && e.metaKey && e.shiftKey && k === "f") return hit(zen);
  }, true);
  window.addEventListener("beforeunload", function (e) {
    var unsaved = Object.keys(files).filter(function (p) { return isDirty(files[p]); });
    unsaved.forEach(save);
    if (unsaved.length) { e.preventDefault(); e.returnValue = ""; }
  });

  // Sublime's editing keys, added to each Monaco column (they win over Monaco's own defaults).
  function sublimeKeys(g) {
    var KM = M.KeyMod, KC = M.KeyCode, C = KM.CtrlCmd, W = KM.WinCtrl, S = KM.Shift, A = KM.Alt;
    [
      [C | W | KC.KeyG, "editor.action.selectHighlights"],
      [C | S | KC.KeyL, "editor.action.insertCursorAtEndOfEachLineSelected"],
      [W | S | KC.KeyK, "editor.action.deleteLines"],
      [C | W | KC.UpArrow, "editor.action.moveLinesUpAction"],
      [C | W | KC.DownArrow, "editor.action.moveLinesDownAction"],
      [C | S | KC.KeyD, "editor.action.copyLinesDownAction"],
      [C | KC.KeyJ, "editor.action.joinLines"],
      [C | KC.KeyR, "editor.action.quickOutline"],
      [C | S | KC.Space, "editor.action.smartSelect.expand"],
      [W | KC.KeyM, "editor.action.jumpToBracket"],
      [C | A | KC.Slash, "editor.action.blockComment"],
      [KC.F5, "editor.action.sortLinesAscending"],
      [W | S | KC.UpArrow, "editor.action.insertCursorAbove"],
      [W | S | KC.DownArrow, "editor.action.insertCursorBelow"],
      [C | A | KC.KeyF, "editor.action.startFindReplaceAction"],
      [C | A | KC.BracketLeft, "editor.fold"],
      [C | A | KC.BracketRight, "editor.unfold"],
      [KM.chord(C | KC.KeyK, C | KC.KeyU), "editor.action.transformToUppercase"],
      [KM.chord(C | KC.KeyK, C | KC.KeyL), "editor.action.transformToLowercase"],
      [KM.chord(C | KC.KeyK, C | KC.KeyD), "editor.action.moveSelectionToNextFindMatch"],
      [KM.chord(C | KC.KeyK, C | KC.KeyJ), "editor.unfoldAll"],
      [KM.chord(C | KC.KeyK, C | KC.Digit0), "editor.foldAll"]
    ].forEach(function (m, i) {
      g.addAction({ id: "sublime." + i, label: "Sublime: " + m[1], keybindings: [m[0]], run: function (x) { x.trigger("sublime", m[1], null); } });
    });
    g.addAction({ id: "sublime.side", label: "Toggle Side Bar", keybindings: [KM.chord(C | KC.KeyK, C | KC.KeyB)], run: toggleSide });
    g.addAction({ id: "sublime.wrap", label: "Toggle Word Wrap", keybindings: [A | KC.KeyZ], run: toggleOpt("wordWrap", "wrap") });
  }

  window.__hubEditor = function () { return { allFiles: allFiles && allFiles.length, asking: !!asking, shown: shown.length, files: Object.keys(files), current: current }; }; // for debugging

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
      multiCursorModifier: "alt", linkedEditing: true, renderLineHighlight: "all", mouseWheelZoom: true
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
