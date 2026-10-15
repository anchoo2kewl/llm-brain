// llm-brain service worker: an offline shell and push notifications. Everything is network-first, since
// the hub is live data; the cache only answers when the tailnet can't.
"use strict";
const CACHE = "llmb-shell-v1";
const SHELL = ["/offline", "/static/tokens.css", "/static/style.css", "/static/insights.css", "/static/ui.css",
  "/static/theme.js", "/static/app.js", "/static/logo.svg", "/static/icons/icon-192.png", "/manifest.webmanifest"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(caches.keys()
    .then((ks) => Promise.all(ks.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/api/") || url.pathname.endsWith("/ws")) return; // live: never from a cache
  if (req.mode === "navigate") {
    e.respondWith(fetch(req).catch(() => caches.match("/offline")));
    return;
  }
  if (url.pathname.startsWith("/static/")) {
    e.respondWith(fetch(req).then((res) => {
      if (res.ok && SHELL.includes(url.pathname)) {
        const copy = res.clone();
        caches.open(CACHE).then((c) => c.put(req, copy));
      }
      return res;
    }).catch(() => caches.match(req)));
  }
});

self.addEventListener("push", (e) => {
  let n = { title: "llm-brain", body: "", url: "/inbox", tag: "" };
  try { n = Object.assign(n, e.data.json()); } catch (err) { if (e.data) n.body = e.data.text(); }
  e.waitUntil(self.registration.showNotification(n.title, {
    body: n.body, tag: n.tag || undefined, renotify: !!n.tag, data: { url: n.url || "/inbox" },
    icon: "/static/icons/icon-192.png", badge: "/static/icons/icon-192.png",
  }));
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const url = new URL((e.notification.data && e.notification.data.url) || "/inbox", self.location.origin).href;
  e.waitUntil(self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((ws) => {
    for (const w of ws) {
      if (new URL(w.url).origin === self.location.origin && "focus" in w) {
        return w.focus().then((f) => (f && "navigate" in f ? f.navigate(url) : f));
      }
    }
    return self.clients.openWindow(url);
  }));
});
