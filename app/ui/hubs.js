"use strict";
(function () {
  var invoke = window.__TAURI__.core.invoke;
  var REFRESH_MS = 10000;

  var listEl = document.getElementById("hubs");
  var statusEl = document.getElementById("status");
  var nameEl = document.getElementById("name");
  var urlEl = document.getElementById("url");
  var hotkeyEl = document.getElementById("hotkey");
  var hkmsg = document.getElementById("hkmsg");

  var state = { hubs: [], selected: null, hotkey: "", hotkey_error: null };
  var health = {}; // url -> {ok, ms, error}
  var editing = null; // url being renamed

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  function say(text, isErr) {
    statusEl.textContent = text || "";
    statusEl.className = isErr ? "err" : "muted";
  }

  function apply(st) {
    state = st;
    render();
  }

  function fail(e) {
    say(String(e), true);
  }

  function render() {
    hotkeyEl.value = state.hotkey;
    if (state.hotkey_error) {
      hkmsg.textContent = state.hotkey_error;
      hkmsg.className = "err";
    }
    listEl.textContent = "";
    if (!state.hubs.length) {
      listEl.appendChild(el("li", "muted", "No hubs yet. Add one below."));
    }
    state.hubs.forEach(function (h) {
      var li = el("li", h.url === state.selected ? "selected" : "");
      var hs = health[h.url];
      li.appendChild(el("span", "dot " + (hs ? (hs.ok ? "ok" : "bad") : ""), ""));
      var info = el("div", "info");
      if (editing === h.url) {
        var inp = el("input", "rename");
        inp.value = h.name;
        inp.maxLength = 64;
        inp.addEventListener("keydown", function (ev) {
          if (ev.key === "Enter") commitRename(h.url, inp.value);
          if (ev.key === "Escape") {
            editing = null;
            render();
          }
        });
        inp.addEventListener("blur", function () {
          if (editing === h.url) commitRename(h.url, inp.value);
        });
        info.appendChild(inp);
        setTimeout(function () { inp.focus(); inp.select(); }, 0);
      } else {
        info.appendChild(el("div", "hname", h.name));
      }
      info.appendChild(el("div", "hurl", h.url));
      var stat = hs ? (hs.ok ? "reachable, " + hs.ms + " ms" : (hs.error || "unreachable")) : "checking...";
      info.appendChild(el("div", "hstat " + (hs && !hs.ok ? "err" : "muted"), stat));
      li.appendChild(info);

      var actions = el("div", "actions");
      var open = el("button", h.url === state.selected ? "" : "primary", h.url === state.selected ? "Open" : "Use");
      open.addEventListener("click", function () { choose(h.url); });
      var ren = el("button", "", "Rename");
      ren.addEventListener("click", function () {
        editing = h.url;
        render();
      });
      var del = el("button", "danger", "Remove");
      del.addEventListener("click", function () { remove(h.url); });
      actions.appendChild(open);
      actions.appendChild(ren);
      actions.appendChild(del);
      li.appendChild(actions);
      listEl.appendChild(li);
    });
  }

  function commitRename(url, name) {
    editing = null;
    invoke("rename_hub", { url: url, name: name }).then(apply).catch(function (e) {
      fail(e);
      render();
    });
  }

  function choose(url) {
    say("Opening...");
    invoke("select_hub", { url: url })
      .then(function (r) {
        return invoke("get_state").then(function (st) {
          apply(st);
          if (r.reachable) say("");
          else say("That hub did not answer: " + (r.error || "unreachable") + ". The window did not change.", true);
        });
      })
      .catch(fail);
  }

  function remove(url) {
    invoke("remove_hub", { url: url }).then(function (st) {
      delete health[url];
      apply(st);
    }).catch(fail);
  }

  function check() {
    invoke("check_hubs")
      .then(function (rows) {
        health = {};
        rows.forEach(function (r) { health[r.url] = r; });
        render();
      })
      .catch(function () {});
  }

  document.getElementById("add").addEventListener("submit", function (ev) {
    ev.preventDefault();
    invoke("add_hub", { name: nameEl.value, url: urlEl.value })
      .then(function (st) {
        nameEl.value = "";
        urlEl.value = "";
        say("");
        apply(st);
        check();
      })
      .catch(fail);
  });

  // ---- hotkey capture
  var recording = false;
  var recordBtn = document.getElementById("record");

  function stopRecording() {
    recording = false;
    hotkeyEl.classList.remove("recording");
    recordBtn.textContent = "Change";
    hotkeyEl.value = state.hotkey;
  }

  function keyName(ev) {
    var c = ev.code;
    if (/^Key[A-Z]$/.test(c)) return c.slice(3);
    if (/^Digit[0-9]$/.test(c)) return c.slice(5);
    if (/^F([1-9]|1[0-9]|2[0-4])$/.test(c)) return c;
    if (["Space", "Enter", "Tab", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Backspace", "Escape"].indexOf(c) >= 0) return c;
    return null;
  }

  function saveHotkey(text) {
    invoke("set_hotkey", { hotkey: text })
      .then(function (st) {
        hkmsg.textContent = "Saved. Press " + text + " anywhere to open quick search.";
        hkmsg.className = "muted";
        apply(st);
      })
      .catch(function (e) {
        hkmsg.textContent = String(e);
        hkmsg.className = "err";
      });
  }

  recordBtn.addEventListener("click", function () {
    if (recording) return stopRecording();
    recording = true;
    hotkeyEl.classList.add("recording");
    hotkeyEl.value = "Press the new keys...";
    recordBtn.textContent = "Cancel";
    hkmsg.textContent = "";
  });

  document.getElementById("reset").addEventListener("click", function () {
    stopRecording();
    saveHotkey("Alt+Space");
  });

  window.addEventListener("keydown", function (ev) {
    if (!recording) return;
    ev.preventDefault();
    if (["Control", "Alt", "Shift", "Meta"].indexOf(ev.key) >= 0) return;
    if (ev.key === "Escape") return stopRecording();
    var key = keyName(ev);
    var mods = [];
    if (ev.ctrlKey) mods.push("Control");
    if (ev.altKey) mods.push("Alt");
    if (ev.shiftKey) mods.push("Shift");
    if (ev.metaKey) mods.push("Command");
    if (!key || !mods.length) {
      hkmsg.textContent = "Use at least one modifier key (Control, Option, Shift, Command) and a letter, digit, F key or Space.";
      hkmsg.className = "err";
      return;
    }
    var text = mods.concat([key]).join("+");
    stopRecording();
    saveHotkey(text);
  });

  invoke("get_state").then(function (st) {
    apply(st);
    check();
    setInterval(check, REFRESH_MS);
  }).catch(fail);
})();
