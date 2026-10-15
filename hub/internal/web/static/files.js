// Browse a Mac's home folder through the hub (files.go): list, download (the hub keeps a copy), keep, upload.
(function () {
  "use strict";
  var host = document.getElementById("bhost"), path = document.getElementById("bpath");
  if (!host || !path) return;
  var list = document.querySelector("#blist tbody"), msg = document.getElementById("bmsg"), crumbs = document.getElementById("bcrumbs");
  var home = "";
  function say(t) { msg.textContent = t || ""; }
  function size(n) { return n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(1) + " KB" : (n / 1048576).toFixed(1) + " MB"; }
  function tilde(p) { return home && p === home ? "~" : home && p.indexOf(home + "/") === 0 ? "~" + p.slice(home.length) : p; }
  function q(p) { return "/hosts/" + encodeURIComponent(host.value) + p; }
  function cell(tr, text, cls) { var td = document.createElement("td"); if (cls) td.className = cls; if (text !== undefined) td.textContent = text; tr.appendChild(td); return td; }
  function open(p) {
    say("Loading…");
    fetch(q("/ls?path=" + encodeURIComponent(p)), { cache: "no-store" }).then(function (r) { return r.json(); }).then(function (l) {
      if (l.error) { say(l.error); return; }
      home = l.home; path.value = tilde(l.path); say(l.more ? "Showing the first 2000 entries." : "");
      history.replaceState(null, "", "/browse?host=" + encodeURIComponent(host.value) + "&path=" + encodeURIComponent(path.value));
      crumbs.textContent = "";
      var parts = tilde(l.path).split("/"), acc = "";
      parts.forEach(function (seg, i) {
        acc = i === 0 ? seg : acc + "/" + seg;
        var a = document.createElement("a"); a.href = "#"; a.textContent = seg || "/"; var to = acc;
        a.onclick = function (e) { e.preventDefault(); open(to); };
        crumbs.appendChild(a); if (i < parts.length - 1) crumbs.appendChild(document.createTextNode("/"));
      });
      list.textContent = "";
      l.entries.forEach(function (en) {
        var full = l.path + "/" + en.name, tr = document.createElement("tr");
        var name = cell(tr), a = document.createElement("a");
        a.textContent = en.dir ? en.name + "/" : en.name;
        if (en.dir) { a.href = "#"; a.onclick = function (e) { e.preventDefault(); open(full); }; }
        else a.href = q("/download?path=" + encodeURIComponent(full));
        name.appendChild(a);
        cell(tr, en.dir ? "" : size(en.size || 0), "mut small nowrap");
        cell(tr, en.mtime ? en.mtime.slice(0, 16).replace("T", " ") : "", "mut small nowrap");
        var act = cell(tr, undefined, "act");
        if (!en.dir) {
          var k = document.createElement("button"); k.type = "button"; k.textContent = "Keep";
          k.title = "Copy it into the hub's Files without downloading";
          k.onclick = function () {
            k.disabled = true; k.textContent = "Keeping…";
            var body = new URLSearchParams(); body.set("path", full);
            fetch(q("/keep"), { method: "POST", body: body }).then(function (r) { return r.json(); }).then(function (j) {
              if (j.error) { k.textContent = "Failed"; say(j.error); return; }
              k.textContent = "Kept ✓";
            });
          };
          act.appendChild(k);
        }
        list.appendChild(tr);
      });
    }).catch(function (e) { say(String(e)); });
  }
  document.getElementById("bgo").onclick = function () { open(path.value || "~"); };
  path.addEventListener("keydown", function (e) { if (e.key === "Enter") open(path.value || "~"); });
  host.addEventListener("change", function () { open("~"); });
  document.getElementById("bup").addEventListener("change", function (e) {
    var files = Array.prototype.slice.call(e.target.files);
    say("Uploading " + files.length + "…");
    Promise.all(files.map(function (f) {
      var fd = new FormData(); fd.append("file", f);
      return fetch(q("/upload?into=browse"), { method: "POST", body: fd }).then(function (r) { return r.json(); });
    })).then(function (rs) { say(rs.map(function (j) { return j.error || ("saved " + j.path); }).join(" · ")); });
  });
  open(path.value || "~");
})();
