// The service worker's share target, push notifications and notification
// actions, run in Node against a fake worker scope. Real browsers only show
// these on a device; this pins the decisions the worker makes.
import test from "node:test";
import assert from "node:assert/strict";

import { installFakeIndexedDB } from "./fake-idb.mjs";

installFakeIndexedDB();

// --- A fake worker scope --------------------------------------------------------
const listeners = new Map();
const notifications = [];
const shown = [];
const windows = [];
const opened = [];
const cacheStore = new Map();
// Like the real Cache, match() hands out a fresh copy so bodies can be read again.
const fakeCache = { match: async key => cacheStore.get(key)?.clone(), put: async (key, response) => cacheStore.set(key, response), addAll: async () => {} };
globalThis.caches = { open: async () => fakeCache, keys: async () => [], delete: async () => true, match: async () => undefined };
globalThis.self = globalThis;
globalThis.location = new URL("https://chat.example/app/sw.js");
const fakeNavigator = { language: "en", userAgent: "Mozilla/5.0 (Linux; Android 14) Chrome/120 Mobile", setAppBadge: async () => {}, clearAppBadge: async () => {} };
Object.defineProperty(globalThis, "navigator", { value: fakeNavigator, configurable: true });
globalThis.registration = {
    showNotification: async (title, options) => { shown.push({ title, ...options }); notifications.push({ tag: options.tag, close() { this.closed = true; } }); },
    getNotifications: async ({ tag } = {}) => notifications.filter(n => !n.closed && (!tag || n.tag === tag)),
    pushManager: { subscribe: async () => {} },
};
globalThis.clients = { matchAll: async () => windows, openWindow: async url => { opened.push(url); } };
globalThis.skipWaiting = () => {};
globalThis.fetch = async () => { throw new Error("offline"); };
// Browsers resolve a relative redirect against the worker; Node demands an absolute URL.
Response.redirect = (url, status) => new Response(null, { status, headers: { location: url } });
globalThis.addEventListener = (type, listener) => listeners.set(type, listener);

await import("../src/sw.js");
const { pending, remove } = await import("../src/core/outbox.js");
const { getShare, clearShare, MAX_SHARED_BYTES } = await import("../src/core/share-store.js");
const { beginIdentity, saveIdentity } = await import("../src/core/identity-store.js");
const { generateIdentity } = await import("../src/core/crypto.js");
// Pushes are shown only for the linked account this browser holds.
await saveIdentity(generateIdentity(), { uniqueID: "me", deviceID: "phone", linked: true });

async function dispatch(type, event) {
    const waits = [];
    const responses = [];
    listeners.get(type)({ ...event, waitUntil: p => waits.push(p), respondWith: p => responses.push(p) });
    await Promise.all(waits);
    return Promise.all(responses);
}

const push = (payload) => dispatch("push", { data: { json: () => payload } });

test("a Web Share Target post is kept for the share screen and redirected", async () => {
    await clearShare();
    const form = new FormData();
    form.set("title", "Look");
    form.set("text", "at this");
    form.set("url", "https://example.org/x");
    form.append("files", new File(["abc"], "note.txt", { type: "text/plain" }));
    const request = new Request("https://chat.example/app/share", { method: "POST", body: form });
    const [response] = await dispatch("fetch", { request });
    assert.equal(response.status, 303);
    assert.equal(response.headers.get("location"), "/app/#/share");
    const share = await getShare();
    assert.match(share.text, /Look[\s\S]*at this[\s\S]*https:\/\/example\.org\/x/);
    assert.equal(share.files.length, 1);
    assert.equal(share.files[0].name, "note.txt");
    assert.equal(share.tooLarge, false);
});

test("a share post whose files exceed the size cap keeps only the text and flags it", async () => {
    await clearShare();
    const form = new FormData();
    form.set("text", "big one");
    // Sized without allocating: only file.size is checked before dropping.
    const huge = new File(["x"], "huge.bin");
    Object.defineProperty(huge, "size", { value: MAX_SHARED_BYTES });
    form.append("files", huge);
    form.append("files", new File(["abc"], "small.txt"));
    const original = Request.prototype.formData;
    Request.prototype.formData = async () => form;
    try {
        const [response] = await dispatch("fetch", { request: new Request("https://chat.example/app/share", { method: "POST", body: "x" }) });
        assert.equal(response.status, 303);
    } finally {
        Request.prototype.formData = original;
    }
    const share = await getShare();
    assert.equal(share.text, "big one");
    assert.equal(share.files.length, 0);
    assert.equal(share.tooLarge, true);
});

