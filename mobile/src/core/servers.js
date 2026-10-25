// servers.js — the bookmark list of noXa servers this browser knows. Each
// server serves its own copy of the app at <origin>/app/ with its own
// storage, identity and installed icon, so the list only remembers where to
// go: [{origin, name}]. Switching is a full navigation to that origin.

const KEY = "noxa.servers";
const MAX = 32;

function storage() {
    try { return globalThis.localStorage || null; } catch { return null; }
}

/**
 * Turns what a user typed ("chat.example", "https://chat.example/app/",
 * "chat.example:8443") into an origin, or null. Plain http is accepted only
 * for loopback addresses (development servers).
 */
export function normalizeServer(input) {
    let text = String(input || "").trim();
    if (!text || /\s/.test(text)) return null;
    if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(text)) text = `https://${text}`;
    let url;
    try { url = new URL(text); } catch { return null; }
    if (url.username || url.password || !url.hostname) return null;
    const loopback = ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
    if (url.protocol !== "https:" && !(url.protocol === "http:" && loopback)) return null;
    return url.origin;
}

/** Saved servers, in the order they were added. */
export function listServers(store = storage()) {
    try {
        const list = JSON.parse(store?.getItem(KEY) || "[]");
        return Array.isArray(list) ? list.filter(s => s && typeof s.origin === "string" && normalizeServer(s.origin) === s.origin)
            .map(s => ({ origin: s.origin, name: typeof s.name === "string" ? s.name.slice(0, 256) : "" })) : [];
    } catch {
        return [];
    }
}

function save(list, store) {
    try { store?.setItem(KEY, JSON.stringify(list.slice(0, MAX))); } catch { /* private window or quota */ }
}

/** Adds or updates a server; the name is refreshed when known. Returns the list. */
export function addServer(origin, name = "", store = storage()) {
    const normalized = normalizeServer(origin);
    if (!normalized) throw new Error("invalid server address");
    const list = listServers(store);
    const existing = list.find(s => s.origin === normalized);
    if (existing) {
        if (name) existing.name = name;
    } else {
        list.push({ origin: normalized, name });
    }
    save(list, store);
    return list;
}

export function removeServer(origin, store = storage()) {
    const list = listServers(store).filter(s => s.origin !== origin);
    save(list, store);
    return list;
}

/**
 * Asks a server for its public info. Browsers allow this only for the
 * page's own origin (the app's Content-Security-Policy limits connections to
 * 'self'), so other servers are saved by address and show their real name
 * once they are opened.
 */
export async function fetchServerInfo(origin, { fetchImpl = globalThis.fetch, self = globalThis.location?.origin } = {}) {
    if (origin !== self) return null;
    const response = await fetchImpl(`${origin}/api/v1/web/info`, { cache: "no-store" });
    if (!response.ok) throw new Error(`server answered ${response.status}`);
    return response.json();
}

/** The app's address on a server. */
export function appURL(origin) {
    return `${origin}/app/`;
}
