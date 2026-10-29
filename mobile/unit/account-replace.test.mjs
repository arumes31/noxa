// Task 4 review P2-1 and P1-1 (app level): linking or restoring a DIFFERENT
// member account replaces the old account's data before the new account
// connects (an old queued outbox entry is never sent as the new account, old
// DMs/pins are gone); re-linking the same account keeps it. A server key for
// this account that the linking desktop did not vouch for blocks secure
// messaging. Real app.js, identity store, outbox and fake IndexedDB; the chat
// session, DMs and transport are recording fakes.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
globalThis.localStorage ??= { getItem: () => null, setItem() {}, removeItem() {} };
const db = await import("../src/core/db.js");
const identityStore = await import("../src/core/identity-store.js");
const outbox = await import("../src/core/outbox.js");
const { ChatSession } = await import("../src/core/chat-session.js");
const { generateIdentity, keyFingerprint, publicKeyPEM, sign, toBase64 } = await import("../src/core/crypto.js");
const { announcedBinding, linkBindingTranscript, verifyLinkBinding } = await import("../src/core/link-binding.js");

const { Stores, keys, getSealed, putSealed } = db;
const BINDING = "tls-sha256:" + "ab".repeat(32);
const desktop = generateIdentity();
const statementFor = (device, principal) => {
    const transcript = linkBindingTranscript(principal, "desk", publicKeyPEM(device.edPublic), toBase64(device.xPublic), BINDING);
    return { binding: BINDING, signature: toBase64(sign(transcript, desktop.edSecret)), approverDeviceID: "desk", approverKey: publicKeyPEM(desktop.edPublic), principal };
};

const Msg = { Error: 1, KeyPublish: 42, KeyRequest: 43, KeyResponse: 44 };
class FakeConnection extends EventTarget {
    open = true;
    send() {}
    async request(type, msg) { if (type === Msg.KeyRequest) return { unique_id: msg.unique_id, public_key: "" }; throw new Error("unexpected request " + type); }
    on(type, fn) { const f = e => fn(e.detail); this.addEventListener(String(type), f); return () => this.removeEventListener(String(type), f); }
    probe() { return Promise.resolve(); }
    close() { this.open = false; this.dispatchEvent(new Event("close")); }
}
const tick = (ms = 60) => new Promise(r => setTimeout(r, ms));
async function until(check, ms = 5000) { const end = Date.now() + ms; while (!check() && Date.now() < end) await tick(20); }

