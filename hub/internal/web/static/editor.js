// The hub's editor: Monaco on a folder of a Mac, over a live file channel (agent/fsbridge.go via /fs/ws).
// Edits autosave a moment after you stop typing; a file changed on disk (by Claude, say) reloads in place,
// or, if you have unsaved edits, asks which to keep. Everything shown is set through textContent or Monaco.
(function () {
  "use strict";
  var root = document.getElementById("ed");
  if (!root || !window.require) return;
  var host = root.dataset.host, folder = root.dataset.root;
  var $ = function (id) { return document.getElementById(id); };
  var state = $("edstate"), banner = $("edbanner"), tabsEl = $("edtabs"), tree = $("edtree");
  var ws = null, seq = 0, waiting = {}, monacoRef = null, editor = null;
  var files = {}; // path -> {model, sha, saved (version id), timer, tab, binary}
  var order = [], current = null, allFiles = null;

  function el(tag, cls, text) { var e = document.createElement(tag); if (cls) e.className = cls; if (text !== undefined) e.textContent = text; return e; }
  function say(t) { state.textContent = t; }

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
        $("edroot").textContent = folder; // as asked (~/…); the full path is in the tooltip
        $("edroot").title = r.root || folder;
        say("connected · " + host);
        loadDir("", tree, 0);
        watch();
        Object.keys(files).forEach(function (p) { refreshFromDisk(p); });
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

  // ------------------------------------------------------------ tree
  function join(dir, name) { return dir ? dir + "/" + name : name; }
  function loadDir(dir, box, depth) {
    return call("list", { path: dir }).then(function (r) {
      box.textContent = "";
      (r.entries || []).forEach(function (en) {
        var p = join(dir, en.name);
        var row = el("div", "ed-node" + (en.dir ? " dir" : "") + (en.name[0] === "." ? " dim" : ""));
        row.style.paddingLeft = (8 + depth * 14) + "px";
        row.setAttribute("role", "treeitem");
        row.dataset.path = p;
        var tw = el("span", "tw", en.dir ? "▸" : ""), nm = el("span", "nm", en.name);
        row.append(tw, nm);
        box.appendChild(row);
        if (en.dir) {
          var kids = el("div"); kids.hidden = true; box.appendChild(kids);
          row.onclick = function () {
            kids.hidden = !kids.hidden;
            tw.textContent = kids.hidden ? "▸" : "▾";
            if (!kids.hidden && !kids.dataset.loaded) { kids.dataset.loaded = "1"; loadDir(p, kids, depth + 1); }
          };
        } else {
          row.onclick = function () { open(p); };
        }
      });
      markTree();
    }).catch(function (e) { box.textContent = ""; box.appendChild(el("div", "ed-node dim", e.message)); });
  }
  function markTree() {
    tree.querySelectorAll(".ed-node").forEach(function (n) { n.classList.toggle("on", n.dataset.path === current); });
  }

  // ------------------------------------------------------------ files and tabs
  function open(path) {
    if (files[path]) { show(path); return; }
    say("opening " + path + "…");
    call("read", { path: path }).then(function (r) {
      var f = { sha: r.sha, binary: r.binary };
      if (r.binary) {
        f.model = monacoRef.editor.createModel("(binary file: not shown)", "plaintext");
      } else {
        f.model = monacoRef.editor.createModel(r.content, undefined, monacoRef.Uri.file("/" + path));
        f.model.onDidChangeContent(function () { dirty(path); });
      }
      f.saved = f.model.getAlternativeVersionId();
      files[path] = f;
      order.push(path);
      addTab(path);
      show(path);
      watch();
      say("connected · " + host);
    }).catch(function (e) { say(path + ": " + e.message); });
  }
  function addTab(path) {
    var t = el("div", "ed-tab"), name = el("span", "", path.split("/").pop()), x = el("button", "x", "✕");
    t.title = path; x.type = "button"; x.title = "Close";
    t.append(name, x);
    t.onclick = function () { show(path); };
    t.onauxclick = function (e) { if (e.button === 1) close(path); };
    x.onclick = function (e) { e.stopPropagation(); close(path); };
    tabsEl.appendChild(t);
    files[path].tab = t;
  }
  function show(path) {
    current = path;
    var f = files[path];
    editor.setModel(f.model);
    editor.updateOptions({ readOnly: !!f.binary });
    Object.keys(files).forEach(function (p) { files[p].tab.classList.toggle("on", p === path); });
    $("edlang").textContent = f.model.getLanguageId();
    hideBanner();
    markTree();
    try { history.replaceState(null, "", location.pathname + "?host=" + encodeURIComponent(host) + "&root=" + encodeURIComponent(folder) + "&file=" + encodeURIComponent(path) + (document.body.dataset.embed ? "&embed=1" : "")); } catch (e) { /* ok */ }
    editor.focus();
  }
  function close(path) {
    var f = files[path];
    if (!f) return;
    if (isDirty(f)) save(path); // never lose an edit: save on close
    f.tab.remove();
    setTimeout(function () { f.model.dispose(); }, 2000);
    delete files[path];
    order = order.filter(function (p) { return p !== path; });
    watch();
    if (current === path) { if (order.length) show(order[order.length - 1]); else { editor.setModel(null); current = null; markTree(); } }
  }
  function isDirty(f) { return !f.binary && f.model.getAlternativeVersionId() !== f.saved; }
  function dirty(path) {
    var f = files[path];
    f.tab.classList.toggle("dirty", isDirty(f));
    clearTimeout(f.timer);
    f.timer = setTimeout(function () { save(path); }, 600);
  }
  function save(path) {
    var f = files[path];
    if (!f || !isDirty(f) || f.saving) return;
    var version = f.model.getAlternativeVersionId(), content = f.model.getValue();
    f.saving = true;
    say("saving " + path + "…");
    call("write", { path: path, content: content, base: f.sha }).then(function (r) {
      f.saving = false;
      if (r.conflict) { conflict(path, r); return; }
      f.sha = r.sha;
      f.saved = version;
      f.tab.classList.toggle("dirty", isDirty(f));
      say("saved " + path.split("/").pop() + " · " + new Date().toLocaleTimeString());
      if (isDirty(f)) dirty(path); // typed more while it was saving
    }).catch(function (e) { f.saving = false; say("couldn't save " + path + ": " + e.message); f.timer = setTimeout(function () { save(path); }, 3000); });
  }
  function watch() { if (ws && ws.readyState === 1) call("watch", { paths: Object.keys(files) }).catch(function () {}); }

  // A file changed on disk: reload it if you have no unsaved edits, else ask.
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
      if (path === current) say(path.split("/").pop() + " changed on disk · reloaded");
    }).catch(function () {});
  }
  function replace(f, content, sha) {
    var pos = editor.getModel() === f.model ? editor.getPosition() : null;
    f.model.pushEditOperations([], [{ range: f.model.getFullModelRange(), text: content }], function () { return null; });
    f.sha = sha;
    f.saved = f.model.getAlternativeVersionId();
    f.tab.classList.remove("dirty");
    if (pos) editor.setPosition(pos);
  }
  function conflict(path, r) {
    var f = files[path];
    show(path);
    banner.textContent = "";
    banner.appendChild(el("span", "", path + " changed on disk while you were editing."));
    var mine = el("button", "", "Keep mine"), theirs = el("button", "", "Load the disk version");
    mine.type = theirs.type = "button";
    mine.onclick = function () { f.sha = r.sha; hideBanner(); save(path); }; // overwrite on purpose
    theirs.onclick = function () {
      call("read", { path: path }).then(function (d) { replace(f, d.content || "", d.sha); hideBanner(); });
    };
    banner.append(mine, theirs);
    banner.hidden = false;
  }
  function hideBanner() { banner.hidden = true; banner.textContent = ""; }

  // ------------------------------------------------------------ quick open (⌘P / Ctrl+P)
  var q = $("edq"), results = $("edresults"), picked = 0, shown = [];
  function score(name, needle) { // subsequence match, earlier and tighter is better
    var i = 0, last = -1, s = 0;
    for (var c = 0; c < needle.length; c++) {
      var at = name.indexOf(needle[c], i);
      if (at < 0) return -1;
      s += at - last;
      last = at; i = at + 1;
    }
    return s + name.length / 100;
  }
  function find() {
    var needle = q.value.toLowerCase().replace(/\s+/g, "");
    if (!needle) { results.hidden = true; return; }
    var run = function () {
      shown = allFiles.map(function (p) { return [score(p.toLowerCase(), needle), p]; })
        .filter(function (x) { return x[0] >= 0; }).sort(function (a, b) { return a[0] - b[0]; }).slice(0, 50).map(function (x) { return x[1]; });
      picked = 0;
      results.textContent = "";
      shown.forEach(function (p, i) {
        var li = el("li", i === 0 ? "on" : "", p);
        li.onmousedown = function (e) { e.preventDefault(); pick(i); };
        results.appendChild(li);
      });
      results.hidden = !shown.length;
    };
    if (allFiles) run(); else call("find").then(function (r) { allFiles = r.files || []; run(); });
  }
  function pick(i) { if (shown[i]) { open(shown[i]); q.value = ""; results.hidden = true; } }
  q.addEventListener("input", find);
  q.addEventListener("focus", function () { allFiles = null; }); // pick up new files
  q.addEventListener("keydown", function (e) {
    var lis = results.children;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (lis[picked]) lis[picked].classList.remove("on");
      picked = Math.max(0, Math.min(shown.length - 1, picked + (e.key === "ArrowDown" ? 1 : -1)));
      if (lis[picked]) { lis[picked].classList.add("on"); lis[picked].scrollIntoView({ block: "nearest" }); }
    } else if (e.key === "Enter") { e.preventDefault(); pick(picked); }
    else if (e.key === "Escape") { q.value = ""; results.hidden = true; if (editor) editor.focus(); }
  });
  document.addEventListener("keydown", function (e) {
    if ((e.metaKey || e.ctrlKey) && !e.shiftKey && (e.key === "p" || e.key === "P")) { e.preventDefault(); q.focus(); q.select(); }
    if ((e.metaKey || e.ctrlKey) && (e.key === "s" || e.key === "S")) { e.preventDefault(); if (current) save(current); }
  }, true);
  window.addEventListener("beforeunload", function (e) {
    var unsaved = Object.keys(files).filter(function (p) { return isDirty(files[p]); });
    unsaved.forEach(save);
    if (unsaved.length) { e.preventDefault(); e.returnValue = ""; }
  });

  // ------------------------------------------------------------ Monaco
  window.MonacoEnvironment = { getWorkerUrl: function () { return "/static/monaco/vs/base/worker/workerMain.js"; } };
  window.require.config({ paths: { vs: "/static/monaco/vs" } });
  window.require(["vs/editor/editor.main"], function () {
    monacoRef = window.monaco;
    var dark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
    editor = monacoRef.editor.create($("edmonaco"), {
      model: null, theme: dark ? "vs-dark" : "vs", automaticLayout: true, fontSize: 13, minimap: { enabled: true },
      fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace', smoothScrolling: true, scrollBeyondLastLine: false,
      renderWhitespace: "selection", bracketPairColorization: { enabled: true }, tabSize: 2, detectIndentation: true
    });
    editor.onDidChangeCursorPosition(function (e) { $("edpos").textContent = "Ln " + e.position.lineNumber + ", Col " + e.position.column; });
    connect();
  });
})();
