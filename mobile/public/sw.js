// sw.js — offline app shell. The shell is cached at install; navigations
// fall back to the cached index when the server is unreachable. API and
// WebSocket traffic is never cached.
const VERSION = "noxa-shell-v1";
const SHELL = ["/app/", "/app/manifest.webmanifest", "/app/icons/icon-192.png", "/app/icons/icon-512.png"];

self.addEventListener("install", event => {
    event.waitUntil(caches.open(VERSION).then(cache => cache.addAll(SHELL)));
});

self.addEventListener("activate", event => {
    event.waitUntil(caches.keys().then(keys => Promise.all(keys.filter(k => k !== VERSION).map(k => caches.delete(k)))));
});

// The page asks before activating a new version so it never swaps code mid-call.
self.addEventListener("message", event => {
    if (event.data === "activate-update") self.skipWaiting();
});

self.addEventListener("fetch", event => {
    const request = event.request;
    const url = new URL(request.url);
    if (request.method !== "GET" || url.origin !== self.location.origin || !url.pathname.startsWith("/app/")) return;
    if (request.mode === "navigate") {
        event.respondWith(fetch(request).catch(() => caches.match("/app/")));
        return;
    }
    if (url.pathname.startsWith("/app/assets/")) {
        event.respondWith(caches.open(VERSION).then(async cache => {
            const hit = await cache.match(request);
            if (hit) return hit;
            const response = await fetch(request);
            if (response.ok) cache.put(request, response.clone());
            return response;
        }));
    }
});