test("message pushes carry Reply and Mark-as-read on Android, calls carry Answer and Decline, iOS gets none", async () => {
    shown.length = 0;
    await push({ kind: "dm", tag: "dm:uid-1", from: "Ivy", to: "me" });
    assert.equal(shown[0].title, "Ivy");
    assert.deepEqual(shown[0].actions.map(a => a.action), ["reply", "read"]);
    assert.equal(shown[0].actions[0].type, "text");
    assert.equal(shown[0].data.url, "/app/#/dm/uid-1");
    // Bound to the identity generation that received it.
    assert.equal(shown[0].data.generation, 1);

    await push({ kind: "call", tag: "call:c-9", from: "Ivy", video: true, to: "me" });
    assert.equal(shown[1].body, "Incoming video call");
    assert.equal(shown[1].requireInteraction, true);
    assert.deepEqual(shown[1].actions.map(a => a.action), ["answer", "decline"]);

    fakeNavigator.userAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Safari";
    await push({ kind: "mention", tag: "channel:4", from: "Kai", place: "general", to: "me" });
    assert.equal(shown[2].body, "mentioned you in #general");
    assert.deepEqual(shown[2].actions, []);
    fakeNavigator.userAgent = "Mozilla/5.0 (Linux; Android 14) Chrome/120 Mobile";

    // A visible window shows the event itself; calls still notify.
    windows.push({ visibilityState: "visible", url: "https://chat.example/app/#/home", postMessage() {}, focus: async () => {} });
    await push({ kind: "dm", tag: "dm:uid-2", from: "Lou", to: "me" });
    assert.equal(shown.length, 3);
    await push({ kind: "call", tag: "call:c-10", from: "Lou", to: "me" });
    assert.equal(shown.length, 4);
    windows.length = 0;
});

test("a muted conversation stays quiet while the app is open and is silent otherwise", async () => {
    shown.length = 0;
    await dispatch("message", { data: { type: "muted", tags: ["dm:quiet"] } });
    windows.push({ visibilityState: "hidden", url: "https://chat.example/app/#/home", postMessage() {}, focus: async () => {} });
    await push({ kind: "dm", tag: "dm:quiet", from: "Noa", to: "me" });
    assert.equal(shown.length, 0);
    windows.length = 0;
    await push({ kind: "dm", tag: "dm:quiet", from: "Noa", to: "me" });
    assert.equal(shown.length, 1);
    assert.equal(shown[0].silent, true);
    assert.equal(Object.hasOwn(shown[0], "vibrate"), false);
});

test("notification actions: decline and answer reach an open app, a closed app opens the call", async () => {
    const messages = [];
    const open = { visibilityState: "hidden", url: "https://chat.example/app/#/home", postMessage: m => messages.push(m), focus: async () => {} };
    windows.push(open);
    const notification = { close() {}, data: { url: "/app/#/call/c-9", tag: "call:c-9", generation: 1 } };
    await dispatch("notificationclick", { notification, action: "decline" });
    assert.deepEqual(messages, [{ type: "call-action", action: "decline", id: "c-9" }]);
    messages.length = 0;
    await dispatch("notificationclick", { notification, action: "answer" });
    assert.deepEqual(messages, [{ type: "open", url: "/app/#/call/c-9" }, { type: "call-action", action: "answer", id: "c-9" }]);
    windows.length = 0;
    await dispatch("notificationclick", { notification, action: "answer" });
    assert.deepEqual(opened, ["/app/?answer=c-9#/call/c-9"]);
    // The fresh window auto-accepts only with this single-use intent.
    const intent = await (await fakeCache.match("/app/__intents/answer.json")).json();
    assert.equal(intent.id, "c-9");
    assert.ok(intent.until > Date.now());
});

test("actions of a notification from another identity generation do nothing", async () => {
    for (const entry of await pending()) await remove(entry.id);
    opened.length = 0;
    const messages = [];
    const open = { visibilityState: "hidden", url: "https://chat.example/app/#/home", postMessage: m => messages.push(m), focus: async () => {} };
    windows.push(open);
    const stale = { close() {}, data: { url: "/app/#/dm/uid-1", tag: "dm:uid-1", generation: 7 } };
    await dispatch("notificationclick", { notification: stale, action: "reply", reply: "as the wrong account" });
    await dispatch("notificationclick", { notification: stale, action: "read" });
    assert.equal((await pending()).length, 0);
    assert.deepEqual(messages, []);
    const staleCall = { close() {}, data: { url: "/app/#/call/c-1", tag: "call:c-1" } };
    await dispatch("notificationclick", { notification: staleCall, action: "decline" });
    await dispatch("notificationclick", { notification: staleCall, action: "answer" });
    assert.deepEqual(messages, [{ type: "open", url: "/app/#/home" }]);
    windows.length = 0;
});

