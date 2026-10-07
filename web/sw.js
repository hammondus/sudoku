// Service worker: network first, cache as fallback.
//
// Online, every request goes to the server and the response refreshes the
// cache. Offline, the cache answers, and any navigation gets the cached
// shell. The API is never cached: puzzles come from the buffer in
// IndexedDB, and sync must always see the server.
//
// The cache is named after the app version, which app.js passes in the
// registration URL (/sw.js?v=1.2.3). A release changes this script's URL,
// which makes the browser install it as a new worker with a new cache, and
// activate deletes the old one. One number to bump, in /VERSION.

const CACHE = "sudoku-" + (new URL(self.location.href).searchParams.get("v") || "dev");

// An unreachable host (a laptop asleep, a phone between networks) doesn't
// refuse the connection; the fetch hangs for the OS timeout, a minute or
// more on iOS. Give up sooner and use the cache.
const NETWORK_TIMEOUT_MS = 4000;

// Plain paths: the shell requests hashed URLs (?v=…), which match these
// entries because every lookup passes ignoreSearch.
const PRECACHE = [
  "/",
  "/css/style.css",
  "/js/app.js",
  "/js/version.js",
  "/js/game.js",
  "/js/db.js",
  "/js/sync.js",
  "/js/hint.js",
  "/manifest.webmanifest",
  "/icons/icon.svg",
  "/icons/icon-192.png",
  "/icons/icon-512.png",
  "/icons/apple-touch-icon.png",
];

self.addEventListener("install", (e) => {
  e.waitUntil(
    caches
      .open(CACHE)
      .then((c) => c.addAll(PRECACHE))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== location.origin || url.pathname.startsWith("/api/")) return;

  // Every navigation is the one-page shell.
  const key = req.mode === "navigate" ? "/" : req;

  e.respondWith(
    fetch(req, { signal: AbortSignal.timeout(NETWORK_TIMEOUT_MS) })
      .then((res) => {
        if (res.ok) {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(key, copy));
        }
        return res;
      })
      .catch(async () => {
        const hit = await caches.match(key, { ignoreSearch: true });
        return hit ?? (await caches.match("/", { ignoreSearch: true })) ?? Response.error();
      }),
  );
});
