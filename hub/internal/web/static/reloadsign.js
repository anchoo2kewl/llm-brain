// "Reload to update": the hub restarted with another version under this open tab. Sessions run on the hub and stay
// open, so a reload only loads the new pages. The first version this tab sees is its own.
(function (root) {
  "use strict";
  var EVERY = 5 * 60 * 1000;

  /** The version to announce, or null: a known first version that differs from a valid newer reply. */
  function changed(first, now) {
    return typeof first === "string" && first && typeof now === "string" && now && now !== first ? now : null;
  }

  function start() {
    if (root.top !== root) return; // an embedded pane: its page shows the sign
    var first = null, sign = null;
    function show(v) {
      if (!sign) {
        sign = document.createElement("button");
        sign.type = "button";
        sign.textContent = "Reload to update";
        sign.style.cssText = "position:fixed;right:16px;bottom:16px;z-index:9999;padding:6px 14px;border-radius:999px;" +
          "border:0;background:var(--ok,#15803d);color:#fff;font:600 13px/1.4 system-ui,sans-serif;cursor:pointer;" +
          "box-shadow:0 2px 8px rgba(0,0,0,.25)";
        sign.addEventListener("click", function () { location.reload(); });
        document.body.appendChild(sign);
      }
      sign.title = "The hub now runs " + v + ". Reload this page to use it. Sessions run on the hub and stay open.";
    }
    function check() {
      if (document.hidden && first !== null) return; // a hidden tab still learns its own version at once
      fetch("/api/hub-update/health", { cache: "no-store" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (j) {
          var v = j && j.version;
          if (first === null && typeof v === "string" && v) first = v;
          var next = changed(first, v);
          if (next) show(next);
        })
        .catch(function () {});
    }
    check();
    setInterval(check, EVERY);
    document.addEventListener("visibilitychange", check);
  }

  if (typeof module === "object" && module.exports) module.exports = { changed: changed };
  else if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})(this);
