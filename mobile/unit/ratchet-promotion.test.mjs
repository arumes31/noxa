// 1c-iii-b item 0: consumer-level tests of PWA reset-chain promotion
// (RatchetConsumer #promote through the real send path) and the 7-day
// old-pin receive window (through the real drain/receive path).
import test from "node:test";
import assert from "node:assert/strict";
import { RatchetConsumer } from "../src/core/ratchet-consumer.js";
import { generateIdentity, toBase64 } from "../src/core/crypto.js";
import { signReset } from "../src/core/olm-reset.js";
import * as wire from "../src/core/olm-admission.js";

const server = "tls-sha256:" + "a".repeat(64);
const raw = b => toBase64(b).replace(/=+$/, "");
const b64 = b => Buffer.from(b).toString("base64");
const nowS = () => Math.floor(Date.now() / 1000);
const keys = () => { const i = generateIdentity(); return { identity: raw(i.xPublic), signing: raw(i.edPublic) }; };
const trustID = JSON.stringify(["bob", "b1"]);

function chain(noxa, n) {
    const pins = [{ epoch: 1, ...keys() }], resets = [];
    for (let i = 0; i < n; i++) {
        const cur = pins.at(-1), next = { epoch: cur.epoch + 1, ...keys() };
        resets.push(signReset({ version: "noxa-olm-reset-v1", server, principal: "bob", device: "b1", transition_id: "t" + i,
            old_epoch: cur.epoch, old_identity: cur.identity, old_signing: cur.signing, new_epoch: next.epoch, new_identity: next.identity, new_signing: next.signing,
            generation: 2, publication_digest: "cd".repeat(32), reason: "lost" }, noxa.edSecret));
        pins.push(next);
    }
    return { pins, resets };
}

// A store whose only durable record is bob's trust entry.
function trustStore(saved) {
    const writes = [];
    return {
        writes,
        async read(refs = []) {
            const records = {};
            for (const r of refs) records[JSON.stringify(r)] = r[0] === "trust" && r[1] === trustID
                ? { revision: 1 + writes.length, data: new TextEncoder().encode(JSON.stringify(saved)) } : { revision: 0, data: null };
            return { records };
        },
        async setTrust(s, id, value) { writes.push(value); saved.value = value; },
        async accountAuthority() { return { accountID: "local", epoch: 1, blocked: false, nextAttempt: 0, fence: 1, retired: { through: 0, above: [] } }; },
        async checkAccountAuthority() {},
        async pendingRefs() { return []; },
    };
}
function consumer(store, admission, noxa) {
    const c = new RatchetConsumer({
        store, core: {}, namespace: { server, principal: "alice", device: "a" }, admission, directory: async () => ({}),
        deviceKey: async () => noxa.edPublic, signBundle: async b => b, assertCurrent: () => {},
    });
    c.readyValue = { identity: "id", epoch: 1, expiresAt: nowS() + 86400 };
    c.readiness = Promise.resolve(c.readyValue);
    c.readyAt = Date.now();
    c.identity = "id";
    return c;
}

