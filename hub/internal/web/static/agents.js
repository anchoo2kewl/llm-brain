// llm-brain hub: chats. Everything inserted is server-rendered (escaped by html/template, or the hub's
// own markdown renderer, which escapes first) or plain text set through textContent.
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);
  const busy = (s) => s === "queued" || s === "running";

  // Enter sends, Shift+Enter is a new line.
  document.addEventListener("keydown", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLTextAreaElement) || !t.hasAttribute("data-enter-send")) return;
    if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
    e.preventDefault();
    if (t.value.trim() && t.form) t.form.requestSubmit();
  });
  // Don't send twice.
  document.addEventListener("submit", (e) => {
    const b = e.target.querySelector("button[type=submit]");
    if (b) setTimeout(() => { b.disabled = true; }, 0);
  });
  // Saved prompt picker: put the prompt in the box.
  document.addEventListener("change", (e) => {
    const sel = e.target;
    if (!(sel instanceof HTMLSelectElement) || !sel.hasAttribute("data-fill-prompt")) return;
    const opt = sel.selectedOptions[0];
    const box = sel.closest("form")?.querySelector("textarea[name=prompt]");
    if (opt && opt.dataset.body && box) {
      box.value = box.value.trim() ? box.value + "\n\n" + opt.dataset.body : opt.dataset.body;
      box.focus();
    }
    sel.value = "";
  });

  // New chat: the options only matter for agent chats.
  const withSel = $("with"), opts = $("options");
  if (withSel && opts) {
    const sync = () => { opts.hidden = !withSel.value.startsWith("agent:"); };
    withSel.addEventListener("change", sync);
    sync();
  }

  const toBottom = () => window.scrollTo(0, document.body.scrollHeight);

  // Session page: load the composer for continuing this session from the hub.
  const ctl = $("agentctl");
  if (ctl && ctl.dataset.src) {
    fetch(ctl.dataset.src + location.search, { cache: "no-store" }).then((r) => r.ok ? r.text() : "").then((html) => {
      if (html) ctl.innerHTML = html;
    }).catch(() => {});
  }

  // Agent chat: refresh the conversation while a turn runs, and for a while after (the transcript is
  // ingested a few seconds behind).
  const convo = $("convo");
  if (convo) {
    toBottom();
    let status = convo.dataset.status, after = busy(status) ? Infinity : 8;
    const tick = async () => {
      try {
        const r = await fetch(convo.dataset.src || $("convo").dataset.src, { cache: "no-store" });
        if (r.ok) {
          const html = await r.text();
          const now = r.headers.get("X-Status") || status;
          const cur = $("convo");
          const near = window.innerHeight + window.scrollY >= document.body.scrollHeight - 300;
          const sel = window.getSelection();
          if (!(sel && !sel.isCollapsed && cur.contains(sel.anchorNode))) {
            const tpl = document.createElement("template");
            tpl.innerHTML = html;
            const fresh = tpl.content.getElementById("convo");
            if (fresh) { cur.replaceWith(fresh); if (near) toBottom(); }
          }
          if (busy(status) && !busy(now)) { location.reload(); return; }
          status = now;
          const st = $("agentstatus");
          if (st) st.textContent = now;
        }
      } catch (e) { /* hub restarting */ }
      if (busy(status)) after = 8; else after--;
      if (after > 0) setTimeout(tick, 3500);
    };
    setTimeout(tick, 3000);
  }

  // Plain chat: stream the reply.
  const live = $("streaming");
  if ($("llmconvo")) toBottom();
  if (live && live.dataset.src && window.EventSource) {
    const txt = live.querySelector(".txt.live");
    const es = new EventSource(live.dataset.src);
    es.addEventListener("delta", (e) => {
      const d = JSON.parse(e.data);
      const near = window.innerHeight + window.scrollY >= document.body.scrollHeight - 300;
      if (txt) txt.textContent = d.text;
      if (near) toBottom();
    });
    es.addEventListener("done", (e) => {
      es.close();
      const d = JSON.parse(e.data);
      const md = document.createElement("div");
      md.className = "md";
      md.innerHTML = d.html; // rendered by the hub from escaped text
      if (txt) txt.replaceWith(md);
      live.querySelector(".typing")?.remove();
      const em = live.querySelector(".err-msg");
      if (d.error && em) { em.textContent = d.error; em.hidden = false; }
      document.querySelectorAll("#composer textarea, #composer button").forEach((el) => { el.disabled = false; });
      document.querySelector("#composer textarea")?.focus();
      toBottom();
    });
    es.onerror = () => { es.close(); setTimeout(() => location.reload(), 3000); };
  }
})();

// Tasks: drop, paste or attach files on the composer; their paths on the host go into the message.
document.addEventListener("DOMContentLoaded", function () {
  if (!window.hubDrop) return;
  document.querySelectorAll("textarea[data-drop-host]").forEach(function (ta) {
    var take = window.hubDrop(ta, ta.getAttribute("data-drop-host"), ta.getAttribute("data-drop-into"), function (paths) {
      var add = paths.map(function (p) { return "Attached: " + p; }).join("\n");
      ta.value = ta.value ? ta.value.replace(/\s*$/, "") + "\n" + add + "\n" : add + "\n";
      ta.focus();
    });
    var form = ta.form, input = form && form.querySelector("input[data-attach]");
    if (input) input.addEventListener("change", function () { take(input.files); input.value = ""; });
  });
});
