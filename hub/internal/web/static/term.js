// The browser side of a hub terminal (terminals.go): xterm.js on a websocket that the hub bridges to
// `tmux attach` on the host. Binary frames are terminal bytes; text frames are JSON control.
(function () {
  "use strict";
  // New terminal form: the chosen Mac's quick picks, recent folders and folder suggestions.
  var hostSel = document.getElementById("termhost"), pathIn = document.getElementById("termpath");
  if (hostSel && pathIn) {
    var showPicks = function () {
      document.querySelectorAll(".folderpicks .picks").forEach(function (p) {
        var on = p.getAttribute("data-host") === hostSel.value;
        p.hidden = !on;
        if (on) pathIn.setAttribute("list", p.getAttribute("data-list"));
      });
    };
    hostSel.addEventListener("change", showPicks);
    showPicks();
    document.querySelectorAll(".folderpicks .chip").forEach(function (b) {
      b.addEventListener("click", function () { pathIn.value = b.getAttribute("data-path"); pathIn.focus(); });
    });
    // Images and files for the first message: uploaded to the chosen Mac before the terminal exists.
    var prompt = document.getElementById("termprompt"), attach = document.getElementById("termattach");
    if (prompt && window.hubDrop) {
      var takeNew = window.hubDrop(prompt.closest("form") || prompt, function () { return hostSel.value; }, "new", function (paths) {
        var add = paths.map(function (p) { return "Attached: " + p; }).join("\n");
        prompt.value = prompt.value ? prompt.value.replace(/\s*$/, "") + "\n" + add + "\n" : add + "\n";
      });
      if (attach) attach.addEventListener("change", function () { takeNew(attach.files); attach.value = ""; });
    }
  }
  var el = document.getElementById("term");
  if (!el || !window.TermPane) return;
  var id = el.getAttribute("data-term");
  var msg = document.getElementById("termmsg");
  var pill = document.getElementById("termstatus");
  var pane = new window.TermPane({
    id: id, host: el.getAttribute("data-host"), status: el.getAttribute("data-status"), parent: el,
    isFocused: function () { return document.hasFocus(); },
    onSay: function (t) { msg.textContent = t; },
    onStatus: function (s) {
      if (pill && s.status) { pill.textContent = s.status; pill.className = "pill " + (s.status === "live" ? "ok" : s.status === "failed" ? "bad" : s.status === "starting" ? "warn" : ""); }
    }
  });
  pane.term.focus();
  var send = pane.send;

  var keys = { esc: "\x1b", tab: "\t", stab: "\x1b[Z", up: "\x1b[A", down: "\x1b[B", ctrlc: "\x03", enter: "\r" };
  document.getElementById("termkeys").addEventListener("click", function (e) {
    var k = e.target.getAttribute && e.target.getAttribute("data-key");
    if (k && keys[k]) { send(keys[k]); pane.term.focus(); }
  });
  var line = document.getElementById("termin");
  document.getElementById("termline").addEventListener("submit", function (e) {
    e.preventDefault();
    if (!line.value) { send("\r"); return; }
    pane.paste(line.value);
    setTimeout(function () { send("\r"); }, 60);
    line.value = "";
  });
  var fileIn = document.getElementById("termfile");
  if (fileIn && pane.take) fileIn.addEventListener("change", function () { pane.take(fileIn.files); fileIn.value = ""; });
  var endForm = document.querySelector("form.endform");
  if (endForm) endForm.addEventListener("submit", function () { pane.stop(); });
})();