test("send promotes a cached pin only through the signed reset chain, recording retired pins", async () => {
    const noxa = generateIdentity(), { pins, resets } = chain(noxa, 2);
    const run = async (resetPage, target) => {
        const saved = { revoked: false, value: { principal: "bob", device: "b1", noxa: b64(noxa.edPublic), pin: pins[0] } };
        const store = trustStore(saved);
        const roster = { revision: 0, epoch: 0, rosters: [{ principal: "alice", version: 1 }, { principal: "bob", version: 1 }],
            targets: [{ principal: "alice", device: "a", account_epoch: 1, identity: "id", local: true }, { principal: "bob", device: "b1", account_epoch: target.epoch, identity: target.identity }] };
        const c = consumer(store, async r => {
            if (r.action === "snapshot") return roster;
            if (r.action === "reset_chain") return resetPage.filter(x => x.old_epoch >= r.after_epoch);
            return {};
        }, noxa);
        const error = await c.send({ logicalID: "x", scope: "direct", peer: "bob", body: {} }).then(() => null, e => e);
        return { code: error?.code, writes: store.writes };
    };
    const ok = await run(resets, pins[2]);
    assert.notEqual(ok.code, "account_changed", "a valid chain promotes");
    const pinWrite = ok.writes.find(w => w.pin?.epoch === 3);
    assert.ok(pinWrite, "the promoted pin is committed");
    assert.deepEqual(pinWrite.pin, pins[2]);
    assert.deepEqual(pinWrite.history.map(h => h.epoch), [1, 2]);
    assert.ok(pinWrite.history.every(h => h.retired_at > 0 && Math.abs(h.retired_at - nowS()) < 60), "retired pins record their retirement time");
    assert.equal(pinWrite.noxa, b64(noxa.edPublic), "the independent noXa key is never replaced");

    const other = generateIdentity();
    const forged = resets.map(r => signReset(r, other.edSecret));
    const bad = await run(forged, pins[2]);
    assert.equal(bad.code, "account_changed", "a chain signed by another key blocks");
    assert.equal(bad.writes.filter(w => w.pin).length, 0, "nothing is promoted");
    const gap = await run([resets[1]], pins[2]);
    assert.equal(gap.code, "account_changed", "a gap blocks");
    const independent = await run(resets, { epoch: 3, identity: keys().identity });
    assert.equal(independent.code, "account_changed", "an endpoint off the chain blocks");
});

test("a retired pin receives only within 7 days of its retirement, and never sends", async () => {
    const noxa = generateIdentity();
    const old = { epoch: 1, ...keys() }, cur = { epoch: 2, ...keys() };
    const m = { version: wire.ADMISSION_VERSION, server, sender: "bob", device: "b1", account_epoch: 1, attempt_id: "1",
        logical_id: "L1", purpose: "chat", scope: "direct", peer: "alice", deadline: nowS() + 300,
        rosters: [{ principal: "alice", version: 1 }, { principal: "bob", version: 1 }],
        targets: [{ principal: "bob", device: "b1", account_epoch: 1, identity: old.identity, local: true }, { principal: "alice", device: "a", account_epoch: 1, identity: "id" }] };
    const envelope = { version: "noxa-olm-v1", session_id: "s1", binding: wire.expectedBinding(m, m.targets[1]), message: { type: 0, body: "AAAA" } };
    const frozen = await wire.freezeAttempt(m, [{ principal: "alice", device: "a", account_epoch: 1, session_id: "s1", claim_id: "c1", envelope }]);
    const delivery = { id: "d-1", manifest: frozen.manifest, copy: { principal: "alice", device: "a", account_epoch: 1, session_id: "s1", envelope },
        source_bundle: { identity: old.identity, account_epoch: 1 }, target_bundle: { identity: "id", account_epoch: 1 } };
    const drainWith = async retiredAgo => {
        const saved = { revoked: false, value: { principal: "bob", device: "b1", noxa: b64(noxa.edPublic), pin: cur,
            history: [{ ...old, retired_at: nowS() - retiredAgo }] } };
        const c = consumer(trustStore(saved), async r => (r.action === "deliveries" ? { deliveries: [delivery], after: "", has_more: false } : {}), noxa);
        return (await c.drain()).quarantined.map(q => q.code);
    };
    // Inside the window the old pin is accepted for receiving: the copy gets
    // past promotion and fails later only on its (fake) bundle evidence.
    assert.deepEqual(await drainWith(24 * 3600), ["invalid_evidence"]);
    assert.deepEqual(await drainWith(8 * 24 * 3600), ["account_changed"], "after 7 days the old pin no longer receives");
    // Sending never uses a retired pin, even inside the window.
    const saved = { revoked: false, value: { principal: "bob", device: "b1", noxa: b64(noxa.edPublic), pin: cur, history: [{ ...old, retired_at: nowS() - 60 }] } };
    const roster = { revision: 0, epoch: 0, rosters: [{ principal: "alice", version: 1 }, { principal: "bob", version: 1 }],
        targets: [{ principal: "alice", device: "a", account_epoch: 1, identity: "id", local: true }, { principal: "bob", device: "b1", account_epoch: 1, identity: old.identity }] };
    const s = consumer(trustStore(saved), async r => (r.action === "snapshot" ? roster : {}), noxa);
    await assert.rejects(s.send({ logicalID: "y", scope: "direct", peer: "bob", body: {} }), e => e.code === "account_changed");
});
