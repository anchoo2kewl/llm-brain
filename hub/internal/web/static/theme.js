// Runs before the first paint: apply the theme picked in Settings (stored per browser), so a page never
// flashes the wrong one. hubDark() tells terminals and the editor which palette to use.
(function () {
  "use strict";
  try {
    var t = localStorage.getItem("llmb.theme");
    if (t === "light" || t === "dark") document.documentElement.setAttribute("data-theme", t);
  } catch (e) { /* private mode */ }
  window.hubDark = function () {
    var t = document.documentElement.getAttribute("data-theme");
    if (t) return t === "dark";
    return !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
  };
})();
