// Drop or paste files onto a terminal or a task (files.go): each goes to that Mac under the hub's uploads
// folder (and the hub keeps a copy); the caller gets the paths to hand to Claude/Codex.
(function () {
  "use strict";
  function upload(host, into, files) {
    return Promise.all(files.map(function (f) {
      var fd = new FormData(); fd.append("file", f, f.name || "pasted.png");
      return fetch("/hosts/" + encodeURIComponent(host) + "/upload?into=" + encodeURIComponent(into), { method: "POST", body: fd })
        .then(function (r) { return r.json(); });
    }));
  }
  var note = null;
  function say(t) {
    if (!note) { note = document.createElement("div"); note.className = "dropmsg"; document.body.appendChild(note); }
    note.textContent = t; note.hidden = !t;
    if (t) { clearTimeout(note.t); note.t = setTimeout(function () { note.hidden = true; }, 6000); }
  }
  // wire(el, host, into, onPaths): drag-over highlight, drop, and paste of files on el.
  // host may be a function, for a form whose Mac can still change.
  window.hubDrop = function (el, hostArg, into, onPaths) {
    function take(files) {
      files = Array.prototype.slice.call(files || []);
      if (!files.length) return false;
      var host = typeof hostArg === "function" ? hostArg() : hostArg;
      if (!host) { say("Pick a Mac first."); return true; }
      say("Uploading " + files.map(function (f) { return f.name || "image"; }).join(", ") + " to " + host + "…");
      upload(host, into, files).then(function (rs) {
        var bad = rs.filter(function (j) { return j.error; });
        var paths = rs.filter(function (j) { return j.path; }).map(function (j) { return j.path; });
        say(bad.length ? "Failed: " + bad[0].error : "Uploaded " + paths.length + " file(s); kept in Files.");
        if (paths.length) onPaths(paths);
      }).catch(function (e) { say("Upload failed: " + e); });
      return true;
    }
    var depth = 0;
    el.addEventListener("dragenter", function (e) { if (e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types, "Files") >= 0) { depth++; el.classList.add("dropzone"); e.preventDefault(); } });
    el.addEventListener("dragover", function (e) { if (e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types, "Files") >= 0) e.preventDefault(); });
    el.addEventListener("dragleave", function () { if (--depth <= 0) { depth = 0; el.classList.remove("dropzone"); } });
    el.addEventListener("drop", function (e) {
      depth = 0; el.classList.remove("dropzone");
      if (e.dataTransfer && e.dataTransfer.files.length) { e.preventDefault(); e.stopPropagation(); take(e.dataTransfer.files); }
    }, true);
    el.addEventListener("paste", function (e) {
      if (e.clipboardData && e.clipboardData.files.length && take(e.clipboardData.files)) { e.preventDefault(); e.stopPropagation(); }
    }, true);
    return take;
  };
})();
