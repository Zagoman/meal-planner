// Service worker (package C).
//
// - Static assets: cache first, refreshed in the background.
// - Pages (GET navigations and boosted htmx page loads): network first; the
//   last good copy is kept so the shopping list opens in the supermarket
//   with no signal. Ticks made offline are queued by app.js, not here.
// - Everything else (POSTs, the SSE stream, /api) goes straight to the network.
// - Visiting /login (after logout or an expired session) drops cached pages,
//   so one member's household data does not linger on a shared phone.
const VERSION = "v4";
const ASSETS = `mp-assets-${VERSION}`;
const PAGES = `mp-pages-${VERSION}`;
const PRECACHE = [
  "/static/hvadskalviha.css",
  "/static/app.css",
  "/static/app.js",
  "/static/htmx.min.js",
  "/static/htmx-ext-sse.js",
  "/static/manifest.webmanifest",
  "/static/icon.svg",
  "/static/favicon.svg",
  "/static/fonts/instrument-sans-var.woff2",
  "/static/fonts/instrument-serif-400.woff2",
  "/static/fonts/instrument-serif-400-italic.woff2",
];
const CACHED_PAGES = [/^\/$/, /^\/list$/, /^\/week$/, /^\/meals\/[0-9a-f-]+$/];

self.addEventListener("install", (e) => {
  e.waitUntil(
    caches.open(ASSETS).then((c) => c.addAll(PRECACHE)).then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== ASSETS && k !== PAGES).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  if (url.pathname === "/login") {
    e.waitUntil(caches.delete(PAGES));
    return;
  }
  if (url.pathname.startsWith("/static/")) {
    e.respondWith(assetFirst(req, e));
    return;
  }
  // htmx partial requests (not boosted page loads) are never cached.
  const partial = req.headers.get("HX-Request") === "true" && req.headers.get("HX-Boosted") !== "true";
  if (!partial && CACHED_PAGES.some((re) => re.test(url.pathname))) {
    e.respondWith(pageNetworkFirst(req, url));
  }
});

async function assetFirst(req, e) {
  const cache = await caches.open(ASSETS);
  const hit = await cache.match(req, { ignoreSearch: true });
  const fresh = fetch(req)
    .then((res) => {
      if (res.ok) cache.put(req, res.clone());
      return res;
    })
    .catch(() => undefined);
  if (hit) {
    e.waitUntil(fresh);
    return hit;
  }
  return (await fresh) || new Response("offline", { status: 503 });
}

async function pageNetworkFirst(req, url) {
  const cache = await caches.open(PAGES);
  // One cache entry per path: a boosted load and a full load are the same page.
  const key = new Request(url.origin + url.pathname);
  try {
    const res = await fetch(req);
    if (res.ok && !res.redirected) cache.put(key, res.clone());
    return res;
  } catch {
    const hit = await cache.match(key);
    if (hit) return hit;
    return new Response(
      '<!doctype html><meta name="viewport" content="width=device-width"><title>Offline</title>' +
        '<p style="font-family:system-ui;padding:1rem">You are offline and this page has not been saved yet. ' +
        '<a href="/list">Open the shopping list</a></p>',
      { status: 503, headers: { "Content-Type": "text/html; charset=utf-8" } },
    );
  }
}
