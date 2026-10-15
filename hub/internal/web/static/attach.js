// Model chats (sessions.go): drop, paste or pick files on the message box. They're stored on the hub and
// sent with the message: images as images, text files as text. Works before the first message too.
(function () {
  "use strict";
  function wire(form) {
    var ta = form.querySelector("textarea[name=prompt]"), tray = form.querySelector(".atttray"), pick = form.querySelector("input[data-attach-hub]");
    if (!ta || !tray) return;
    var n = 0;
    function sync() { if (n > 0) ta.removeAttribute("required"); else ta.setAttribute("required", ""); }
    function add(f) {
      var chip = document.createElement("span"); chip.className = "attchip";
      if (f.image) { var img = document.createElement("img"); img.src = f.url + "?view=1"; img.alt = ""; chip.appendChild(img); }
      chip.appendChild(document.createTextNode(f.name));
      var x = document.createElement("button"); x.type = "button"; x.textContent = "✕"; x.title = "Remove";
      var hid = document.createElement("input"); hid.type = "hidden"; hid.name = "att"; hid.value = f.id;
      x.onclick = function () { chip.remove(); hid.remove(); n--; sync(); };
      chip.appendChild(x); tray.appendChild(chip); form.appendChild(hid); n++; sync();
    }
    function take(files) {
      files = Array.prototype.slice.call(files || []);
      if (!files.length) return false;
      var fd = new FormData();
      files.forEach(function (f) { fd.append("file", f, f.name || "pasted.png"); });
      tray.classList.add("busy");
      fetch("/llms/upload", { method: "POST", body: fd }).then(function (r) { return r.json(); }).then(function (j) {
        tray.classList.remove("busy");
        if (j.error) { alertBox(j.error); return; }
        j.forEach(add);
        ta.focus();
      }).catch(function (e) { tray.classList.remove("busy"); alertBox(String(e)); });
      return true;
    }
    function alertBox(t) { var p = form.querySelector(".atterr"); if (p) { p.textContent = t; p.hidden = false; } }
    var depth = 0;
    form.addEventListener("dragenter", function (e) { if (e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types, "Files") >= 0) { depth++; form.classList.add("dropzone"); e.preventDefault(); } });
    form.addEventListener("dragover", function (e) { if (e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types, "Files") >= 0) e.preventDefault(); });
    form.addEventListener("dragleave", function () { if (--depth <= 0) { depth = 0; form.classList.remove("dropzone"); } });
    form.addEventListener("drop", function (e) { depth = 0; form.classList.remove("dropzone"); if (e.dataTransfer && e.dataTransfer.files.length) { e.preventDefault(); take(e.dataTransfer.files); } });
    ta.addEventListener("paste", function (e) { if (e.clipboardData && e.clipboardData.files.length && take(e.clipboardData.files)) e.preventDefault(); });
    if (pick) pick.addEventListener("change", function () { take(pick.files); pick.value = ""; });
  }
  document.querySelectorAll("form[data-attach-hub]").forEach(wire);
})();
