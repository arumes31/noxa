// Review P2-8: the real app.js ratchet lifecycle (configureRatchetForTests,
// openRatchet, attach-before-publish, pollRatchet, stopRatchet) with fenced
// reconnect, disconnect and sign-out during an outbox flush. The consumer,
// store and core are recording fakes; their production behaviour is covered
// by the PG/Chromium product test.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";
import { ChatSession } from "../src/core/chat-session.js";
import { generateIdentity, keyFingerprint, publicKeyPEM, sign, toBase64 } from "../src/core/crypto.js";
import { announcedBinding, linkBindingTranscript, verifyLinkBinding } from "../src/core/link-binding.js";

// The desktop-signed device-link statement (DeviceLinkBindingTranscript).
const desktop = generateIdentity();
export function statementFor(device, { principal = "alice", binding = "tls-sha256:" + "ab".repeat(32) } = {}) {
    const transcript = linkBindingTranscript(principal, "desk", publicKeyPEM(device.edPublic), toBase64(device.xPublic), binding);
    return { binding, signature: toBase64(sign(transcript, desktop.edSecret)), approverDeviceID: "desk", approverKey: publicKeyPEM(desktop.edPublic), principal };
}

class FakeConnection extends EventTarget {
    open = true;
    on(type, fn) { const f = e => fn(e.detail); this.addEventListener(String(type), f); return () => this.removeEventListener(String(type), f); }
    close() { this.open = false; this.dispatchEvent(new Event("close")); }
}
const tick = (ms = 30) => new Promise(r => setTimeout(r, ms));

test("app lifecycle owns, polls and fences the ratchet consumer across reconnect, disconnect and sign-out", { timeout: 15000 }, async () => {
    const consumers = [], cores = [], attached = { dm: [], groups: [] }, quarantine = [];
    let flushGate = null, flushEntered = null, flushResult = null;
    let capabilities = [], announced = "";
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
        "./core/olm-directory.js": { signBundle: () => ({}) },
        "./core/olm-reset.js": { signReset: () => ({}) },
        "./core/link-binding.js": { verifyLinkBinding, announcedBinding },
        "./core/ratchet-consumer.js": { RatchetConsumer, connectionTransport: () => ({ admission: none, directory: none }) },
        "./core/crypto.js": { generateIdentity },
        "./core/device.js": { signIn: async () => ({ unique_id: "alice", device_id: "phone", client_id: "c1", capabilities: [...capabilities], tls_fingerprint: announced }) },
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
    const { app, connect, disconnect, forgetDevice, configureRatchetForTests } = mod.namespace;

    configureRatchetForTests({
        pollMS: 10,
        modules: async () => [{ openRatchetStore: async ns => ({ ns }) }, { createRatchetCore: async () => { const core = { closed: false, close() { core.closed = true; } }; cores.push(core); return core; } }],
    });
    // 1c-iii-b item 5: secure messaging is ON by default for a signed-in
    // session. A server without the capability blocks it with an explicit
    // reason; the session itself (channels, legacy history) continues and
    // nothing falls back to legacy sending.
    app.identity = generateIdentity();
    app.meta = { linkBinding: statementFor(app.identity), bindingAnchor: keyFingerprint(publicKeyPEM(desktop.edPublic)) };
    await connect();
    assert.equal(consumers.length, 0);
    assert.equal(app.ratchet, null);
    assert.equal(app.ratchetError?.code, "server_outdated");
    capabilities = ["noxa-olm-admission-v1", "noxa-olm-directory-v1"];
    // 1c-iii-a item 1: the binding comes only from the stored desktop-signed
    // statement; without one, or with one that does not verify, secure
    // messaging is blocked (one explanation: link the device again).
    const blocked = async (meta, code, note) => {
        app.meta = meta;
        await connect();
        assert.equal(app.ratchet, null, note);
        assert.equal(app.ratchetError?.code, code, note);
    };
    await blocked({}, "server_binding_unavailable", "no statement");
    await blocked({ linkBinding: statementFor(app.identity) }, "server_binding_mismatch", "a statement without the QR anchor (typed code / empty fingerprint)");
    const anchor = keyFingerprint(publicKeyPEM(desktop.edPublic));
    await blocked({ linkBinding: statementFor(generateIdentity()), bindingAnchor: anchor }, "server_binding_mismatch", "a statement for another device's keys");
    await blocked({ linkBinding: statementFor(app.identity, { principal: "mallory" }), bindingAnchor: anchor }, "server_binding_mismatch", "a statement for another account");
    await blocked({ linkBinding: statementFor(app.identity), bindingAnchor: "0000 0000 0000 0000" }, "server_binding_mismatch", "a statement from another approver than the one shown");
    await blocked({ linkBinding: { ...statementFor(app.identity), binding: "tls-sha256:" + "cd".repeat(32) }, bindingAnchor: anchor }, "server_binding_mismatch", "a binding the desktop did not sign");
    // Server certificate rotation: the announced fingerprint differs from the
    // signed binding -> blocked until the device is linked again.
    announced = "cd:".repeat(31) + "cd";
    await blocked({ linkBinding: statementFor(app.identity), bindingAnchor: anchor }, "server_binding_rotated", "a changed server certificate");
    announced = "ab".repeat(32);
    assert.equal(consumers.length, 0, "no consumer for a refused binding");
    app.meta = { linkBinding: statementFor(app.identity), bindingAnchor: keyFingerprint(publicKeyPEM(desktop.edPublic)) };
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
    configureRatchetForTests(null);
});