test("an inline reply is queued in the outbox and the open app is asked to send and mark read", async () => {
    for (const entry of await pending()) await remove(entry.id);
    const messages = [];
    windows.push({ visibilityState: "hidden", url: "https://chat.example/app/#/home", postMessage: m => messages.push(m), focus: async () => {} });
    const notification = { close() {}, data: { url: "/app/#/dm/uid-1", tag: "dm:uid-1", generation: 1 } };
    await dispatch("notificationclick", { notification, action: "reply", reply: "on my way" });
    const waiting = await pending();
    assert.equal(waiting.length, 1);
    assert.equal(waiting[0].kind, "dm");
    assert.equal(waiting[0].peer, "uid-1");
    assert.equal(waiting[0].text, "on my way");
    assert.deepEqual(messages, [{ type: "notification-action", action: "reply", tag: "dm:uid-1", flush: true }]);
    messages.length = 0;
    await dispatch("notificationclick", { notification, action: "read" });
    assert.deepEqual(messages, [{ type: "notification-action", action: "read", tag: "dm:uid-1", flush: false }]);
    windows.length = 0;
    for (const entry of await pending()) await remove(entry.id);
});

test("a private inline reply with the app closed opens the conversation so the app sends it", async () => {
    for (const entry of await pending()) await remove(entry.id);
    opened.length = 0;
    for (const tag of ["dm:uid-1", "conversation:g-1"]) {
        await dispatch("notificationclick", { notification: { close() {}, data: { tag, generation: 1 } }, action: "reply", reply: "later" });
    }
    assert.deepEqual(opened, ["/app/#/dm/uid-1", "/app/#/group/g-1"]);
    assert.equal((await pending()).length, 2, "the replies stay queued for the app");
    // The worker sends channel replies itself, without opening the app.
    await dispatch("notificationclick", { notification: { close() {}, data: { tag: "channel:4", generation: 1 } }, action: "reply", reply: "here" });
    assert.equal(opened.length, 2);
    for (const entry of await pending()) await remove(entry.id);
});


test("Apple push deliveries stay visible when foreground or mute state changed in flight", async () => {
    const previousUA = fakeNavigator.userAgent;
    try {
        for (const userAgent of ["Mozilla/5.0 (iPhone) Safari", "Mozilla/5.0 (Macintosh) Safari"]) {
            fakeNavigator.userAgent = userAgent;
            for (const [visibilityState, muted] of [["visible", false], ["visible", true], ["hidden", true]]) {
                windows.length = 0; shown.length = 0;
                windows.push({ visibilityState, url: "https://chat.example/app/#/home" });
                await dispatch("message", { data: { type: "muted", tags: muted ? ["dm:apple-muted"] : [] } });
                await push({ kind: "dm", tag: "dm:apple-muted", from: "Ivy", to: "me" });
                assert.equal(shown.length, 1, `${userAgent} ${visibilityState} must show a notification`);
                assert.equal(shown[0].silent, true);
                assert.equal(shown[0].renotify, false);
                assert.equal(Object.hasOwn(shown[0], "vibrate"), false);
            }
        }
    } finally {
        windows.length = 0;
        fakeNavigator.userAgent = previousUA;
        await dispatch("message", { data: { type: "muted", tags: [] } });
    }
});


test("desktop Chrome on macOS keeps foreground push suppression", async () => {
    const previousUA = fakeNavigator.userAgent;
    try {
        fakeNavigator.userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/141.0.0.0 Safari/537.36";
        windows.length = 0; shown.length = 0;
        windows.push({ visibilityState: "visible", url: "https://chat.example/app/#/home" });
        await push({ kind: "dm", tag: "dm:chrome", from: "Ivy", to: "me" });
        assert.equal(shown.length, 0);
    } finally {
        windows.length = 0;
        fakeNavigator.userAgent = previousUA;
    }
});

test("a push for another account, or with no linked account stored, shows nothing", async () => {
    shown.length = 0;
    await push({ kind: "dm", tag: "dm:uid-1", from: "Ivy", to: "me" });
    assert.equal(shown.length, 1);
    await push({ kind: "dm", tag: "dm:uid-1", from: "Ivy", to: "someone-else" });
    assert.equal(shown.length, 1);
    await push({ kind: "dm", tag: "dm:uid-1", from: "Ivy" }); // no recipient named
    assert.equal(shown.length, 1);
    // A guest replaced the linked account; its subscription may still deliver.
    await saveIdentity(generateIdentity(), { guest: true, linked: false, uniqueID: "guest:g1", deviceID: "phone-guest" },
        { onboarding: true, replaceAccount: true, fence: await beginIdentity() });
    await push({ kind: "dm", tag: "dm:uid-1", from: "Ivy" });
    await push({ kind: "call", tag: "call:c-11", from: "Ivy", to: "me" });
    assert.equal(shown.length, 1);
});
