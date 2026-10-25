// sw.js — offline app shell. The shell is cached at install; navigations
// fall back to the cached index when the server is unreachable. API and
// WebSocket traffic is never cached.
import { ChatSession } from "./core/chat-session.js";
import { Connection } from "./core/connection.js";
import { signIn } from "./core/device.js";
import { DirectMessages } from "./core/direct.js";
import { loadIdentity } from "./core/identity-store.js";
import { Groups } from "./core/groups.js";
import { MUTED_CACHE, MUTED_URL } from "./core/conversation-prefs.js";
import { SYNC_TAG, enqueue, flush, pending } from "./core/outbox.js";
import { Msg } from "./core/frame.js";
import { announceRead } from "./core/push.js";
import { putShare, shareText } from "./core/share-store.js";

const VERSION = "noxa-shell-v3";
const SHELL = ["/app/", "/app/manifest.webmanifest", "/app/icons/icon-192.png", "/app/icons/icon-512.png"];

self.addEventListener("install", event => {
    event.waitUntil(caches.open(VERSION).then(cache => cache.addAll(SHELL)));
});

self.addEventListener("activate", event => {
    event.waitUntil(caches.keys().then(keys => Promise.all(keys.filter(k => k !== VERSION && k !== MUTED_CACHE).map(k => caches.delete(k)))));
});

// The page asks before activating a new version so it never swaps code mid-call.
// It also sends the muted conversations, kept for the next push.
self.addEventListener("message", event => {
    if (event.data === "activate-update") self.skipWaiting();
    if (event.data?.type === "muted" && Array.isArray(event.data.tags)) {
        const tags = event.data.tags.filter(tag => typeof tag === "string").slice(0, 500);
        event.waitUntil(caches.open(MUTED_CACHE).then(cache => cache.put(MUTED_URL, new Response(JSON.stringify(tags), { headers: { "content-type": "application/json" } }))));
    }
});

async function mutedTags() {
    try {
        const response = await (await caches.open(MUTED_CACHE)).match(MUTED_URL);
        const tags = response ? await response.json() : [];
        return Array.isArray(tags) ? tags : [];
    } catch {
        return [];
    }
}

// Web Share Target (manifest share_target): Android posts the shared text
// and files here. They are kept in IndexedDB and the app opens #/share to
// pick a conversation. The server answers the same POST with a redirect too,
// for the case that this worker is not installed yet (then nothing is kept).
async function receiveShare(request) {
    try {
        const form = await request.formData();
        const files = form.getAll("files").filter(file => typeof file === "object" && file.size > 0);
        await putShare({ text: shareText({ title: form.get("title"), text: form.get("text"), url: form.get("url") }), files });
    } catch {
        // The share screen reports that nothing arrived.
    }
    return Response.redirect("/app/#/share", 303);
}

