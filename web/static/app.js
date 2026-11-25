// Meal Planner client. Small vanilla JS only (package C):
//   - service worker registration
//   - offline badge
//   - offline tick queue (localStorage), replayed when back online
//   - plan-generation progress counter
//   - copy buttons, flash dismiss
//   - cook mode: one step at a time, timers, Screen Wake Lock
// Every handler is delegated from document, so it survives hx-boost swaps.
(() => {
  "use strict";

  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/sw.js").catch((err) => console.warn("sw", err));
  }

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  // ---- offline tick queue --------------------------------------------------
  // Pending ticks are { itemId: checked }: the last tap wins, and the server
  // endpoint sets (not toggles) the state, so replaying is idempotent.
  const QUEUE_KEY = "mp:tickQueue";

  function readQueue() {
    try {
      return JSON.parse(localStorage.getItem(QUEUE_KEY) || "{}") || {};
    } catch {
      return {};
    }
  }
  function writeQueue(q) {
    try {
      if (Object.keys(q).length === 0) localStorage.removeItem(QUEUE_KEY);
      else localStorage.setItem(QUEUE_KEY, JSON.stringify(q));
    } catch {
      /* storage full or blocked: the tick stays only on screen */
    }
    updateBadge();
  }

  function showTick(itemId, checked) {
    const li = document.getElementById("item-" + itemId);
    if (!li) return;
    const btn = $(".tick", li);
    li.classList.toggle("checked", checked);
    li.classList.add("pending");
    if (btn) {
      btn.dataset.checked = String(checked);
      btn.setAttribute("aria-pressed", String(checked));
      btn.setAttribute("hx-vals", JSON.stringify({ checked: String(!checked) }));
    }
    updateProgress();
  }

  // Progress counts only items in the sections (not Check at home).
  function updateProgress() {
    const p = $("#progress");
    if (!p) return;
    const done = $$("#list .sec .item.checked").length;
    const total = $$("#list .sec .item").length;
    const text = $(".progress-text", p);
    if (text) text.textContent = `${done} of ${total} in the basket`;
    const fill = $(".bar-fill", p);
    if (fill) fill.style.width = total ? `${Math.floor((done * 100) / total)}%` : "0%";
  }

  function queueTick(itemId, checked) {
    const q = readQueue();
    q[itemId] = checked;
    writeQueue(q);
    showTick(itemId, checked);
  }

  // Re-apply queued ticks after the list is re-rendered (SSE, cached page).
  function applyQueue() {
    const q = readQueue();
    for (const [id, checked] of Object.entries(q)) showTick(id, checked);
  }

  let replaying = false;
  async function replay() {
    if (replaying) return;
    const q = readQueue();
    const ids = Object.keys(q);
    if (ids.length === 0) return;
    replaying = true;
    try {
      for (const id of ids) {
        let res;
        try {
          res = await fetch(`/list/items/${encodeURIComponent(id)}/toggle`, {
            method: "POST",
            credentials: "same-origin",
            headers: { "Content-Type": "application/x-www-form-urlencoded", "HX-Request": "true" },
            body: new URLSearchParams({ checked: String(q[id]) }),
          });
        } catch {
          setOffline(true);
          return; // still offline; try again later
        }
        const now = readQueue();
        if (res.ok || (res.status >= 400 && res.status < 500 && res.status !== 401 && res.status !== 408 && res.status !== 429)) {
          // Done, or the item is gone (404 after a rebuild): drop it.
          if (now[id] === q[id]) delete now[id];
          writeQueue(now);
          const li = document.getElementById("item-" + id);
          if (li && res.ok) {
            const html = await res.text();
            li.outerHTML = html;
            const fresh = document.getElementById("item-" + id);
            if (fresh && window.htmx) htmx.process(fresh);
          }
        } else if (res.status === 401) {
          return; // logged out: keep the queue until the next login
        }
      }
      setOffline(false);
    } finally {
      replaying = false;
    }
  }

  // ---- offline badge -------------------------------------------------------
  let offline = !navigator.onLine;
  function setOffline(v) {
    offline = v;
    updateBadge();
  }
  function updateBadge() {
    const badge = $("#offline-badge");
    if (!badge) return;
    const n = Object.keys(readQueue()).length;
    const show = offline || n > 0;
    badge.hidden = !show;
    badge.textContent = offline
      ? n > 0 ? `Offline · ${n} to sync` : "Offline"
      : `Syncing ${n}…`;
  }

  window.addEventListener("online", () => {
    setOffline(false);
    replay();
  });
  window.addEventListener("offline", () => setOffline(true));
  setInterval(() => {
    if (Object.keys(readQueue()).length > 0) replay();
  }, 15000);

  document.addEventListener("htmx:sseOpen", () => {
    setOffline(false);
    replay();
  });
  document.addEventListener("htmx:sseError", () => {
    if (!navigator.onLine) setOffline(true);
  });

  // Tap while offline: queue instead of sending.
  document.addEventListener("htmx:beforeRequest", (e) => {
    const elt = e.detail.elt;
    if (elt.matches && elt.matches(".tick[data-item-id]") && (offline || !navigator.onLine)) {
      e.preventDefault();
      queueTick(elt.dataset.itemId, elt.dataset.checked !== "true");
    }
  });
  // The request failed at the network level: queue it.
  document.addEventListener("htmx:sendError", (e) => {
    const elt = e.detail.elt;
    if (elt.matches && elt.matches(".tick[data-item-id]")) {
      setOffline(true);
      queueTick(elt.dataset.itemId, elt.dataset.checked !== "true");
    } else {
      setOffline(true);
      showFlash("You're offline. That didn't go through.");
    }
  });

  document.addEventListener("htmx:afterSettle", (e) => {
    if (e.detail.target && e.detail.target.id === "flash") {
      e.detail.target.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
    if (e.detail.target && (e.detail.target.id === "list" || e.detail.target === document.body)) {
      applyQueue();
      updateBadge();
    }
  });

  // ---- flash ---------------------------------------------------------------
  function showFlash(msg) {
    const f = $("#flash");
    if (!f) return;
    f.innerHTML = "";
    const div = document.createElement("div");
    div.className = "flash error";
    const p = document.createElement("p");
    p.textContent = msg;
    div.append(p);
    f.append(div);
  }
  document.addEventListener("htmx:beforeSend", (e) => {
    // A new action clears the last error (SSE swaps are not requests).
    if (e.detail.elt && e.detail.elt.closest && !e.detail.elt.closest("#flash")) {
      const f = $("#flash");
      if (f) f.innerHTML = "";
    }
  });

  // ---- generate progress ---------------------------------------------------
  let genTimer = null;
  document.addEventListener("htmx:beforeRequest", (e) => {
    if (e.detail.elt.id !== "generate") return;
    const out = $("[data-elapsed]", e.detail.elt);
    const start = Date.now();
    if (out) out.textContent = "0";
    clearInterval(genTimer);
    genTimer = setInterval(() => {
      if (out) out.textContent = String(Math.floor((Date.now() - start) / 1000));
    }, 1000);
  });
  document.addEventListener("htmx:afterRequest", (e) => {
    if (e.detail.elt.id === "generate") clearInterval(genTimer);
  });

  // ---- clicks --------------------------------------------------------------
  document.addEventListener("click", async (e) => {
    const t = e.target.closest("button, a");
    if (!t) return;

    if (t.matches("[data-dismiss-flash]")) {
      const f = $("#flash");
      if (f) f.innerHTML = "";
      return;
    }
    if (t.matches("[data-copy]")) {
      const input = $("[data-copy-source]", t.closest(".copy-row"));
      if (!input) return;
      try {
        await navigator.clipboard.writeText(input.value);
      } catch {
        input.select();
        document.execCommand("copy");
      }
      t.textContent = "Copied ✓";
      setTimeout(() => (t.textContent = "Copy"), 2000);
      return;
    }
    if (t.matches("[data-timer-min]")) return toggleTimer(t);
    if (t.matches("[data-cook-open]")) return openCook();
    if (t.matches("[data-cook-close]")) return closeCook();
    if (t.matches("[data-cook-next]")) return stepCook(1);
    if (t.matches("[data-cook-prev]")) return stepCook(-1);
  });

  // ---- timers --------------------------------------------------------------
  const timers = new Map(); // button -> interval id

  function fmt(sec) {
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    return `${m}:${String(s).padStart(2, "0")}`;
  }
  function toggleTimer(btn) {
    const minutes = Number(btn.dataset.timerMin);
    if (timers.has(btn)) {
      clearInterval(timers.get(btn));
      timers.delete(btn);
      btn.classList.remove("running", "done");
      btn.textContent = `⏱ ${minutes} min timer`;
      return;
    }
    const end = Date.now() + minutes * 60000;
    btn.classList.add("running");
    btn.classList.remove("done");
    const tick = () => {
      const left = Math.max(0, Math.round((end - Date.now()) / 1000));
      btn.textContent = `⏱ ${fmt(left)} · tap to stop`;
      if (left === 0) {
        clearInterval(timers.get(btn));
        timers.set(btn, null);
        btn.classList.remove("running");
        btn.classList.add("done");
        btn.textContent = "⏰ Time's up! Tap to reset";
        alarm();
      }
    };
    tick();
    timers.set(btn, setInterval(tick, 1000));
  }
  function alarm() {
    if (navigator.vibrate) navigator.vibrate([300, 150, 300, 150, 300]);
    try {
      const ctx = new (window.AudioContext || window.webkitAudioContext)();
      [0, 0.4, 0.8].forEach((at) => {
        const o = ctx.createOscillator();
        const g = ctx.createGain();
        o.frequency.value = 880;
        g.gain.value = 0.2;
        o.connect(g).connect(ctx.destination);
        o.start(ctx.currentTime + at);
        o.stop(ctx.currentTime + at + 0.25);
      });
    } catch {
      /* no audio */
    }
  }
  function clearTimers() {
    for (const id of timers.values()) if (id) clearInterval(id);
    timers.clear();
  }

  // ---- cook mode + wake lock -----------------------------------------------
  let step = 0;
  let wakeLock = null;

  async function lockScreen() {
    const status = $("[data-wake-status]");
    if (!("wakeLock" in navigator)) {
      if (status) status.textContent = "Screen may turn off (not supported)";
      return;
    }
    try {
      wakeLock = await navigator.wakeLock.request("screen");
      if (status) status.textContent = "Screen stays on";
      wakeLock.addEventListener("release", () => {
        if (status && !$("#cook")?.hidden) status.textContent = "Screen may turn off";
      });
    } catch {
      if (status) status.textContent = "Screen may turn off";
    }
  }
  function unlockScreen() {
    if (wakeLock) wakeLock.release().catch(() => {});
    wakeLock = null;
  }
  // The lock is dropped when the tab is hidden; take it again on return.
  document.addEventListener("visibilitychange", () => {
    const cook = $("#cook");
    if (document.visibilityState === "visible" && cook && !cook.hidden) lockScreen();
  });

  function showStep() {
    const steps = $$("#cook .cook-step");
    steps.forEach((s, i) => (s.hidden = i !== step));
    const pos = $("[data-cook-pos]");
    if (pos) pos.textContent = `${step + 1} / ${steps.length}`;
    const prev = $("[data-cook-prev]");
    const next = $("[data-cook-next]");
    if (prev) prev.disabled = step === 0;
    if (next) next.textContent = step === steps.length - 1 ? "Done ✓" : "Next →";
  }
  function openCook() {
    const cook = $("#cook");
    if (!cook) return;
    step = 0;
    cook.hidden = false;
    document.body.classList.add("cooking");
    showStep();
    lockScreen();
  }
  function closeCook() {
    const cook = $("#cook");
    if (cook) cook.hidden = true;
    document.body.classList.remove("cooking");
    unlockScreen();
  }
  function stepCook(d) {
    const n = $$("#cook .cook-step").length;
    if (step + d >= n) return closeCook();
    step = Math.max(0, step + d);
    showStep();
  }
  document.addEventListener("keydown", (e) => {
    const cook = $("#cook");
    if (!cook || cook.hidden) return;
    if (e.key === "Escape") closeCook();
    if (e.key === "ArrowRight") stepCook(1);
    if (e.key === "ArrowLeft") stepCook(-1);
  });

  // Leaving the page (boosted navigation): drop timers and the wake lock,
  // and do not save an open cook overlay into htmx's history cache.
  document.addEventListener("htmx:beforeHistorySave", () => {
    closeCook();
    clearTimers();
  });

  // ---- setup steppers (− / + next to a number input) -------------------------
  document.addEventListener("click", (e) => {
    const btn = e.target.closest && e.target.closest("button[data-step][data-for]");
    if (!btn) return;
    const input = document.getElementById(btn.dataset.for);
    if (!input) return;
    const min = Number(input.min || 0), max = Number(input.max || 99);
    const next = Math.min(max, Math.max(min, (Number(input.value) || 0) + Number(btn.dataset.step)));
    input.value = String(next);
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });

  // ---- start ---------------------------------------------------------------
  function init() {
    applyQueue();
    updateBadge();
    if (navigator.onLine) replay();
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
