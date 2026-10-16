// llm-brain hub: the app-wide event stream, live sections, inbox actions, settings and shortcuts. Everything inserted is
// server-rendered (escaped by html/template) HTML or plain text set through textContent.
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);

  // A live section (overview, inbox, now) re-renders from its fragment URL when the event stream says
  // something changed, and at least once a minute.
  const live = $("live");
  let refreshing = false;
  const refreshLive = async () => {
    const cur = document.getElementById("live");
    if (!cur || !cur.dataset.refresh || refreshing) return;
    refreshing = true;
    try {
      const r = await fetch(cur.dataset.refresh, { cache: "no-store" });
      if (!r.ok) return;
      const html = await r.text();
      const tpl = document.createElement("template");
      tpl.innerHTML = html;
      const fresh = tpl.content.getElementById("live");
      if (!fresh) return;
      const current = document.getElementById("live");
      // Keep the person's place: don't replace while they select text or use a control.
      const selection = window.getSelection();
      if ((selection && !selection.isCollapsed && current.contains(selection.anchorNode)) ||
          (current.contains(document.activeElement) && document.activeElement.matches("input, select, button, textarea"))) return;
      const open = new Set(Array.from(current.querySelectorAll("details[id][open]"), d => d.id));
      fresh.querySelectorAll("details[id]").forEach(d => { d.open = open.has(d.id); });
      const focusedID = document.activeElement.closest("details[id]")?.id;
      const scrolls = Array.from(current.querySelectorAll(".wrap, pre"), el => [el.scrollLeft, el.scrollTop]);
      current.replaceWith(fresh);
      fresh.querySelectorAll(".wrap, pre").forEach((el, i) => {
        if (scrolls[i]) { el.scrollLeft = scrolls[i][0]; el.scrollTop = scrolls[i][1]; }
      });
      if (focusedID) document.getElementById(focusedID)?.querySelector("summary")?.focus({ preventScroll: true });
      wireInbox(fresh);
    } catch (e) { /* hub restarting */ } finally { refreshing = false; }
  };
  if (live && live.dataset.refresh) setInterval(refreshLive, 60000);

  // One event stream for the whole app: badges, the tab title, live sections. Only the visible tab holds a
  // connection, and pages inside the workspace don't open one.
  let es = null, lastSnap = null;
  const setBadges = (snap) => {
    document.querySelectorAll("[data-count]").forEach((b) => {
      const n = b.dataset.count === "inbox" ? snap.inbox : (snap.now && snap.now.total) || 0;
      b.textContent = n > 99 ? "99+" : String(n);
      b.hidden = !n;
    });
    const base = document.title.replace(/^\(\d+\+?\) /, "");
    document.title = snap.inbox ? `(${snap.inbox}) ${base}` : base;
    if ("setAppBadge" in navigator) { try { snap.inbox ? navigator.setAppBadge(snap.inbox) : navigator.clearAppBadge(); } catch (e) {} }
  };
  const onSnap = (snap) => {
    const prev = lastSnap;
    lastSnap = snap;
    setBadges(snap);
    if (!prev) return;
    const cur = document.getElementById("live");
    if (!cur) return;
    const kind = cur.dataset.live;
    const moved = kind === "now" ? JSON.stringify(prev.now) !== JSON.stringify(snap.now)
      : kind === "inbox" ? prev.hash !== snap.hash
      : prev.hash !== snap.hash || JSON.stringify(prev.now) !== JSON.stringify(snap.now);
    if (moved) refreshLive();
  };
  const connect = () => {
    if (es || document.body.dataset.embed || !window.EventSource) return;
    es = new EventSource("/api/events");
    es.onmessage = (e) => { try { onSnap(JSON.parse(e.data)); } catch (err) {} };
  };
  const disconnect = () => { if (es) { es.close(); es = null; } };
  document.addEventListener("visibilitychange", () => { document.hidden ? disconnect() : connect(); });
  if (!document.hidden) connect();

  // A small message at the bottom of the screen.
  const toast = (msg, bad) => {
    let t = document.querySelector(".toast");
    if (!t) { t = document.createElement("div"); t.className = "toast"; t.setAttribute("role", "status"); document.body.appendChild(t); }
    t.textContent = msg;
    t.classList.toggle("bad", !!bad);
    t.classList.add("show");
    clearTimeout(t._h);
    t._h = setTimeout(() => t.classList.remove("show"), 2600);
  };
  window.hubToast = toast;

  // Inbox: dismiss without leaving the page; answer an agent or a terminal from the list.
  const seen = (key, at) => {
    const body = new URLSearchParams({ key, at: at || "", format: "json" });
    return fetch("/inbox/seen", { method: "POST", body });
  };
  const removeItem = (li) => {
    li.classList.add("gone");
    setTimeout(() => { li.remove(); if (!document.querySelector(".ibx-item")) refreshLive(); }, 220);
  };
  const termSend = async (id, text, keys) => {
    const body = new URLSearchParams();
    if (text) body.set("text", text);
    body.set("keys", keys || (text ? "enter" : ""));
    const r = await fetch(`/api/terminals/${id}/send`, { method: "POST", body });
    if (!r.ok) { let m = r.status; try { m = (await r.json()).error || m; } catch (e) {} throw new Error(m); }
  };
  function wireInbox(root) {
    root.querySelectorAll("form[data-dismiss]").forEach((f) => f.addEventListener("submit", async (e) => {
      e.preventDefault();
      const li = f.closest(".ibx-item");
      try { const r = await seen(li.dataset.key, li.dataset.at); if (!r.ok) throw new Error(r.status); removeItem(li); }
      catch (err) { toast("Couldn't dismiss: " + err.message, true); }
    }));
    root.querySelectorAll(".ibx-item [data-open]").forEach((a) => a.addEventListener("click", () => {
      const li = a.closest(".ibx-item");
      if (a.target === "_blank") { seen(li.dataset.key, li.dataset.at); removeItem(li); }
    }));
    root.querySelectorAll("form[data-reply]").forEach((f) => f.addEventListener("submit", async (e) => {
      e.preventDefault();
      const li = f.closest(".ibx-item");
      const btns = f.querySelectorAll("button");
      btns.forEach((b) => { b.disabled = true; });
      try {
        const r = await fetch(f.action, { method: "POST", body: new URLSearchParams(new FormData(f)) });
        const err = new URL(r.url).searchParams.get("err");
        if (!r.ok || err) throw new Error(err || r.status);
        toast("Sent");
        seen(li.dataset.key, new Date().toISOString());
        removeItem(li);
      } catch (err) { toast("Not sent: " + err.message, true); btns.forEach((b) => { b.disabled = false; }); }
    }));
    root.querySelectorAll(".ibx-reply[data-term]").forEach((box) => {
      const id = box.dataset.term, li = box.closest(".ibx-item");
      const done = () => { toast("Sent"); seen(li.dataset.key, new Date().toISOString()); removeItem(li); };
      box.querySelectorAll("button[data-say]").forEach((b) => b.addEventListener("click", async () => {
        try { await termSend(id, b.dataset.say); done(); } catch (err) { toast("Not sent: " + err.message, true); }
      }));
      box.querySelectorAll("button[data-keys]").forEach((b) => b.addEventListener("click", async () => {
        try { await termSend(id, "", b.dataset.keys); toast("Sent " + b.textContent); } catch (err) { toast("Not sent: " + err.message, true); }
      }));
      const f = box.querySelector("form[data-term-say]");
      if (f) f.addEventListener("submit", async (e) => {
        e.preventDefault();
        const t = f.elements.text.value;
        if (!t) return;
        try { await termSend(id, t); done(); } catch (err) { toast("Not sent: " + err.message, true); }
      });
    });
  }
  wireInbox(document);

  // Run detail: tail the log.
  const log = $("runlog");
  if (log && log.dataset.live === "1") {
    let next = parseInt(log.dataset.next, 10) || 0;
    const tick = async () => {
      try {
        const r = await fetch(`/runs/${log.dataset.run}/log?from=${next}`, { cache: "no-store" });
        const d = await r.json();
        if (d.text) {
          const stick = log.scrollTop + log.clientHeight >= log.scrollHeight - 30;
          log.appendChild(document.createTextNode(d.text));
          if (stick) log.scrollTop = log.scrollHeight;
        }
        next = d.next;
        const st = $("logstate");
        if (d.ended) { if (st) st.textContent = "finished: " + d.status; return; }
      } catch (e) {}
      setTimeout(tick, 3000);
    };
    log.scrollTop = log.scrollHeight;
    setTimeout(tick, 3000);
  }

  // Session detail: append new transcript items while the session is live.
  const tr = $("transcript");
  if (tr) {
    const ht = $("hidetools"), hm = $("hidemeta");
    if (ht) ht.addEventListener("change", () => tr.classList.toggle("hidetools", ht.checked));
    if (hm) hm.addEventListener("change", () => tr.classList.toggle("hidemeta", hm.checked));
    tr.addEventListener("click", (e) => {
      const b = e.target.closest("button.more");
      if (!b) return;
      const t = b.previousElementSibling;
      if (t) t.classList.remove("clamp");
      b.remove();
    });
    if (document.body.dataset.embed) window.scrollTo(0, document.body.scrollHeight); // the workspace shows the newest
    if (tr.dataset.live === "1") {
      let after = tr.dataset.after || "0";
      let quiet = 0;
      const tick = async () => {
        try {
          const r = await fetch(`/sessions/${tr.dataset.session}/more?after=${after}&view=${tr.dataset.view || "chat"}`, { cache: "no-store" });
          const html = await r.text();
          const last = r.headers.get("X-Last-Id");
          if (html.trim()) {
            const nearBottom = window.innerHeight + window.scrollY >= document.body.scrollHeight - 200;
            tr.insertAdjacentHTML("beforeend", html);
            if (nearBottom) window.scrollTo(0, document.body.scrollHeight);
          }
          if (last) after = last;
          quiet = r.headers.get("X-Live") === "true" ? 0 : quiet + 1;
          // A page of its own gives up after a quiet spell; one inside the workspace keeps listening, slower.
          if (quiet > 20 && !document.body.dataset.embed) { const s = $("livestate"); if (s) s.textContent = "session went quiet"; return; }
        } catch (e) {}
        setTimeout(tick, quiet > 20 ? 15000 : 4000);
      };
      setTimeout(tick, 4000);
    }
  }

  // Rename: click a title (or its ✎) to edit it; Enter saves, Escape cancels. The hub renames the linked
  // terminal / task / transcript too, and tells Claude (/rename) when it's a Claude terminal.
  document.querySelectorAll("[data-rename]").forEach((el) => {
    const pen = document.createElement("button");
    pen.type = "button"; pen.className = "renamebtn"; pen.textContent = "✎"; pen.title = "Rename"; pen.setAttribute("aria-label", "Rename");
    el.after(pen);
    let before = "";
    const start = () => {
      if (el.isContentEditable) return;
      before = el.textContent;
      el.contentEditable = "true"; el.classList.add("renaming"); el.focus();
      document.getSelection().selectAllChildren(el);
    };
    const stop = async (save) => {
      if (!el.isContentEditable) return;
      el.contentEditable = "false"; el.classList.remove("renaming");
      const name = el.textContent.replace(/\s+/g, " ").trim();
      if (!save || !name || name === before) { el.textContent = before; return; }
      try {
        const body = new URLSearchParams({ title: name });
        const r = await fetch(el.dataset.rename, { method: "POST", body });
        const j = await r.json();
        if (!r.ok) throw new Error(j.error || r.status);
        el.textContent = j.title;
        document.title = j.title + " · llm-brain hub";
      } catch (e) { el.textContent = before; pen.title = "Rename failed: " + e.message; pen.classList.add("bad"); }
    };
    el.addEventListener("click", start);
    pen.addEventListener("click", start);
    el.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); stop(true); }
      if (e.key === "Escape") { e.preventDefault(); stop(false); }
    });
    el.addEventListener("blur", () => stop(true));
  });
  // Menus: close the More menu / sheet on outside click, Escape, or navigation.
  const menus = () => document.querySelectorAll("details.menu[open], details.tab-more[open]");
  document.addEventListener("click", (e) => {
    menus().forEach((d) => { if (!d.contains(e.target) || e.target.closest("a")) d.open = false; });
  });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") menus().forEach((d) => { d.open = false; }); });

  // Keyboard: "/" searches, "g" then a letter goes somewhere, "c" starts something. Not while typing, and
  // not on pages that own the keyboard (terminals, the workspace, the editor).
  const owns = () => document.querySelector(".ws, #term, .monaco-editor");
  let g = 0;
  const go = { i: "/inbox", n: "/now", w: "/workspace", h: "/chats", s: "/sessions", r: "/runs", f: "/fleet", t: "/insights", c: "/settings" };
  document.addEventListener("keydown", (e) => {
    if (e.metaKey || e.ctrlKey || e.altKey || e.defaultPrevented) return;
    const t = e.target;
    if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
    if (owns()) return;
    if (e.key === "/") { const q = document.querySelector(".gsearch input"); if (q && q.offsetParent) { e.preventDefault(); q.focus(); q.select(); } else { location.href = "/search"; } return; }
    if (g && Date.now() - g < 1200 && go[e.key]) { e.preventDefault(); g = 0; location.href = go[e.key]; return; }
    g = e.key === "g" ? Date.now() : 0;
    if (e.key === "c") { location.href = "/new"; }
  });

  // Settings: the theme (this browser), and notifications (this device).
  const pick = document.querySelector("[data-theme-pick]");
  if (pick) {
    const cur = () => { try { return localStorage.getItem("llmb.theme") || ""; } catch (e) { return ""; } };
    const mark = () => pick.querySelectorAll("button").forEach((b) => {
      const on = b.dataset.themeV === cur();
      b.classList.toggle("on", on); b.setAttribute("aria-checked", on ? "true" : "false"); b.setAttribute("role", "radio");
    });
    pick.addEventListener("click", (e) => {
      const b = e.target.closest("button[data-theme-v]");
      if (!b) return;
      const v = b.dataset.themeV;
      try { v ? localStorage.setItem("llmb.theme", v) : localStorage.removeItem("llmb.theme"); } catch (err) {}
      if (v) document.documentElement.setAttribute("data-theme", v); else document.documentElement.removeAttribute("data-theme");
      mark();
    });
    mark();
  }
  const pushctl = $("pushctl");
  if (pushctl) {
    const on = $("pushon"), off = $("pushoff"), test = $("pushtest"), msg = $("pushmsg");
    const say = (t) => { msg.textContent = t; };
    const b64 = (s) => { const p = "=".repeat((4 - s.length % 4) % 4); const r = atob((s + p).replace(/-/g, "+").replace(/_/g, "/")); return Uint8Array.from(r, (c) => c.charCodeAt(0)); };
    const reg = () => navigator.serviceWorker.register("/sw.js").then(() => navigator.serviceWorker.ready);
    const post = (u, body) => fetch(u, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body || {}) });
    const label = () => {
      const ua = navigator.userAgent;
      const dev = /iPhone/.test(ua) ? "iPhone" : /iPad|Macintosh.*Mobile/.test(ua) || (navigator.maxTouchPoints > 1 && /Macintosh/.test(ua)) ? "iPad" : /Android/.test(ua) ? "Android" : /Mac/.test(ua) ? "Mac" : "browser";
      const br = /Edg\//.test(ua) ? "Edge" : /Chrome\//.test(ua) ? "Chrome" : /Firefox\//.test(ua) ? "Firefox" : /Safari\//.test(ua) ? "Safari" : "";
      return `${dev}${br ? " · " + br : ""}${matchMedia("(display-mode: standalone)").matches ? " · app" : ""}`;
    };
    const refresh = async () => {
      if (!("serviceWorker" in navigator) || !("PushManager" in window) || !window.isSecureContext) {
        say(window.isSecureContext ? "This browser can't receive notifications." : "Open the hub over HTTPS to turn notifications on (see below).");
        return;
      }
      const r = await reg();
      const sub = await r.pushManager.getSubscription();
      on.hidden = !!sub; off.hidden = !sub;
      if (Notification.permission === "denied") say("Notifications are blocked for this site in the browser's settings.");
    };
    on.addEventListener("click", async () => {
      try {
        const perm = await Notification.requestPermission();
        if (perm !== "granted") { say("Not allowed."); return; }
        const r = await reg();
        const k = await (await fetch("/api/push/key")).json();
        const sub = await r.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64(k.key) });
        const res = await post("/api/push/subscribe", Object.assign(sub.toJSON(), { label: label() }));
        if (!res.ok) throw new Error((await res.json()).error || res.status);
        say("On for this device."); test.hidden = false; refresh();
      } catch (err) { say("Couldn't turn on: " + err.message); }
    });
    off.addEventListener("click", async () => {
      try {
        const r = await reg();
        const sub = await r.pushManager.getSubscription();
        if (sub) { await post("/api/push/unsubscribe", { endpoint: sub.endpoint }); await sub.unsubscribe(); }
        say("Off for this device."); refresh();
      } catch (err) { say("Couldn't turn off: " + err.message); }
    });
    test.addEventListener("click", async () => {
      const r = await post("/api/push/test");
      say(r.ok ? "Sent. It should arrive in a few seconds." : "Failed: " + ((await r.json()).error || r.status));
    });
    refresh().catch((err) => say(err.message));
  }

  // Offline page: try again, and come back by itself when the hub answers.
  document.querySelectorAll("[data-reload]").forEach((b) => b.addEventListener("click", () => location.reload()));
  if (document.querySelector("[data-reload]")) setInterval(() => fetch("/healthz", { cache: "no-store" }).then((r) => { if (r.ok) location.reload(); }).catch(() => {}), 5000);

  // The installable app: register the service worker where the browser allows it (HTTPS or localhost).
  if ("serviceWorker" in navigator && window.isSecureContext && !document.body.dataset.embed) {
    navigator.serviceWorker.register("/sw.js").catch(() => {});
  }

  // Terminal | Chat: the viewer's choice, kept in localStorage and in the hub_view cookie (so the hub renders
  // it the first time). Terminal is the default. A link with ?view= shows that view once without changing it.
  window.hubView = {
    get() {
      try { const v = localStorage.getItem("hub.view"); if (v === "term" || v === "chat") return v; } catch (e) {}
      const m = document.cookie.match(/(?:^|; )hub_view=(term|chat)/);
      return m ? m[1] : "term";
    },
    set(v) {
      try { localStorage.setItem("hub.view", v); } catch (e) {}
      try { document.cookie = "hub_view=" + v + "; path=/; max-age=31536000; samesite=lax"; } catch (e) {}
    },
  };
  {
    // The cookie lost (cleared, another port) but localStorage kept: put it back.
    const want = window.hubView.get();
    if (!/(?:^|; )hub_view=/.test(document.cookie) && want !== "term") window.hubView.set(want);
  }
  document.addEventListener("click", (e) => {
    const a = e.target.closest(".viewseg [data-view]");
    if (a && a.tagName === "A") window.hubView.set(a.dataset.view);
  });
})();
