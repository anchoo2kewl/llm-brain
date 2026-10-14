// llm-brain hub: live updates by polling. Everything inserted is either server-rendered (escaped by
// html/template) HTML or plain text set through textContent.
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);

  // Overview: re-render the live section every 15 s.
  const live = $("live");
  if (live && live.dataset.refresh) {
    setInterval(async () => {
      try {
        const r = await fetch(live.dataset.refresh, { cache: "no-store" });
        if (!r.ok) return;
        const html = await r.text();
        const tpl = document.createElement("template");
        tpl.innerHTML = html;
        const fresh = tpl.content.getElementById("live");
        if (fresh) {
          const current = document.getElementById("live");
          // Keep the user's inspection state while updating live counts.
          // Avoid replacing content while they are selecting text or using a control.
          const selection = window.getSelection();
          if ((selection && !selection.isCollapsed && current.contains(selection.anchorNode)) ||
              (current.contains(document.activeElement) && document.activeElement.matches("input, select, button"))) return;
          const open = new Set(Array.from(current.querySelectorAll("details[id][open]"), d => d.id));
          fresh.querySelectorAll("details[id]").forEach(d => { d.open = open.has(d.id); });
          const focusedID = document.activeElement.closest("details[id]")?.id;
          const scrolls = Array.from(current.querySelectorAll(".wrap, pre"), el => [el.scrollLeft, el.scrollTop]);
          current.replaceWith(fresh);
          fresh.querySelectorAll(".wrap, pre").forEach((el, i) => {
            if (scrolls[i]) { el.scrollLeft = scrolls[i][0]; el.scrollTop = scrolls[i][1]; }
          });
          if (focusedID) document.getElementById(focusedID)?.querySelector("summary")?.focus({ preventScroll: true });
        }
      } catch (e) { /* hub restarting */ }
    }, 15000);
  }

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
    if (tr.dataset.live === "1") {
      let after = tr.dataset.after || "0";
      let quiet = 0;
      const tick = async () => {
        try {
          const r = await fetch(`/sessions/${tr.dataset.session}/more?after=${after}`, { cache: "no-store" });
          const html = await r.text();
          const last = r.headers.get("X-Last-Id");
          if (html.trim()) {
            const nearBottom = window.innerHeight + window.scrollY >= document.body.scrollHeight - 200;
            tr.insertAdjacentHTML("beforeend", html);
            if (nearBottom) window.scrollTo(0, document.body.scrollHeight);
          }
          if (last) after = last;
          quiet = r.headers.get("X-Live") === "true" ? 0 : quiet + 1;
          if (quiet > 20) { const s = $("livestate"); if (s) s.textContent = "session went quiet"; return; }
        } catch (e) {}
        setTimeout(tick, 4000);
      };
      setTimeout(tick, 4000);
    }
  }
})();
