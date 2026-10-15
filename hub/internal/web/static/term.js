// The browser side of a hub terminal (terminals.go): xterm.js on a websocket that the hub bridges to
// `tmux attach` on the host. Binary frames are terminal bytes; text frames are JSON control.
(function () {
  "use strict";
  // New terminal form: only offer the folders of the chosen Mac.
  var hostSel = document.getElementById("termhost"), folderSel = document.getElementById("termfolder");
  if (hostSel && folderSel) {
    var only = function () {
      folderSel.querySelectorAll("optgroup").forEach(function (g) {
        var on = g.getAttribute("data-host") === hostSel.value;
        g.hidden = !on; g.disabled = !on;
      });
      var o = folderSel.selectedOptions[0];
      if (o && o.parentElement && o.parentElement.disabled) folderSel.value = "";
    };
    hostSel.addEventListener("change", only);
    only();
  }
  var el = document.getElementById("term");
  if (!el || !window.Terminal) return;
  var id = el.getAttribute("data-term");
  var msg = document.getElementById("termmsg");
  var pill = document.getElementById("termstatus");
  var enc = new TextEncoder();
  var dark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  var term = new window.Terminal({
    cursorBlink: true, scrollback: 5000, allowProposedApi: true, macOptionIsMeta: true,
    fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace', fontSize: window.innerWidth < 640 ? 12 : 13,
    theme: dark ? { background: "#0d1117", foreground: "#e6edf3" } : { background: "#ffffff", foreground: "#1f2328", cursor: "#1f2328", selectionBackground: "#b6d7ff" }
  });
  var fit = new window.FitAddon.FitAddon();
  term.loadAddon(fit);
  if (window.WebLinksAddon) term.loadAddon(new window.WebLinksAddon.WebLinksAddon());
  term.open(el);
  fit.fit();
  var ws = null, wanted = true, tries = 0, timer = null;

  function say(t) { msg.textContent = t || ""; }
  function send(data) {
    if (ws && ws.readyState === 1) ws.send(typeof data === "string" ? enc.encode(data) : data);
  }
  function resize() {
    try { fit.fit(); } catch (e) { return; }
    if (ws && ws.readyState === 1) ws.send(JSON.stringify({ t: "resize", cols: term.cols, rows: term.rows }));
  }
  function status(cb) {
    fetch("/terminals/" + id + "/status", { cache: "no-store" }).then(function (r) { return r.json(); }).then(function (s) {
      if (pill && s.status) { pill.textContent = s.status; pill.className = "pill " + (s.status === "live" ? "ok" : s.status === "failed" ? "bad" : s.status === "starting" ? "warn" : ""); }
      cb(s);
    }).catch(function () { cb({}); });
  }
  function connect() {
    clearTimeout(timer);
    say(tries ? "Reconnecting…" : "Connecting…");
    var proto = location.protocol === "https:" ? "wss://" : "ws://";
    ws = new WebSocket(proto + location.host + "/terminals/" + id + "/ws?cols=" + term.cols + "&rows=" + term.rows);
    ws.binaryType = "arraybuffer";
    var got = false;
    ws.onmessage = function (ev) {
      if (!got) { got = true; tries = 0; say(""); term.focus(); }
      if (typeof ev.data === "string") return;
      term.write(new Uint8Array(ev.data));
    };
    ws.onclose = function (ev) {
      ws = null;
      if (!wanted) return;
      var why = ev.code === 4000 && ev.reason ? ev.reason : "";
      status(function (s) {
        if (s.status === "starting") { say("Starting…"); timer = setTimeout(connect, 1500); return; }
        if (s.status && s.status !== "live") { say(why || ("The terminal has " + s.status + ".") + (s.error ? " " + s.error : "")); return; }
        if (why === "detached") { say("Detached (someone ended the attach). Reconnecting…"); }
        else if (why) { say(why + ". Retrying…"); }
        tries++;
        timer = setTimeout(connect, Math.min(1000 * tries, 10000));
      });
    };
  }

  term.onData(function (d) { send(d); });
  term.onBinary(function (d) {
    var b = new Uint8Array(d.length);
    for (var i = 0; i < d.length; i++) b[i] = d.charCodeAt(i) & 255;
    send(b);
  });
  var rt = null;
  window.addEventListener("resize", function () { clearTimeout(rt); rt = setTimeout(resize, 120); });
  if (window.ResizeObserver) new ResizeObserver(function () { clearTimeout(rt); rt = setTimeout(resize, 120); }).observe(el);

  var keys = { esc: "\x1b", tab: "\t", stab: "\x1b[Z", up: "\x1b[A", down: "\x1b[B", ctrlc: "\x03", enter: "\r" };
  document.getElementById("termkeys").addEventListener("click", function (e) {
    var k = e.target.getAttribute && e.target.getAttribute("data-key");
    if (k && keys[k]) { send(keys[k]); term.focus(); }
  });
  var line = document.getElementById("termin");
  document.getElementById("termline").addEventListener("submit", function (e) {
    e.preventDefault();
    if (!line.value) { send("\r"); return; }
    send("\x1b[200~" + line.value + "\x1b[201~"); // bracketed paste: a TUI takes it as text, not keys
    setTimeout(function () { send("\r"); }, 60);
    line.value = "";
  });

  // End needs a second click instead of a confirm() dialog.
  document.querySelectorAll("form[data-confirm]").forEach(function (f) {
    f.addEventListener("submit", function (e) {
      var b = f.querySelector("button");
      if (f.getAttribute("data-armed")) { wanted = false; return; }
      e.preventDefault();
      f.setAttribute("data-armed", "1");
      b.textContent = "Click again to end";
      setTimeout(function () { f.removeAttribute("data-armed"); b.textContent = "End"; }, 4000);
    });
  });

  if (el.getAttribute("data-status") === "live" || el.getAttribute("data-status") === "starting") connect();
  else say("This terminal has " + el.getAttribute("data-status") + ".");
})();
