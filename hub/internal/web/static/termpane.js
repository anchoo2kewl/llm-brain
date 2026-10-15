// TermPane: one hub terminal (terminals.go) in the browser: xterm.js on a websocket the hub bridges to
// `tmux attach` on the host. Binary frames are terminal bytes; text frames are JSON control. Used by the
// terminal page (term.js) and the workspace (workspace.js), which keeps many attached at once and moves
// their elements between panes.
//
// It also tracks activity, so a list can show who's working and who's waiting for you:
//   working  output in the last ~2.5 s
//   waiting  went quiet after answering what you typed (or rang the bell)
//   idle     quiet, nothing new since you last looked
(function () {
  "use strict";
  var enc = new TextEncoder();
  var dark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;

  window.TermPane = function (opts) {
    var id = opts.id, self = this;
    var el = document.createElement("div");
    el.className = "termpane-x";
    (opts.parent || document.body).appendChild(el);
    var term = new window.Terminal({
      cursorBlink: true, scrollback: 10000, allowProposedApi: true, macOptionIsMeta: true,
      fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace', fontSize: opts.fontSize || (window.innerWidth < 640 ? 12 : 13),
      theme: dark ? { background: "#0d1117", foreground: "#e6edf3" } : { background: "#ffffff", foreground: "#1f2328", cursor: "#1f2328", selectionBackground: "#b6d7ff" }
    });
    var fit = new window.FitAddon.FitAddon();
    term.loadAddon(fit);
    if (window.WebLinksAddon) term.loadAddon(new window.WebLinksAddon.WebLinksAddon());
    term.open(el);
    var ws = null, wanted = true, tries = 0, timer = null, queued = [], visible = opts.visible !== false;
    var lastOut = 0, lastIn = 0, answered = false, state = "connecting", seen = true;

    function say(t) { if (opts.onSay) opts.onSay(t || ""); }
    function setState(s) {
      if (s === state) return;
      var was = state;
      state = s;
      if (opts.onState) opts.onState(s, was);
    }
    function send(data) {
      var b = typeof data === "string" ? enc.encode(data) : data;
      lastIn = Date.now(); answered = false; seen = true;
      if (ws && ws.readyState === 1) ws.send(b);
      else if (queued.length < 500) queued.push(b);
    }
    function resize() {
      if (!visible || !el.offsetParent) return;
      try { fit.fit(); } catch (e) { return; }
      if (ws && ws.readyState === 1) ws.send(JSON.stringify({ t: "resize", cols: term.cols, rows: term.rows }));
    }
    function status(cb) {
      fetch("/terminals/" + id + "/status", { cache: "no-store" }).then(function (r) { return r.json(); }).then(cb).catch(function () { cb({}); });
    }
    function connect() {
      clearTimeout(timer);
      say(tries ? "Reconnecting…" : "Connecting…");
      var proto = location.protocol === "https:" ? "wss://" : "ws://";
      ws = new WebSocket(proto + location.host + "/terminals/" + id + "/ws?cols=" + term.cols + "&rows=" + term.rows);
      ws.binaryType = "arraybuffer";
      ws.onopen = function () { var q = queued; queued = []; q.forEach(function (b) { ws.send(b); }); };
      var got = false;
      ws.onmessage = function (ev) {
        if (!got) { got = true; tries = 0; say(""); setState("idle"); resize(); }
        if (typeof ev.data === "string") return;
        var b = new Uint8Array(ev.data);
        term.write(b);
        lastOut = Date.now();
        if (lastIn && lastOut - lastIn > 300) answered = true;
        for (var i = 0; i < b.length; i++) if (b[i] === 7) { answered = true; seen = false; } // the bell
        setState("working");
      };
      ws.onclose = function (ev) {
        ws = null;
        if (!wanted) return;
        var why = ev.code === 4000 && ev.reason ? ev.reason : "";
        status(function (s) {
          if (opts.onStatus) opts.onStatus(s);
          if (s.status === "starting") { say("Starting…"); setState("connecting"); timer = setTimeout(connect, 1500); return; }
          if (s.status && s.status !== "live") { say(why || ("The terminal has " + s.status + ".") + (s.error ? " " + s.error : "")); setState("ended"); return; }
          if (why) say(why === "detached" ? "Reconnecting…" : why + ". Retrying…");
          setState("connecting");
          tries++;
          timer = setTimeout(connect, Math.min(1000 * tries, 10000));
        });
      };
    }
    // Activity: quiet for 2.5 s after output -> waiting (if it answered you) or idle.
    var tick = setInterval(function () {
      if (state !== "working") return;
      if (Date.now() - lastOut > 2500) {
        if (answered && !self.focused()) seen = false;
        setState(answered && !seen ? "waiting" : "idle");
      }
    }, 500);

    term.onData(function (d) { send(d); });
    term.onBinary(function (d) {
      var b = new Uint8Array(d.length);
      for (var i = 0; i < d.length; i++) b[i] = d.charCodeAt(i) & 255;
      send(b);
    });
    if (opts.keys) term.attachCustomKeyEventHandler(opts.keys);
    var rt = null;
    if (window.ResizeObserver) new ResizeObserver(function () { clearTimeout(rt); rt = setTimeout(resize, 80); }).observe(el);
    if (window.hubDrop && opts.host) {
      this.take = window.hubDrop(el, opts.host, "t" + id, function (paths) {
        self.paste(paths.map(function (p) { return /\s/.test(p) ? "'" + p + "'" : p; }).join(" ") + " ");
        term.focus();
      });
    }

    this.id = id;
    this.el = el;
    this.term = term;
    this.send = send;
    this.resize = resize;
    this.connect = connect;
    this.paste = function (text) { send("\x1b[200~" + text + "\x1b[201~"); }; // bracketed: a TUI takes it as text
    this.state = function () { return state; };
    this.focused = function () { return opts.isFocused ? opts.isFocused() : document.hasFocus(); };
    this.markSeen = function () { seen = true; if (state === "waiting") setState("idle"); };
    this.show = function (on) { visible = on; if (on) setTimeout(resize, 30); };
    this.focus = function () { self.markSeen(); term.focus(); };
    this.lastLine = function () { // the last non-empty line on screen, for previews
      var buf = term.buffer.active;
      for (var y = buf.baseY + term.rows - 1; y >= buf.baseY; y--) {
        var l = buf.getLine(y);
        var t = l ? l.translateToString(true).trim() : "";
        if (t && !/^[─━\-─╭╰│>❯\s]+$/.test(t) && !/bypass permissions|esc to interrupt|shift\+tab/i.test(t)) return t;
      }
      return "";
    };
    this.stop = function () { wanted = false; };
    this.dispose = function () {
      wanted = false; clearInterval(tick); clearTimeout(timer);
      if (ws) try { ws.close(); } catch (e) { /* closing */ }
      term.dispose(); el.remove();
    };
    if (opts.status === "live" || opts.status === "starting") connect();
    else { say("This terminal has " + opts.status + "."); state = "ended"; }
  };
})();
