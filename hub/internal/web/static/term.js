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

  var keys = { esc: "\x1b", tab: "\t", stab: "\x1b[Z", up: "\x1b[A", down: "\x1b[B", left: "\x1b[D", right: "\x1b[C",
    ctrlc: "\x03", ctrld: "\x04", enter: "\r" };
  var touch = window.matchMedia && window.matchMedia("(pointer: coarse)").matches;
  document.getElementById("termkeys").addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("button");
    if (!b) return;
    var k = b.getAttribute("data-key"), q = b.getAttribute("data-quick");
    if (k && keys[k]) send(keys[k]);
    else if (q) { pane.paste(q); setTimeout(function () { send("\r"); }, 60); }
    else return;
    if (!touch) pane.term.focus(); // on a phone, keep the keyboard down
  });
  // Dictation into the line (Safari and Chrome have speech recognition; the keyboard's own mic works too).
  var mic = document.getElementById("termmic"), Rec = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (mic && Rec) {
    mic.hidden = false;
    var rec = null;
    mic.addEventListener("click", function () {
      if (rec) { rec.stop(); return; }
      rec = new Rec();
      rec.interimResults = true; rec.continuous = false; rec.lang = navigator.language || "en-US";
      var base = document.getElementById("termin").value;
      rec.onresult = function (ev) {
        var t = "";
        for (var i = 0; i < ev.results.length; i++) t += ev.results[i][0].transcript;
        document.getElementById("termin").value = (base ? base + " " : "") + t;
      };
      rec.onend = rec.onerror = function () { rec = null; mic.setAttribute("aria-pressed", "false"); };
      mic.setAttribute("aria-pressed", "true");
      rec.start();
    });
  }
  // Swipe left/right on the header (or the ‹ › buttons) to move between open terminals.
  var nav = document.getElementById("termnav");
  if (nav) {
    fetch("/api/terminals", { cache: "no-store" }).then(function (r) { return r.json(); }).then(function (list) {
      var open = (list.terminals || list || []).filter(function (t) { return t.status === "live" || t.status === "starting" || t.open; });
      var ids = open.map(function (t) { return String(t.id); });
      var i = ids.indexOf(nav.getAttribute("data-id"));
      if (i < 0 || ids.length < 2) return;
      var prev = ids[(i - 1 + ids.length) % ids.length], next = ids[(i + 1) % ids.length];
      var p = document.getElementById("termprev"), n = document.getElementById("termnext");
      p.href = "/terminals/" + prev; n.href = "/terminals/" + next; p.hidden = n.hidden = false;
      var top = document.querySelector(".termtop"), x0 = null, y0 = 0;
      if (top) {
        top.addEventListener("touchstart", function (e) { x0 = e.touches[0].clientX; y0 = e.touches[0].clientY; }, { passive: true });
        top.addEventListener("touchend", function (e) {
          if (x0 === null) return;
          var dx = e.changedTouches[0].clientX - x0, dy = e.changedTouches[0].clientY - y0;
          x0 = null;
          if (Math.abs(dx) > 70 && Math.abs(dx) > 2 * Math.abs(dy)) location.href = dx < 0 ? n.href : p.href;
        });
      }
    }).catch(function () {});
  }
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
  // Editor beside the terminal (same folder); the choice is remembered per browser.
  var edBtn = document.getElementById("edtoggle"), ed = document.getElementById("termed"), split = document.getElementById("termsplit");
  if (edBtn && ed) {
    var setEd = function (on) {
      if (on && !ed.src) ed.src = edBtn.getAttribute("data-src");
      ed.hidden = !on; split.classList.toggle("on", on); edBtn.classList.toggle("on", on);
      edBtn.textContent = on ? "Hide editor" : "</> Editor";
      try { localStorage.setItem("llmb.termEditor", on ? "1" : ""); } catch (e) { /* private mode */ }
      setTimeout(function () { pane.resize(); }, 50);
    };
    edBtn.addEventListener("click", function () { setEd(ed.hidden); });
    try { if (localStorage.getItem("llmb.termEditor")) setEd(true); } catch (e) { /* private mode */ }
  }

  var endForm = document.querySelector("form.endform");
  if (endForm) endForm.addEventListener("submit", function () { pane.stop(); });
})();
