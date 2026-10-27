// Review P2-8: the real app.js ratchet lifecycle (enableRatchetTestSession,
// openRatchet, attach-before-publish, pollRatchet, stopRatchet) with fenced
// reconnect, disconnect and sign-out during an outbox flush. The consumer,
// store and core are recording fakes; their production behaviour is covered
// by the PG/Chromium product test.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";
import { ChatSession } from "../src/core/chat-session.js";
import { generateIdentity } from "../src/core/crypto.js";

class FakeConnection extends EventTarget {
    open = true;
    on(type, fn) { const f = e => fn(e.detail); this.addEventListener(String(type), f); return () => this.removeEventListener(String(type), f); }
    close() { this.open = false; this.dispatchEvent(new Event("close")); }
}
const tick = (ms = 30) => new Promise(r => setTimeout(r, ms));

test("app lifecycle owns, polls and fences the ratchet consumer across reconnect, disconnect and sign-out", { timeout: 15000 }, async () => {
    const consumers = [], cores = [], attached = { dm: [], groups: [] }, quarantine = [];
    let flushGate = null, flushEntered = null, flushResult = null;
    class RatchetConsumer {
        constructor(options) { Object.assign(this, options); this.closed = false; this.drains = 0; this.sent = []; consumers.push(this); }
        async drain() { this.drains++; return { quarantined: [{ id: "q" + consumers.indexOf(this), scope: "direct" }] }; }
        // Mirrors the real consumer: every operation first runs the app's
        // assertCurrent closure, so a stale session can never transport.
        async send(intent) { this.assertCurrent(); this.sent.push(intent); return { receipt: { state: "accepted" } }; }
        async close() { this.closed = true; }
    }
    class DirectMessages extends EventTarget {
        constructor() { super(); this.ready = Promise.resolve(); }
        attachRatchet(c) { this.ratchet = c; attached.dm.push(c); return Promise.resolve(); }
        async showQuarantined(list) { quarantine.push(list); }
        send(peer, text, nick, { id }) { return this.ratchet.send({ logicalID: id, peer, body: { text } }); }
        async importHistory() {} close() {}
    }
    class Groups extends EventTarget {
        attachRatchet(c) { attached.groups.push(c); return Promise.resolve(); }
        showQuarantined() {} async refresh() {} close() {}
    }
    class Empty extends EventTarget { async refresh() {} close() {} async leave() {} }
    const none = () => {};
    const sources = {
        "./core/chat-session.js": { ChatSession },
        "./core/direct.js": { DirectMessages }, "./core/calls.js": { Calls: Empty }, "./core/groups.js": { Groups }, "./core/voice.js": { Voice: Empty },
        "./core/connection.js": { Connection: { open: async () => new FakeConnection() } }, "./core/frame.js": { Msg: {} },
        "./core/olm-directory.js": { serverBinding: v => "tls-sha256:" + v, signBundle: () => ({}) },
        "./core/ratchet-consumer.js": { RatchetConsumer, connectionTransport: () => ({ admission: none, directory: none }) },
        "./core/crypto.js": { generateIdentity },
        "./core/device.js": { signIn: async () => ({ unique_id: "alice", device_id: "phone", client_id: "c1" }) },
        "./core/identity-store.js": { assertIdentityFence: async () => {}, beginIdentity: async () => ({ generation: 1, active: true }), clearIdentity: async () => {}, loadIdentity: none, saveIdentity: none, updateMeta: async () => {} },
        "./core/outbox.js": {
            enqueue: none, showPending: async () => {}, waitingCount: async () => 0,
            flush: async (session, dm) => {
                if (!flushGate) return;
                flushEntered();
                await flushGate;
                try { await dm.send("bob", "queued while signing out", "", { id: "o1" }); flushResult = "sent"; } catch (e) { flushResult = e.code; }
            },
        },
        "./core/push.js": { announceRead: none, closeNotifications: none, reportVisibility: none, setBadge: none, syncPush: async () => ({}) },
        "./core/settings.js": { get: () => [] }, "./core/servers.js": { addServer: () => [] },
        "./notifier.js": { notifier: { install() {}, decide() { return { count: true }; }, read() {} } },
        "./core/conversation-prefs.js": { activityShared: () => false, isMuted: () => false, syncMuted: () => {}, watchCallLog: () => {}, resetCallLog: async () => {} },
        "./core/saved.js": { SavedMessages: class extends EventTarget {} },
    };
    const context = vm.createContext({ EventTarget, Event, CustomEvent, setTimeout, clearTimeout, clearInterval, console, document: { visibilityState: "hidden", addEventListener() {} } });
    const mod = new vm.SourceTextModule(await fs.readFile(new URL("../src/app.js", import.meta.url), "utf8"), { context });
    await mod.link(spec => new vm.SyntheticModule(Object.keys(sources[spec]), function () { for (const [k, v] of Object.entries(sources[spec])) this.setExport(k, v); }, { context }));
    await mod.evaluate();
    const { app, connect, disconnect, forgetDevice, enableRatchetTestSession } = mod.namespace;

    // Without explicit enablement nothing is constructed (production default).
    await connect();
    assert.equal(consumers.length, 0);
    assert.equal(app.ratchet, null);

    enableRatchetTestSession({
        serverBinding: "ab".repeat(32), pollMS: 10,
        modules: async () => [{ openRatchetStore: async ns => ({ ns }) }, { createRatchetCore: async () => { const core = { closed: false, close() { core.closed = true; } }; cores.push(core); return core; } }],
    });
    const first = await connect();
    const a = consumers[0];
    assert.equal(JSON.stringify(a.namespace), JSON.stringify({ server: "tls-sha256:" + "ab".repeat(32), principal: "alice", device: "phone" }));
    assert.equal(attached.dm[0], a, "attached to DirectMessages");
    assert.equal(attached.groups[0], a, "attached to Groups");
    assert.equal(app.ratchet.consumer, a);
    await tick();
    assert.ok(a.drains >= 2, "session-owned poll timer drains");
    assert.equal(JSON.stringify(quarantine.at(-1)), JSON.stringify([{ id: "q0", scope: "direct" }]), "quarantine reaches the UI");
    a.assertCurrent();

    // Reconnect/session replacement: old consumer closed and fenced.
    await connect();
    const b = consumers[1];
    assert.notEqual(b, a);
    assert.equal(a.closed, true);
    await tick();
    assert.equal(cores[0].closed, true);
    assert.throws(() => a.assertCurrent(), e => e.code === "fenced");
    b.assertCurrent();
    const drained = a.drains;
    await tick();
    assert.equal(a.drains, drained, "old poll stopped");
    assert.notEqual(app.session, first);

    // Disconnect stops and fences the current consumer.
    disconnect();
    assert.equal(b.closed, true);
    assert.throws(() => b.assertCurrent(), e => e.code === "fenced");
    const bDrains = b.drains;
    await tick();
    assert.equal(b.drains, bDrains);

    // Sign-out while an outbox flush is in flight: the flush resumes after
    // forgetDevice and the stale consumer refuses before any transport.
    let release;
    flushGate = new Promise(r => { release = r; });
    const entered = new Promise(r => { flushEntered = r; });
    await connect();
    const c = consumers[2];
    await entered;
    await forgetDevice({ generation: 1, active: true });
    assert.equal(c.closed, true);
    release();
    await tick();
    assert.equal(flushResult, "fenced");
    assert.equal(c.sent.length, 0);
    assert.equal(app.ratchet, null);
    enableRatchetTestSession(null);
});
