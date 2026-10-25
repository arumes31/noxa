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
const { getShare, clearShare } = await import("../src/core/share-store.js");

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
});

test("message pushes carry Reply and Mark-as-read on Android, calls carry Answer and Decline, iOS gets none", async () => {
    shown.length = 0;
    await push({ kind: "dm", tag: "dm:uid-1", from: "Ivy" });
    assert.equal(shown[0].title, "Ivy");
    assert.deepEqual(shown[0].actions.map(a => a.action), ["reply", "read"]);
    assert.equal(shown[0].actions[0].type, "text");
    assert.equal(shown[0].data.url, "/app/#/dm/uid-1");

    await push({ kind: "call", tag: "call:c-9", from: "Ivy", video: true });
    assert.equal(shown[1].body, "Incoming video call");
    assert.equal(shown[1].requireInteraction, true);
    assert.deepEqual(shown[1].actions.map(a => a.action), ["answer", "decline"]);

    fakeNavigator.userAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Safari";
    await push({ kind: "mention", tag: "channel:4", from: "Kai", place: "general" });
    assert.equal(shown[2].body, "mentioned you in #general");
    assert.deepEqual(shown[2].actions, []);
    fakeNavigator.userAgent = "Mozilla/5.0 (Linux; Android 14) Chrome/120 Mobile";

    // A visible window shows the event itself; calls still notify.
    windows.push({ visibilityState: "visible", url: "https://chat.example/app/#/home", postMessage() {}, focus: async () => {} });
    await push({ kind: "dm", tag: "dm:uid-2", from: "Lou" });
    assert.equal(shown.length, 3);
    await push({ kind: "call", tag: "call:c-10", from: "Lou" });
    assert.equal(shown.length, 4);
    windows.length = 0;
});

test("a muted conversation stays quiet while the app is open and is silent otherwise", async () => {
    shown.length = 0;
    await dispatch("message", { data: { type: "muted", tags: ["dm:quiet"] } });
    windows.push({ visibilityState: "hidden", url: "https://chat.example/app/#/home", postMessage() {}, focus: async () => {} });
    await push({ kind: "dm", tag: "dm:quiet", from: "Noa" });
    assert.equal(shown.length, 0);
    windows.length = 0;
    await push({ kind: "dm", tag: "dm:quiet", from: "Noa" });
    assert.equal(shown.length, 1);
    assert.equal(shown[0].silent, true);
});

test("notification actions: decline and answer reach an open app, a closed app opens the call", async () => {
    const messages = [];
    const open = { visibilityState: "hidden", url: "https://chat.example/app/#/home", postMessage: m => messages.push(m), focus: async () => {} };
    windows.push(open);
    const notification = { close() {}, data: { url: "/app/#/call/c-9", tag: "call:c-9" } };
    await dispatch("notificationclick", { notification, action: "decline" });
    assert.deepEqual(messages, [{ type: "call-action", action: "decline", id: "c-9" }]);
    messages.length = 0;
    await dispatch("notificationclick", { notification, action: "answer" });
    assert.deepEqual(messages, [{ type: "open", url: "/app/#/call/c-9" }, { type: "call-action", action: "answer", id: "c-9" }]);
    windows.length = 0;
    await dispatch("notificationclick", { notification, action: "answer" });
    assert.deepEqual(opened, ["/app/?answer=c-9#/call/c-9"]);
});

test("an inline reply is queued in the outbox and the open app is asked to send and mark read", async () => {
    for (const entry of await pending()) await remove(entry.id);
    const messages = [];
    windows.push({ visibilityState: "hidden", url: "https://chat.example/app/#/home", postMessage: m => messages.push(m), focus: async () => {} });
    const notification = { close() {}, data: { url: "/app/#/dm/uid-1", tag: "dm:uid-1" } };
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
