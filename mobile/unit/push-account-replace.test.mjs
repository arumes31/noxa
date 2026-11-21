// Replacing the account this browser holds ends the previous account's push
// subscription: in the browser always, and on the server while that account's
// session is still open. Harness as in account-replace.test.mjs, with the real
// core/push.js over a fake browser push surface.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
globalThis.localStorage ??= { getItem: () => null, setItem() {}, removeItem() {} };

// --- fake browser push surface -------------------------------------------------
const browser = { unsubscribes: 0, sub: null, shown: [] };
const vapidBytes = new Uint8Array(65).fill(7);
const vapidKey = Buffer.from(vapidBytes).toString("base64url");
const makeSub = () => ({
    endpoint: "https://push.example/endpoint-of-this-browser", options: { applicationServerKey: vapidBytes.buffer },
    getKey: name => new Uint8Array(name === "auth" ? 16 : 65).fill(1),
    unsubscribe: async () => { browser.unsubscribes++; browser.sub = null; return true; },
});
const reg = {
    pushManager: { getSubscription: async () => browser.sub, subscribe: async () => (browser.sub = makeSub()) },
    getNotifications: async () => browser.shown.filter(n => !n.closed),
};
globalThis.Notification = { permission: "granted", requestPermission: async () => "granted" };
globalThis.document = { visibilityState: "hidden", addEventListener() {} }; // push.js reportVisibility reads it
globalThis.window = { PushManager: class {}, Notification: globalThis.Notification };
Object.defineProperty(globalThis, "navigator", { configurable: true, value: { userAgent: "Mozilla/5.0 (Linux; Android 14) Chrome/120 Mobile", serviceWorker: { getRegistration: async () => reg, ready: Promise.resolve(reg) } } });

const db = await import("../src/core/db.js");
const identityStore = await import("../src/core/identity-store.js");
const outbox = await import("../src/core/outbox.js");
const pushModule = await import("../src/core/push.js");
const { Msg } = await import("../src/core/frame.js");
const { ChatSession } = await import("../src/core/chat-session.js");
const { generateIdentity } = await import("../src/core/crypto.js");
const { announcedBinding, verifyLinkBinding } = await import("../src/core/link-binding.js");
const { Stores, keys, putSealed } = db;

const world = { signInAs: "alice", requests: [] };
class FakeConnection extends EventTarget {
    open = true;
    as = world.signInAs;
    send(type, msg) { world.requests.push({ as: this.as, type, sent: true, ...msg }); }
    async request(type, msg) {
        world.requests.push({ as: this.as, type, ...msg });
        if (type === Msg.KeyRequest) return { unique_id: msg.unique_id, public_key: "" };
        if (type === Msg.DeviceRequest) {
            if (msg.action === "push_config") return { action: msg.action, vapid_public_key: vapidKey, preferences: { subscribed: true } };
            if (msg.action === "push_subscribe" || msg.action === "push_unsubscribe") return { action: msg.action, preferences: { subscribed: msg.action === "push_subscribe" } };
        }
        throw new Error("unexpected request " + type);
    }
    on(type, fn) { const f = e => fn(e.detail); this.addEventListener(String(type), f); return () => this.removeEventListener(String(type), f); }
    probe() { return Promise.resolve(); }
    close() { this.open = false; this.dispatchEvent(new Event("close")); }
}
const tick = (ms = 60) => new Promise(r => setTimeout(r, ms));
class DirectMessages extends EventTarget {
    constructor(session) { super(); this.session = session; this.ready = Promise.resolve(); }
    attachRatchet() { return Promise.resolve(); } async showQuarantined() {} showQueued() {}
    async send() {} async devicesOf() { return { trust: "ok", devices: [] }; } async importHistory() {} close() {}
}
class RatchetConsumer { constructor(o) { Object.assign(this, o); } async drain() { return { quarantined: [] }; } onControl() {} async close() {} }
class Groups extends EventTarget { attachRatchet() { return Promise.resolve(); } showQuarantined() {} async refresh() {} close() {} }
class Empty extends EventTarget { async refresh() {} close() {} async leave() {} }
const none = () => {};
const sources = {
    "./core/chat-session.js": { ChatSession },
    "./core/direct.js": { DirectMessages }, "./core/calls.js": { Calls: Empty }, "./core/groups.js": { Groups }, "./core/voice.js": { Voice: Empty },
    "./core/saved.js": { SavedMessages: class extends EventTarget {} },
    "./core/connection.js": { Connection: { open: async () => new FakeConnection() } }, "./core/reconnect-hint.js": { noteShutdownJitter() {}, clearShutdownJitter() {}, takeShutdownJitterMs: () => 0 }, "./core/frame.js": { Msg },
    "./core/olm-directory.js": { signBundle: () => ({}) }, "./core/olm-reset.js": { signReset: () => ({}) },
    "./core/link-binding.js": { verifyLinkBinding, announcedBinding },
    "./core/ratchet-consumer.js": { RatchetConsumer, connectionTransport: () => ({ admission: none, directory: none }) },
    "./core/crypto.js": { generateIdentity }, "./core/dm-trust.js": { signKeyRecord: () => "record" },
    "./core/device.js": { signIn: async () => ({ resume_token: "tok", unique_id: world.signInAs, device_id: "phone-" + world.signInAs, client_id: "c1", nickname: world.signInAs, capabilities: [], tls_fingerprint: "ab".repeat(32) }) },
    "./core/identity-store.js": identityStore, "./core/outbox.js": outbox,
    "./core/push.js": pushModule,
    "./core/settings.js": { get: () => [] }, "./core/servers.js": { addServer: () => [] },
    "./notifier.js": { notifier: { install() {}, decide() { return { count: true }; }, read() {} } },
    "./core/conversation-prefs.js": { activityShared: () => false, isMuted: () => false, syncMuted: () => {}, watchCallLog: () => {}, resetCallLog: async () => {} },
};
const context = vm.createContext({ EventTarget, Event, CustomEvent, setTimeout, clearTimeout, clearInterval, console, document: { visibilityState: "hidden", addEventListener() {} } });
const mod = new vm.SourceTextModule(await fs.readFile(new URL("../src/app.js", import.meta.url), "utf8"), { context });
await mod.link(spec => new vm.SyntheticModule(Object.keys(sources[spec]), function () { for (const [k, v] of Object.entries(sources[spec])) this.setExport(k, v); }, { context }));
await mod.evaluate();
const { app, connect, disconnect, rememberLinkedIdentity } = mod.namespace;