const world = { signInAs: "alice", capabilities: [], sent: [], dms: [], ownEntry: null, consumers: [] };
class DirectMessages extends EventTarget {
    constructor(session, meta) {
        super();
        this.session = session; this.meta = meta;
        // What this account's view loads from storage when it is created.
        this.ready = keys(Stores.dm).then(list => { this.loaded = list; });
        world.dms.push(this);
    }
    attachRatchet(c) { this.ratchet = c; return Promise.resolve(); }
    async showQuarantined() {}
    showQueued() {}
    async send(peer, text) {
        if (this.ratchet) return this.ratchet.send({ peer, body: { text } });
        world.sent.push({ as: this.session.uniqueID, peer, text });
    }
    async devicesOf(uid) { return uid === this.session.uniqueID && world.ownEntry ? world.ownEntry : { trust: "ok", devices: [] }; }
    async importHistory() {}
    close() {}
}
class RatchetConsumer {
    constructor(options) { Object.assign(this, options); this.closed = false; world.consumers.push(this); }
    async drain() { return { quarantined: [] }; }
    async send() { this.assertCurrent(); throw Object.assign(new Error("not in this test"), { code: "invalid" }); }
    onControl() {}
    async close() { this.closed = true; }
}
class Groups extends EventTarget { attachRatchet(c) { this.ratchet = c; return Promise.resolve(); } showQuarantined() {} async refresh() {} close() {} }
class Empty extends EventTarget { async refresh() {} close() {} async leave() {} }
const none = () => {};
const sources = {
    "./core/chat-session.js": { ChatSession },
    "./core/direct.js": { DirectMessages }, "./core/calls.js": { Calls: Empty }, "./core/groups.js": { Groups }, "./core/voice.js": { Voice: Empty },
    "./core/saved.js": { SavedMessages: class extends EventTarget {} },
    "./core/connection.js": { Connection: { open: async () => new FakeConnection() } }, "./core/frame.js": { Msg },
    "./core/olm-directory.js": { signBundle: () => ({}) }, "./core/olm-reset.js": { signReset: () => ({}) },
    "./core/link-binding.js": { verifyLinkBinding, announcedBinding },
    "./core/ratchet-consumer.js": { RatchetConsumer, connectionTransport: () => ({ admission: none, directory: none }) },
    "./core/crypto.js": { generateIdentity }, "./core/dm-trust.js": { signKeyRecord: () => "record" },
    "./core/device.js": { signIn: async () => ({ unique_id: world.signInAs, device_id: "phone-" + world.signInAs, client_id: "c1", capabilities: [...world.capabilities], tls_fingerprint: "ab".repeat(32) }) },
    "./core/identity-store.js": identityStore, "./core/outbox.js": outbox,
    "./core/push.js": { announceRead: none, closeNotifications: none, reportVisibility: none, setBadge: none, syncPush: async () => ({}) },
    "./core/settings.js": { get: () => [] }, "./core/servers.js": { addServer: () => [] },
    "./notifier.js": { notifier: { install() {}, decide() { return { count: true }; }, read() {} } },
    "./core/conversation-prefs.js": { activityShared: () => false, isMuted: () => false, syncMuted: () => {}, watchCallLog: () => {}, resetCallLog: async () => {} },
};
const context = vm.createContext({ EventTarget, Event, CustomEvent, setTimeout, clearTimeout, clearInterval, console, document: { visibilityState: "hidden", addEventListener() {} } });
const mod = new vm.SourceTextModule(await fs.readFile(new URL("../src/app.js", import.meta.url), "utf8"), { context });
await mod.link(spec => new vm.SyntheticModule(Object.keys(sources[spec]), function () { for (const [k, v] of Object.entries(sources[spec])) this.setExport(k, v); }, { context }));
await mod.evaluate();
const { app, connect, disconnect, rememberLinkedIdentity, configureRatchetForTests } = mod.namespace;
configureRatchetForTests({ pollMS: 10, modules: async () => [{ openRatchetStore: async ns => ({ ns }) }, { createRatchetCore: async () => ({ close() {} }) }] });

/** Account A signed in on this browser with a DM, a pin and a queued outbox entry. */
async function accountWithData(uid) {
    disconnect();
    world.signInAs = uid; world.sent = [];
    const identity = generateIdentity();
    const fence = await identityStore.saveIdentity(identity, { uniqueID: uid, deviceID: "phone-" + uid }, { onboarding: true, fence: await identityStore.beginIdentity(), replaceAccount: true });
    app.identity = identity; app.identityFence = fence; app.meta = { uniqueID: uid, deviceID: "phone-" + uid };
    await putSealed(Stores.dm, "bob", { peer: "bob", nickname: "Bob", messages: [{ id: "m1", from: "bob", body: "private to " + uid, sentAt: 1 }] });
    await putSealed(Stores.pins, "bob", { signingKey: "k", devices: ["b1"] });
    await outbox.enqueue({ kind: "dm", peer: "bob", nickname: "Bob" }, "queued by " + uid);
    // Disconnected (the Connect screen shows while the stores still exist).
    app.paused = true;
}

const stored = async () => ({ dm: await keys(Stores.dm), pins: await keys(Stores.pins), outbox: await keys(Stores.outbox) });

test("P2-1: QR-linking a different account wipes the old account before it connects; its outbox is never sent", async () => {
    await accountWithData("alice");
    const next = generateIdentity();
    const fence = await identityStore.beginIdentity();
    const committed = await rememberLinkedIdentity(next, { via: "link", linkBinding: statementFor(next, "carol"), bindingAnchor: keyFingerprint(publicKeyPEM(desktop.edPublic)) }, fence);
    assert.deepEqual(await stored(), { dm: [], pins: [], outbox: [] }, "old DMs, pins and outbox are gone at link time");
    world.signInAs = "carol";
    await connect({ fence: committed });
    await tick(1000);
    assert.deepEqual(world.sent, [], "nothing of alice's outbox is sent as carol");
    assert.deepEqual(world.dms.at(-1).loaded, []);
    assert.equal((await identityStore.loadIdentity()).meta.uniqueID, "carol");
});

