// Small vanilla JS only (package C): service worker registration now, the
// offline tick queue later.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch((err) => console.warn("sw", err));
}