self.addEventListener("fetch", event => {
    const request = event.request;
    const url = new URL(request.url);
    if (request.method === "POST" && url.origin === self.location.origin && url.pathname === "/app/share") {
        event.respondWith(receiveShare(request));
        return;
    }
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

// --- Notifications ------------------------------------------------------------
// Pushes carry a kind, a conversation tag and, unless the phone hides
// details, the sender and channel names. Message text never arrives here.
const WORDS = {
    en: {
        app: "noXa", message: "New message", dm: "sent you a message", group: "wrote in a group",
        mention: "mentioned you", mentionIn: "mentioned you in #{place}", poke: "poked you",
        call: "Incoming call", video: "Incoming video call", answer: "Answer", decline: "Decline",
        reply: "Reply", replyPlaceholder: "Write a reply", markRead: "Mark as read",
    },
    de: {
        app: "noXa", message: "Neue Nachricht", dm: "hat dir geschrieben", group: "hat in einer Gruppe geschrieben",
        mention: "hat dich erwähnt", mentionIn: "hat dich in #{place} erwähnt", poke: "hat dich angestupst",
        call: "Eingehender Anruf", video: "Eingehender Videoanruf", answer: "Annehmen", decline: "Ablehnen",
        reply: "Antworten", replyPlaceholder: "Antwort schreiben", markRead: "Als gelesen markieren",
    },
};

function words() {
    return WORDS[(self.navigator.language || "en").slice(0, 2)] || WORDS.en;
}

/** The in-app route a notification opens. */
function routeFor(tag) {
    const [scope, id = ""] = String(tag).split(/:(.*)/s);
    switch (scope) {
        case "channel": return `/app/#/chat/${encodeURIComponent(id)}`;
        case "dm": return `/app/#/dm/${encodeURIComponent(id)}`;
        case "conversation": return `/app/#/group/${encodeURIComponent(id)}`;
        case "call": return `/app/#/call/${encodeURIComponent(id)}`;
        default: return "/app/#/home";
    }
}

function describe(push) {
    const w = words();
    const from = push.from || "";
    if (push.kind === "call") {
        const body = push.video ? w.video : w.call;
        return { title: from || w.app, body: from ? body : "", call: true };
    }
    if (!from) return { title: w.app, body: w.message };
    switch (push.kind) {
        case "dm": return { title: from, body: w.dm };
        case "conversation": return { title: from, body: w.group };
        case "mention": return { title: from, body: push.place ? w.mentionIn.replace("{place}", push.place) : w.mention };
        case "poke": return { title: from, body: w.poke };
        default: return { title: w.app, body: w.message };
    }
}

async function updateBadge() {
    const shown = await self.registration.getNotifications();
    if (shown.length) await self.navigator.setAppBadge?.(shown.length).catch(() => {});
    else await self.navigator.clearAppBadge?.().catch(() => {});
}

async function closeTag(tag) {
    for (const notification of await self.registration.getNotifications({ tag })) notification.close();
}

async function onPush(push) {
    if (push.kind === "clear") {
        await closeTag(push.tag);
        await updateBadge();
        return;
    }
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const foreground = push.kind !== "call" && windows.some(client => client.visibilityState === "visible");
    const muted = push.kind !== "call" && (await mutedTags()).includes(push.tag);
    // The server filters foreground/muted deliveries, but state can change
    // while a push is in flight. Apple revokes subscriptions for an event
    // without a visible notification, even when the app is already open.
    const ua = self.navigator.userAgent || "";
    const apple = /iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && /Safari/.test(ua) && !/(?:Chrome|Chromium|Edg|OPR)\//.test(ua));
    if (!apple && (foreground || (muted && windows.length))) return;
    const quiet = muted || foreground;
    const text = describe(push);
    await self.registration.showNotification(text.title, {
        body: text.body,
        tag: push.tag,
        renotify: !quiet,
        silent: quiet,
        icon: "/app/icons/icon-192.png",
        badge: "/app/icons/icon-192.png",
        timestamp: (push.at || Date.now() / 1000) * 1000,
        requireInteraction: !!text.call,
        // A silent notification must omit vibrate, including an empty array.
        ...(quiet ? {} : { vibrate: text.call ? [400, 200, 400, 200, 400] : [120] }),
        // iOS shows no actions; Android gets Answer and Decline on calls, Reply and Mark as read on messages.
        actions: text.call ? [{ action: "answer", title: words().answer }, { action: "decline", title: words().decline }] : messageActions(push),
        data: { url: routeFor(push.tag), tag: push.tag },
    });
    await updateBadge();
}

self.addEventListener("push", event => {
    let push;
    try {
        push = event.data?.json();
    } catch {
        push = null;
    }
    if (!push || typeof push.kind !== "string") push = { kind: "message", tag: "noxa" };
    event.waitUntil(onPush(push));
});

/** The call ID of a "call:<id>" notification tag. */
function callID(tag) {
    const match = /^call:(.+)$/s.exec(String(tag || ""));
    return match ? match[1] : "";
}

/**
 * Android shows "Reply" (inline text where the browser supports it) and
 * "Mark as read" on message notifications. iOS shows no actions.
 */
function messageActions(push) {
    if (/iPhone|iPad|iPod|Macintosh/.test(self.navigator.userAgent || "")) return [];
    const [scope] = String(push.tag || "").split(":");
    const w = words();
    const actions = [];
    if (scope === "dm" || scope === "channel" || scope === "conversation") actions.push({ action: "reply", title: w.reply, type: "text", placeholder: w.replyPlaceholder });
    if (scope === "dm" || scope === "channel" || scope === "conversation") actions.push({ action: "read", title: w.markRead });
    return actions;
}

/** The outbox target of a conversation tag, or null when replies go elsewhere. */
function replyTarget(tag) {
    const [scope, id = ""] = String(tag).split(/:(.*)/s);
    if (scope === "dm" && id) return { kind: "dm", peer: id, nickname: "" };
    if (scope === "channel" && /^\d+$/.test(id)) return { kind: "channel", scope: Number(id) };
    if (scope === "conversation" && id) return { kind: "conversation", conversation: id };
    return null;
}

/**
 * Reply or mark read from the notification: an open app does it itself;
 * otherwise the worker signs in briefly like the outbox flush.
 */
async function notificationAction(action, tag, reply) {
    const [scope, id = ""] = String(tag).split(/:(.*)/s);
    const target = action === "reply" ? replyTarget(tag) : null;
    if (target && reply) await enqueue(target, reply.slice(0, 4000));
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    if (windows.length) {
        for (const client of windows) client.postMessage({ type: "notification-action", action, tag, flush: !!(target && reply) });
    } else {
        await signedIn(async session => {
            announceRead(session.conn, scope, id);
            return !!(target && reply);
        });
    }
    await closeTag(tag);
    await updateBadge();
}

self.addEventListener("notificationclick", event => {
    event.notification.close();
    const tag = event.notification.data?.tag || "";
    const typed = typeof event.reply === "string" ? event.reply.trim() : "";
    // A reply without inline text (unsupported browser) opens the conversation.
    if (event.action === "read" || (event.action === "reply" && typed)) {
        event.waitUntil(notificationAction(event.action, tag, typed).catch(() => {}));
        return;
    }
    const url = event.notification.data?.url || "/app/#/home";
    const call = callID(event.notification.data?.tag);
    event.waitUntil((async () => {
        const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
        const open = windows.find(client => new URL(client.url).pathname.startsWith("/app/"));
        if (call && event.action === "decline") {
            // Declining never opens the app.
            if (open) open.postMessage({ type: "call-action", action: "decline", id: call });
            else await declineCall(call).catch(() => {});
        } else if (open) {
            open.postMessage({ type: "open", url });
            if (call && event.action === "answer") open.postMessage({ type: "call-action", action: "answer", id: call });
            await open.focus();
        } else {
            // A fresh window accepts once the call rings on its session.
            await self.clients.openWindow(call && event.action === "answer" ? `/app/?answer=${encodeURIComponent(call)}${new URL(url, self.location.origin).hash}` : url);
        }
        await updateBadge();
    })());
});

/** Declines a call through a short-lived session, like the outbox flush. */
async function declineCall(id) {
    const stored = await loadIdentity();
    if (!stored?.meta?.linked) return;
    const info = await (await fetch("/api/v1/web/info", { cache: "no-store" })).json();
    const conn = await Connection.open();
    try {
        await signIn(conn, stored.identity, { version: info.version || "0.0.0" });
        await conn.request(Msg.CallRequest, { action: "decline", id }, Msg.CallResult, { accept: r => r.action === "decline" });
    } finally {
        conn.close();
    }
}

self.addEventListener("notificationclose", event => {
    event.waitUntil(updateBadge());
});

// The app re-registers the new subscription with the server when it next
// connects; here we only make sure a subscription exists.
self.addEventListener("pushsubscriptionchange", event => {
    const options = event.oldSubscription?.options;
    if (!options?.applicationServerKey) return;
    event.waitUntil(self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: options.applicationServerKey }).catch(() => {}));
});


