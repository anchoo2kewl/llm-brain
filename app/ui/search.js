"use strict";
(function () {
  var invoke = window.__TAURI__.core.invoke;
  var listen = window.__TAURI__.event.listen;
  var DEBOUNCE_MS = 120;

  var input = document.getElementById("q");
  var msg = document.getElementById("msg");
  var list = document.getElementById("list");
  var foot = document.getElementById("foot");
  var hubEl = document.getElementById("hub");
  var keysEl = document.getElementById("keys");
  var box = document.getElementById("box");

  var seq = 0; // newest request; older replies are dropped
  var timer = null;
  var sessions = [];
  var sel = 0;
  var fallbackPath = null; // set when the hub has no /api/search
  var busy = false; // a continue request is running
  var noHub = false;

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text; // always text, never HTML
    return e;
  }

  function resize() {
    invoke("resize_search", { height: box.offsetHeight + 2 }).catch(function () {});
  }

  function setMsg(text, isErr) {
    msg.hidden = !text;
    msg.textContent = text || "";
    msg.className = isErr ? "err" : "muted";
  }

  function render() {
    list.textContent = "";
    sessions.forEach(function (s, i) {
      var li = el("li", i === sel ? "sel" : "");
      li.setAttribute("role", "option");
      var top = el("div", "top");
      top.appendChild(el("span", "title", s.title || "(untitled)"));
      top.appendChild(el("span", "when", window.Rel.relTime(s.last_at)));
      var meta = el("div", "meta");
      meta.appendChild(el("span", "prov " + s.provider, s.provider));
      meta.appendChild(el("span", "", s.host || s.node));
      var dir = window.Rel.baseName(s.cwd);
      if (dir) meta.appendChild(el("span", "", dir + (s.branch ? " @ " + s.branch : "")));
      li.appendChild(top);
      li.appendChild(meta);
      if (s.snippet) li.appendChild(el("div", "snip", s.snippet));
      li.addEventListener("mousedown", function (ev) {
        ev.preventDefault();
        sel = i;
        open(ev.metaKey || ev.ctrlKey);
      });
      list.appendChild(li);
    });
    foot.hidden = false;
    resize();
  }

  function keepSelectionVisible() {
    var li = list.children[sel];
    if (li) li.scrollIntoView({ block: "nearest" });
  }

  function select(i) {
    if (!sessions.length) return;
    sel = (i + sessions.length) % sessions.length;
    Array.prototype.forEach.call(list.children, function (li, j) {
      li.className = j === sel ? "sel" : "";
    });
    keepSelectionVisible();
  }

  function handle(r) {
    fallbackPath = null;
    if (r.kind === "stale") return;
    if (r.kind === "results") {
      sessions = r.sessions || [];
      sel = 0;
      var note = "";
      if (r.partial && r.partial.length) {
        note = r.partial.length + " node" + (r.partial.length > 1 ? "s" : "") + " did not answer: " + r.partial.join(", ");
      }
      if (!sessions.length) setMsg("No sessions found." + (note ? " " + note : ""));
      else setMsg(r.total + " result" + (r.total === 1 ? "" : "s") + " in " + Math.round(r.took_ms) + " ms" + (note ? " - " + note : ""));
      render();
    } else if (r.kind === "fallback") {
      sessions = [];
      fallbackPath = r.path;
      setMsg("This hub has no quick search yet. Press Enter to search in the main window.");
      render();
    } else {
      sessions = [];
      setMsg(r.message || "Search failed.", true);
      render();
    }
  }

  function run() {
    var q = input.value.trim();
    var mine = ++seq;
    if (!q) {
      sessions = [];
      fallbackPath = null;
      setMsg("");
      list.textContent = "";
      foot.hidden = false;
      resize();
      return;
    }
    invoke("search", { q: q, seq: mine })
      .then(function (r) {
        if (mine === seq) handle(r);
      })
      .catch(function (e) {
        if (mine !== seq) return;
        sessions = [];
        setMsg(String(e), true);
        render();
      });
  }

  function open(wantContinue) {
    if (busy) return;
    if (fallbackPath) {
      invoke("open_in_main", { path: fallbackPath }).catch(function (e) { setMsg(String(e), true); resize(); });
      return;
    }
    var s = sessions[sel];
    if (!s) return;
    if (!wantContinue) {
      invoke("open_in_main", { path: s.link }).catch(function (e) { setMsg(String(e), true); resize(); });
      return;
    }
    busy = true;
    setMsg("Continuing session...");
    resize();
    invoke("continue_session", { continueUrl: s.continue_url })
      .catch(function (e) { setMsg(String(e), true); resize(); })
      .then(function () { busy = false; });
  }

  input.addEventListener("input", function () {
    clearTimeout(timer);
    timer = setTimeout(function () {
      timer = null;
      run();
    }, DEBOUNCE_MS);
  });

  window.addEventListener("keydown", function (ev) {
    if (ev.key === "Escape") {
      ev.preventDefault();
      invoke("hide_search").catch(function () {});
    } else if (ev.key === "ArrowDown") {
      ev.preventDefault();
      select(sel + 1);
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      select(sel - 1);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      // A pending debounce means the list is out of date: search first.
      if (timer) {
        clearTimeout(timer);
        timer = null;
        run();
        return;
      }
      open(ev.metaKey || ev.ctrlKey);
    }
  });

  function showHub() {
    invoke("get_state")
      .then(function (st) {
        var h = (st.hubs || []).filter(function (x) { return x.url === st.selected; })[0];
        hubEl.textContent = h ? h.name : "No hub selected";
        if (!h) {
          noHub = true;
          setMsg("No hub selected. Open the hub list from the menu bar icon.", true);
        } else if (noHub) {
          noHub = false;
          setMsg("");
        }
        keysEl.hidden = !h;
        foot.hidden = false;
        resize();
      })
      .catch(function () {});
  }

  function onShown() {
    input.focus();
    input.select();
    showHub();
  }

  listen("search-shown", onShown);
  window.addEventListener("focus", function () { input.focus(); });
  onShown();
})();