const device = r => r.type === Msg.DeviceRequest;
const actions = (from = 0) => world.requests.slice(from).filter(device).map(r => `${r.as}:${r.action}`);


async function linkedAccount(uid) {
    disconnect();
    world.signInAs = uid;
    const identity = generateIdentity();
    const meta = { uniqueID: uid, deviceID: "phone-" + uid, linked: true };
    const fence = await identityStore.saveIdentity(identity, meta, { onboarding: true, fence: await identityStore.beginIdentity(), replaceAccount: true });
    app.identity = identity; app.identityFence = fence; app.meta = { ...meta };
    await putSealed(Stores.dm, "bob", { peer: "bob", nickname: "Bob", messages: [{ id: "m1", from: "bob", body: "private to " + uid, sentAt: 1 }] });
    await connect();
    await tick(300);
    assert.ok(world.requests.some(r => device(r) && r.as === uid && r.action === "push_subscribe"), "the linked session registers its push endpoint");
    assert.ok(browser.sub, "the browser holds a push subscription");
}

test("a guest login over a stored linked account ends that account's browser push subscription", async () => {
    await linkedAccount("alice");
    // Back on the connect screen with alice still stored (only a disconnect).
    disconnect();
    app.paused = true;
    const mark = world.requests.length;
    world.signInAs = "guest:g1";
    await connect({ guest: { nickname: "Guest", serverPassword: "" } });
    await tick(400);
    assert.equal((await identityStore.loadIdentity()).meta.uniqueID, "guest:g1");
    assert.deepEqual(await keys(Stores.dm), []);
    assert.equal(await reg.pushManager.getSubscription(), null);
    assert.deepEqual(actions(mark), [], "the guest session sends no push request of its own");
});

test("replacing an account whose session is still open also removes the subscription on the server", async () => {
    await linkedAccount("carol");
    const mark = world.requests.length;
    const committed = await rememberLinkedIdentity(generateIdentity(), { via: "passkey" }, await identityStore.beginIdentity());
    world.signInAs = "dave";
    await connect({ fence: committed });
    await tick(400);
    assert.equal((await identityStore.loadIdentity()).meta.uniqueID, "dave");
    assert.ok(actions(mark).includes("carol:push_unsubscribe"), JSON.stringify(actions(mark)));
    assert.equal(browser.unsubscribes, 2);
    disconnect();
});
