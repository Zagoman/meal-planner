// Hvad skal vi ha'? client. Small vanilla JS only (package C):
//   - service worker registration
//   - offline badge
//   - offline tick queue (IndexedDB), sent in order when back online
//   - plan-generation progress counter
//   - copy buttons, flash dismiss
//   - cook mode pages: timers, Screen Wake Lock
//   - recipe form: add ingredient / step rows
// Every handler is delegated from document, so it survives hx-boost swaps.
(() => {
  "use strict";

  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/sw.js").catch((err) => console.warn("sw", err));
  }

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  // ---- offline tick queue --------------------------------------------------
  // Ticks made offline are stored in IndexedDB as { itemId, checked, at } in
  // the order they were made and sent in that order when back online. Each
  // carries the time it was made; the server applies a tick only if no later
  // one of the item was applied, so the last tick wins across phones.
  const DB_NAME = "mp";
  const STORE = "ticks";
  const OLD_KEY = "mp:tickQueue"; // localStorage queue of earlier versions
  let queue = []; // in-memory mirror, in order: { key, itemId, checked, at }

  function openDB() {
    return new Promise((resolve, reject) => {
      if (!("indexedDB" in window)) return reject(new Error("no IndexedDB"));
      const req = indexedDB.open(DB_NAME, 1);
      req.onupgradeneeded = () => req.result.createObjectStore(STORE, { keyPath: "key", autoIncrement: true });
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  }
  async function dbRun(mode, fn) {
    const db = await openDB();
    return new Promise((resolve, reject) => {
      const tx = db.transaction(STORE, mode);
      const out = fn(tx.objectStore(STORE));
      tx.oncomplete = () => resolve(out && "result" in out ? out.result : undefined);
      tx.onerror = () => reject(tx.error);
    });
  }
  async function loadQueue() {
    try {
      queue = (await dbRun("readonly", (st) => st.getAll())) || [];
    } catch {
      queue = queue || [];
    }
    // Move a queue left by an earlier version.
    try {
      const old = JSON.parse(localStorage.getItem(OLD_KEY) || "{}") || {};
      localStorage.removeItem(OLD_KEY);
      for (const [id, checked] of Object.entries(old)) await addTick(id, checked, Date.now());
    } catch {
      /* nothing to move */
    }
  }
  async function addTick(itemId, checked, at) {
    const entry = { itemId, checked, at };
    queue.push(entry);
    try {
      entry.key = await dbRun("readwrite", (st) => st.add({ itemId, checked, at }));
    } catch {
      /* no IndexedDB: the tick stays in memory until the page closes */
    }
    updateBadge();
  }
  async function dropTick(entry) {
    queue = queue.filter((e) => e !== entry);
    if (entry.key !== undefined) {
      try {
        await dbRun("readwrite", (st) => st.delete(entry.key));
      } catch {
        /* already gone */
      }
    }
    updateBadge();
  }
  function pendingItems() {
    return new Set(queue.map((e) => e.itemId)).size;
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
    if (text) {
      const all = total > 0 && done === total;
      text.textContent = all ? "All ticked. Go home, nobody's checking." : `${done} of ${total} in the basket`;
      text.classList.toggle("list-done", all);
    }
    const fill = $(".bar-fill", p);
    if (fill) fill.style.width = total ? `${Math.floor((done * 100) / total)}%` : "0%";
  }

  function queueTick(itemId, checked) {
    showTick(itemId, checked);
    addTick(itemId, checked, Date.now());
  }

  // Re-apply queued ticks after the list is re-rendered (SSE, cached page):
  // the latest queued tick of each item.
  function applyQueue() {
    const latest = new Map();
    for (const e of queue) latest.set(e.itemId, e.checked);
    for (const [id, checked] of latest) showTick(id, checked);
  }

  let replaying = false;
  async function replay() {
    if (replaying || queue.length === 0) return;
    replaying = true;
    try {
      for (const entry of [...queue]) {
        let res;
        try {
          res = await fetch(`/list/items/${encodeURIComponent(entry.itemId)}/toggle`, {
            method: "POST",
            credentials: "same-origin",
            headers: { "Content-Type": "application/x-www-form-urlencoded", "HX-Request": "true" },
            body: new URLSearchParams({ checked: String(entry.checked), at: new Date(entry.at).toISOString() }),
          });
        } catch {
          setOffline(true);
          return; // still offline; try again later
        }
        if (res.status === 401) return; // logged out: keep the queue until the next login
        if (res.ok || (res.status >= 400 && res.status < 500 && res.status !== 408 && res.status !== 429)) {
          // Done, or the item is gone (404 after a rebuild): drop it.
          await dropTick(entry);
          const isLast = !queue.some((e) => e.itemId === entry.itemId);
          if (res.ok && isLast) swapByID(await res.text());
        } else {
          return; // server trouble: try again later
        }
      }
      setOffline(false);
    } finally {
      replaying = false;
    }
  }

  // swapByID puts each top-level element of an HTML answer in place of the
  // element with the same id (the tick answer: the re-sorted section and
  // the progress), as htmx would for the target and out-of-band parts.
  function swapByID(html) {
    const t = document.createElement("template");
    t.innerHTML = html;
    for (const el of [...t.content.children]) {
      el.removeAttribute("hx-swap-oob");
      const old = el.id && document.getElementById(el.id);
      if (!old) continue;
      old.replaceWith(el);
      if (window.htmx) htmx.process(el);
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
    const n = pendingItems();
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
    if (queue.length > 0) replay();
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
  // ---- list: focus and announce after a tick (#1) ------------------------------
  // The tick answer replaces the whole section (rows move), and the live
  // update replaces the whole list right after, so for a few seconds focus is
  // put back on the same item (or its folded section) after every swap; its
  // new state is said once in #announce.
  let refocus = null; // { id, until }
  document.addEventListener("htmx:beforeRequest", (e) => {
    const elt = e.detail.elt;
    if (elt && elt.matches && elt.matches(".tick[data-item-id]")) {
      const checked = elt.dataset.checked !== "true";
      refocus = { id: elt.dataset.itemId, until: Date.now() + 3000 };
      if (elt.dataset.name) announce(checked ? `${elt.dataset.name} in basket` : `${elt.dataset.name} back on the list`);
    }
  });
  function restoreFocus() {
    if (!refocus || Date.now() > refocus.until) return;
    const a = document.activeElement;
    if (a && a !== document.body && a.isConnected && !(a.matches && a.matches(".tick"))) return; // they moved on
    const btn = document.querySelector(`.tick[data-item-id="${refocus.id}"]`);
    if (!btn) return;
    const closed = btn.closest("details:not([open])");
    (closed ? closed.querySelector("summary") : btn).focus({ preventScroll: true });
  }
  document.addEventListener("htmx:afterSettle", restoreFocus);
  document.addEventListener("htmx:sseMessage", () => setTimeout(restoreFocus, 0));

  // ---- list: the "+" in the corner once the add form is out of view (#4) ------
  function watchAddForm() {
    const form = $("#add-item");
    const jump = $("#add-jump");
    if (!form || !jump || !("IntersectionObserver" in window) || jump.dataset.watching) return;
    jump.dataset.watching = "1";
    new IntersectionObserver(([en]) => { jump.hidden = en.isIntersecting; }).observe(form);
    jump.addEventListener("click", () => {
      const input = $("#add-name");
      form.scrollIntoView({ block: "center", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
      if (input) input.focus({ preventScroll: true });
    });
  }
  document.addEventListener("DOMContentLoaded", watchAddForm);
  document.addEventListener("htmx:afterSettle", watchAddForm);

  // ---- toasts that close themselves (Clear ticked · Undo) --------------------------
  document.addEventListener("htmx:afterSettle", () => {
    for (const t of document.querySelectorAll("[data-autoclose]:not([data-closing])")) {
      t.dataset.closing = "1";
      setTimeout(() => t.remove(), Number(t.dataset.autoclose) || 8000);
    }
  });

  // ---- announcements ---------------------------------------------------------
  // Content that changes under the reader (a new plan arriving over SSE) is
  // announced in #announce; a boosted page change moves focus to its title.
  function announce(msg) {
    const el = $("#announce");
    if (!el) return;
    el.textContent = "";
    setTimeout(() => (el.textContent = msg), 50);
  }
  const cardStates = { ready: "Next week's plan is ready.", failed: "We couldn't make a plan just now." };
  let cardState = null; // last state seen, so a refresh in the same state stays quiet
  const seeCard = () => {
    const card = $("#draft-card");
    const st = card ? card.dataset.state : null;
    const changed = cardState !== null && st !== cardState;
    cardState = st;
    return changed ? cardStates[st] : null;
  };
  document.addEventListener("DOMContentLoaded", seeCard);
  document.addEventListener("htmx:afterSettle", (e) => {
    const boosted = !!(e.detail.boosted || (e.detail.requestConfig && e.detail.requestConfig.boosted));
    if (e.detail.target && e.detail.target.id === "draft-card") {
      const msg = seeCard();
      if (msg) announce(msg);
    } else if (boosted) {
      seeCard(); // a new page: start from its card, say nothing
    }
    if (boosted) {
      const t = $("#page-title");
      if (t) t.focus({ preventScroll: true });
    }
  });

  // The server couldn't save a tick (5xx, timeout, rate limit): keep it in
  // the queue to try again and say so under the row, not in the flash.
  document.addEventListener("htmx:beforeSwap", (e) => {
    const elt = e.detail.elt;
    const st = e.detail.xhr ? e.detail.xhr.status : 0;
    if (!(elt && elt.matches && elt.matches(".tick[data-item-id]"))) return;
    if (st >= 500 || st === 408 || st === 429) {
      e.detail.shouldSwap = false;
      e.detail.isError = false;
      queueTick(elt.dataset.itemId, elt.dataset.checked !== "true");
      saveFailed(elt.dataset.itemId);
    }
  });
  function saveFailed(itemId) {
    const li = document.getElementById("item-" + itemId);
    if (!li || $(".save-failed", li)) return;
    const p = document.createElement("p");
    p.className = "save-failed";
    p.setAttribute("role", "status");
    p.textContent = "That tick didn't save. We'll try again.";
    li.appendChild(p);
  }

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
    if (t.matches("[data-add-row]")) return addRow(t.dataset.addRow);
  });

  // ---- timers --------------------------------------------------------------
  const timers = new Map(); // button -> interval id

  function fmt(sec) {
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    return `${m}:${String(s).padStart(2, "0")}`;
  }
  // A timer button counts down in its own label, or in the
  // [data-timer-display] next to it (cook mode).
  function toggleTimer(btn) {
    const minutes = Number(btn.dataset.timerMin);
    const display = btn.parentElement && btn.parentElement.querySelector("[data-timer-display]");
    const idle = display ? "Start timer" : `⏱ ${minutes} min timer`;
    if (timers.has(btn)) {
      clearInterval(timers.get(btn));
      timers.delete(btn);
      btn.classList.remove("running", "done");
      btn.textContent = idle;
      if (display) display.textContent = fmt(minutes * 60);
      return;
    }
    const end = Date.now() + minutes * 60000;
    btn.classList.add("running");
    btn.classList.remove("done");
    const tick = () => {
      const left = Math.max(0, Math.round((end - Date.now()) / 1000));
      if (display) {
        display.textContent = fmt(left);
        btn.textContent = "Stop";
      } else {
        btn.textContent = `⏱ ${fmt(left)} · tap to stop`;
      }
      if (left === 0) {
        clearInterval(timers.get(btn));
        timers.set(btn, null);
        btn.classList.remove("running");
        btn.classList.add("done");
        btn.textContent = display ? "Reset" : "⏰ Time's up! Tap to reset";
        if (display) display.textContent = "Time's up!";
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
  // Cook mode pages ([data-cook]) keep the screen on with the Screen Wake
  // Lock where the browser has it; without it, nothing breaks.
  let wakeLock = null;

  async function lockScreen() {
    const status = $("[data-wake-status]");
    if (!("wakeLock" in navigator)) {
      if (status) status.textContent = "This phone may turn the screen off while cooking";
      return;
    }
    if (wakeLock) return;
    try {
      wakeLock = await navigator.wakeLock.request("screen");
      if (status) status.textContent = "Screen stays on while cooking";
      wakeLock.addEventListener("release", () => {
        wakeLock = null;
      });
    } catch {
      if (status) status.textContent = "This phone may turn the screen off while cooking";
    }
  }
  function unlockScreen() {
    if (wakeLock) wakeLock.release().catch(() => {});
    wakeLock = null;
  }
  function syncCook() {
    if ($("[data-cook]")) lockScreen();
    else unlockScreen();
  }
  // The lock is dropped when the tab is hidden; take it again on return.
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") syncCook();
  });
  document.addEventListener("htmx:afterSettle", syncCook);
  document.addEventListener("keydown", (e) => {
    if (!$("[data-cook]")) return;
    const link = e.key === "ArrowRight" ? $(".cook-nav a.primary") : e.key === "ArrowLeft" ? $(".cook-nav a.secondary-btn") : null;
    if (link) link.click();
  });
  // Leaving a page: drop timers.
  document.addEventListener("htmx:beforeHistorySave", () => {
    clearTimers();
  });

  // ---- recipe form: one more ingredient or step row --------------------------
  function addRow(containerId) {
    const box = document.getElementById(containerId);
    const rows = box ? box.querySelectorAll("[data-row]") : [];
    if (!rows.length) return;
    const n = rows.length;
    const row = rows[rows.length - 1].cloneNode(true);
    row.querySelectorAll("input, textarea, select").forEach((el) => {
      if (el.id) el.id = el.id.replace(/-\d+$/, "-" + n);
      el.classList.remove("flag");
      if (el.type === "radio") {
        el.checked = false;
        el.value = String(n);
      } else if (el.tagName !== "SELECT") {
        el.value = "";
      }
      if (el.placeholder && /^Step \d+$/.test(el.placeholder)) el.placeholder = "Step " + (n + 1);
    });
    row.querySelectorAll("label[for]").forEach((l) => (l.htmlFor = l.htmlFor.replace(/-\d+$/, "-" + n)));
    box.append(row);
    const first = row.querySelector("input, textarea");
    if (first) first.focus();
  }

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
  async function init() {
    await loadQueue();
    applyQueue();
    updateBadge();
    syncCook();
    if (navigator.onLine) replay();
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
