// relTime("2026-11-13T09:48:00Z") -> "12m ago". Plain script; also loads as a CommonJS module for tests.
(function (root) {
  function relTime(iso, now) {
    var t = Date.parse(iso);
    if (isNaN(t)) return "";
    var s = Math.round(((now === undefined ? Date.now() : now) - t) / 1000);
    if (s < 0) s = 0;
    if (s < 45) return "just now";
    var m = Math.round(s / 60);
    if (m < 60) return m + "m ago";
    var h = Math.round(s / 3600);
    if (h < 24) return h + "h ago";
    var d = Math.round(s / 86400);
    if (d < 14) return d + "d ago";
    var w = Math.round(d / 7);
    if (d < 60) return w + "w ago";
    var mo = Math.round(d / 30);
    if (d < 365) return mo + "mo ago";
    return Math.round(d / 365) + "y ago";
  }

  function baseName(path) {
    var p = String(path || "").replace(/\/+$/, "");
    var i = p.lastIndexOf("/");
    return i >= 0 ? p.slice(i + 1) : p;
  }

  var api = { relTime: relTime, baseName: baseName };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else root.Rel = api;
})(typeof window !== "undefined" ? window : this);
