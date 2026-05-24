(function () {
  "use strict";

  const root = document.getElementById("natrun-root");
  const contextEl = document.getElementById("natrun-context");

  // Always start from the server's freshly-signed initial context. We do NOT
  // restore from localStorage across page loads: the server may have rotated
  // its HMAC secret since last session, and even if it hasn't, stale history
  // would suppress the auto-start guard below.
  let context = null;
  try { context = JSON.parse(contextEl.textContent); } catch (_) { context = null; }

  function persist(ctx) { context = ctx; }

  // Signal "a model call is in flight" by toggling a class on the root. The
  // model owns all styling, so it can optionally target #natrun-root.natrun-busy
  // in its <style> block to show a visual cue, or ignore it entirely.
  function setBusy(on) { root.classList.toggle("natrun-busy", !!on); }

  async function send(action) {
    setBusy(true);
    try {
      const res = await fetch("/act", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action, context }),
      });
      if (res.status === 400) {
        // Bad signature — server rotated its secret, or the carried context
        // is corrupt. Reload the page to get a fresh signed initial context.
        window.location.reload();
        return;
      }
      if (!res.ok) {
        root.innerHTML = '<div>the page glitched. <button data-natrun-action=\'{"type":"retry"}\'>retry</button></div>';
        return;
      }
      const data = await res.json();
      if (data.html != null) root.innerHTML = data.html;
      if (data.context != null) persist(data.context);
    } catch (err) {
      root.innerHTML = '<div>network error. <button data-natrun-action=\'{"type":"retry"}\'>retry</button></div>';
    } finally {
      setBusy(false);
    }
  }

  function parseAction(raw) {
    if (!raw) return null;
    try { return JSON.parse(raw); } catch (_) { return { type: "raw", value: raw }; }
  }

  root.addEventListener("click", function (e) {
    const target = e.target.closest("[data-natrun-action]");
    if (!target) return;
    e.preventDefault();
    const action = parseAction(target.getAttribute("data-natrun-action"));
    if (action) send(action);
  });

  root.addEventListener("submit", function (e) {
    const form = e.target.closest("form[data-natrun-action]");
    if (!form) return;
    e.preventDefault();
    const action = parseAction(form.getAttribute("data-natrun-action"));
    if (!action) return;
    const fd = new FormData(form);
    action.form = {};
    for (const [k, v] of fd.entries()) action.form[k] = v;
    send(action);
  });

  // Auto-start: the server's initial context always has empty history, so we
  // always fire {"type":"open"} immediately. The user never sees a button.
  send({ type: "open" });
})();