// --- Background Sync: send the outbox while the app is closed ---------------
// Android fires this when connectivity returns. An open app flushes itself;
// otherwise the worker signs in with the device key, sends the waiting
// messages and disconnects. A failure makes the browser retry later.

async function waitForKeys(session, scopes, timeout = 8000) {
    const missing = () => scopes.filter(scope => !session.current.has(scope));
    if (!missing().length) return;
    await new Promise(resolve => {
        const done = () => { if (!missing().length) { session.removeEventListener("keys", done); clearTimeout(timer); resolve(); } };
        const timer = setTimeout(() => { session.removeEventListener("keys", done); resolve(); }, timeout);
        session.addEventListener("keys", done);
    });
}

/**
 * Signs in with the device key for a moment. work(session) runs first and
 * returns whether the outbox should be sent in the same connection.
 */
async function signedIn(work = async () => true) {
    const stored = await loadIdentity();
    if (!stored?.meta?.linked) return;
    const info = await (await fetch("/api/v1/web/info", { cache: "no-store" })).json();
    const conn = await Connection.open();
    try {
        const auth = await signIn(conn, stored.identity, { version: info.version || "0.0.0" });
        const session = new ChatSession(conn, stored.identity, auth);
        // Offline DMs are replayed immediately after authentication.
        const dm = new DirectMessages(session, stored.meta);
        const groups = new Groups(session, dm);
        try {
            if (!await work(session)) return;
            const waiting = (await pending()).filter(entry => !entry.expired && !entry.failed);
            const scopes = [...new Set(waiting.filter(entry => entry.kind === "channel").map(entry => entry.scope))];
            const channels = scopes.filter(scope => scope > 0);
            if (channels.length) await session.subscribe(channels).catch(() => {});
            await waitForKeys(session, scopes);
            await dm.ready;
            await flush(session, dm, groups);
        } finally {
            groups.close();
            dm.close();
        }
    } finally {
        // The read marker and sends are written before the socket closes.
        await new Promise(resolve => setTimeout(resolve, 150));
        conn.close();
    }
}

async function syncOutbox() {
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    if (windows.length) {
        for (const client of windows) client.postMessage({ type: "flush-outbox" });
        return;
    }
    const waiting = (await pending()).filter(entry => !entry.expired && !entry.failed);
    if (!waiting.length) return;
    await signedIn();
    const left = (await pending()).filter(entry => !entry.expired && !entry.failed);
    if (left.length) throw new Error(`${left.length} messages still waiting`);
}

self.addEventListener("sync", event => {
    if (event.tag === SYNC_TAG) event.waitUntil(syncOutbox());
});