test("P2-1: a typed-code/passkey link of a different account is wiped by connect before the session loads or flushes", async () => {
    await accountWithData("alice");
    const next = generateIdentity();
    const committed = await rememberLinkedIdentity(next, { via: "passkey" }, await identityStore.beginIdentity());
    assert.equal((await identityStore.loadIdentity()).meta.priorPrincipal, "alice", "account not known yet: the old one is remembered");
    world.signInAs = "dave";
    await connect({ fence: committed });
    await tick(1000);
    assert.deepEqual(world.sent, [], "alice's queued message is never sent as dave");
    assert.deepEqual(world.dms.at(-1).loaded, [], "dave's view never loads alice's conversations");
    assert.deepEqual(await stored(), { dm: [], pins: [], outbox: [] });
    const meta = (await identityStore.loadIdentity()).meta;
    assert.equal(meta.uniqueID, "dave");
    assert.equal(meta.priorPrincipal, undefined);
    assert.equal(app.identityFence.generation, (await identityStore.beginIdentity()).generation, "the app holds the advanced fence");
});

test("P2-1: re-linking or restoring the SAME account keeps its data and sends its outbox as that account", async () => {
    await accountWithData("alice");
    const next = generateIdentity();
    const committed = await rememberLinkedIdentity(next, { via: "link", linkBinding: statementFor(next, "alice"), bindingAnchor: keyFingerprint(publicKeyPEM(desktop.edPublic)) }, await identityStore.beginIdentity());
    assert.deepEqual((await stored()).dm, ["bob"]);
    await connect({ fence: committed });
    await until(() => world.sent.length);
    assert.deepEqual(world.sent, [{ as: "alice", peer: "bob", text: "queued by alice" }]);
    assert.equal((await getSealed(Stores.dm, "bob")).messages[0].body, "private to alice");

    // The same account linked by passkey (account not known at link time).
    await accountWithData("alice");
    const again = await rememberLinkedIdentity(generateIdentity(), { via: "passkey" }, await identityStore.beginIdentity());
    await connect({ fence: again });
    await until(() => world.sent.length);
    assert.deepEqual(world.sent, [{ as: "alice", peer: "bob", text: "queued by alice" }]);
    assert.deepEqual((await stored()).pins, ["bob"]);
});

test("P2-1: restoring a backup of a different account replaces the old account", async () => {
    await accountWithData("alice");
    const committed = await rememberLinkedIdentity(generateIdentity(), { via: "backup", restored: true, uniqueID: "erin", deviceID: "phone-erin" }, await identityStore.beginIdentity());
    assert.deepEqual(await stored(), { dm: [], pins: [], outbox: [] });
    world.signInAs = "erin";
    await connect({ fence: committed });
    await tick(1000);
    assert.deepEqual(world.sent, []);
});

test("P1-1: a server key for this account that the linking desktop did not vouch for blocks secure messaging", async () => {
    await accountWithData("alice");
    await outbox.remove((await keys(Stores.outbox))[0]);
    world.capabilities = ["noxa-olm-admission-v1", "noxa-olm-directory-v1"];
    world.ownEntry = { trust: "unavailable", devices: [], block: Object.assign(new Error("own root mismatch"), { code: "server_binding_mismatch" }) };
    app.meta = { ...app.meta, linkBinding: statementFor(app.identity, "alice"), bindingAnchor: keyFingerprint(publicKeyPEM(desktop.edPublic)) };
    try {
        app.paused = false;
        await connect();
        const consumer = world.consumers.at(-1);
        await tick();
        assert.equal(app.ratchet, null, "the consumer is stopped");
        assert.equal(consumer.closed, true);
        assert.equal(app.ratchetError?.code, "server_binding_mismatch", "the re-link explanation");
        assert.equal(world.dms.at(-1).ratchet, null, "sends are refused with the blocking reason, never sealed");
        await assert.rejects(consumer.deviceKey("alice", "evil"), e => e.code === "fenced", "no own-device key for the consumer");
    } finally {
        world.capabilities = []; world.ownEntry = null;
        disconnect();
    }
});
