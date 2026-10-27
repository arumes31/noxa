// Review corrections for the PWA ratchet projection (P1-2, P2-1, P2-2, P2-3,
// P2-7, P2-8 client refusal, fanout bounds). Fake IndexedDB; the product
// PG/Chromium test covers the same paths end to end.
import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { Stores, putSealed, getSealed } = await import("../src/core/db.js");
const { DirectMessages, ratchetRetryable } = await import("../src/core/direct.js");
const { Groups } = await import("../src/core/groups.js");
const { checkFanout, latestAttempts } = await import("../src/core/ratchet-consumer.js");

let n = 0;
const session = uid => Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: {}, uniqueID: uid, deviceID: uid + "-dev", nickname: uid });
const record = (over = {}) => ({ protocol: "noxa-olm-v1", provenance: "authenticated", scope: "direct", purpose: "chat", reference: "", transport: {}, sentAt: 10, ...over });
const consumerStub = (over = {}) => {
    const stub = { projector: null, closed: false, resets: [], project(fn) { stub.projector = fn; }, async history() { return []; }, async resetTrust(uid) { stub.resets.push(uid); }, ...over };
    return stub;
};

test("P1-2: a projection racing DirectMessages.ready keeps cached legacy history", async () => {
    const peer = "bob" + ++n;
    await putSealed(Stores.dm, peer, { peer, nickname: "Bob", messages: [1, 2, 3].map(i => ({ id: "legacy-" + i, from: peer, body: "old " + i, sentAt: i })) });
    const dm = new DirectMessages(session("alice"), {});
    const consumer = consumerStub();
    const attached = dm.attachRatchet(consumer);
    const projected = consumer.projector(record({ sender: peer, peer, logicalID: "n1", key: "k", body: { text: "new" } }), { isNew: true });
    await Promise.all([attached, projected, dm.ready]);
    await new Promise(r => setTimeout(r, 150));
    assert.deepEqual(dm.conversations.get(peer).messages.map(m => m.id), ["legacy-1", "legacy-2", "legacy-3", "n1"]);
    assert.deepEqual((await getSealed(Stores.dm, peer)).messages.map(m => m.id), ["legacy-1", "legacy-2", "legacy-3", "n1"]);
});

test("P3-1: a consumer that lost ownership no longer projects", async () => {
    const dm = new DirectMessages(session("alice"), {});
    const old = consumerStub();
    await dm.attachRatchet(old);
    old.closed = true;
    await old.projector(record({ sender: "eve", peer: "eve", logicalID: "x", key: "k", body: { text: "late" } }), { isNew: true });
    assert.equal(dm.conversations.get("eve")?.messages.length || 0, 0);
});

test("P2-1: showQueued updates the queued ratchet record instead of adding a protocol-less ghost", async () => {
    const dm = new DirectMessages(session("alice"), {});
    const committed = record({ sender: "alice", logicalID: "X", key: "k", peer: "bob", body: { text: "original A", sentAt: 1 }, sentAt: 1, self: true });
    let mode = "uncertain";
    const consumer = consumerStub({
        async send() {
            if (mode === "uncertain") throw Object.assign(new Error("ratchet: uncertain"), { code: "uncertain", committed: { record: committed, receipt: null, reused: false } });
            const rec = { ...committed, status: "accepted", transport: { message: "uuid" } };
            await consumer.projector(rec, { isNew: false });
            return { record: rec, receipt: { state: "accepted" }, reused: true };
        },
    });
    await dm.attachRatchet(consumer);
    await dm.send("bob", "original A", "", { id: "X", sentAt: 1 }).catch(e => assert.equal(e.retryable, true));
    dm.showQueued("bob", { id: "X", text: "draft B", sentAt: 1 });
    assert.deepEqual(dm.conversations.get("bob").messages.map(m => [m.protocol, m.body, !!m.queued]), [["noxa-olm-v1", "original A", true]]);
    mode = "accepted";
    await dm.send("bob", "draft B", "", { id: "X", sentAt: 1 });
    assert.deepEqual(dm.conversations.get("bob").messages.map(m => [m.protocol, m.body, !!m.queued, !!m.pending]), [["noxa-olm-v1", "original A", false, false]]);
});

test("P2-2: replay keeps the newest numeric attempt per logical ID", () => {
    const e = (id, logicalID, status) => ({ id, record: { logicalID, status } });
    // Key order is lexicographic ("1:10" < "1:9"); the reseal 1:10 must win.
    const out = latestAttempts([e("1:10", "m", "accepted"), e("1:9", "m", "aborted"), e("1:2", "other", "accepted")]);
    assert.deepEqual(out.map(x => [x.id, x.record.status]).sort(), [["1:10", "accepted"], ["1:2", "accepted"]]);
});

test("P2-3: only uncertain/transient ratchet failures stay queued", () => {
    const coded = code => Object.assign(new Error(code), { code });
    for (const code of ["authority-required", "limit", "exhausted", "absent", "account-change-required", "conflict", "invalid", "recovery_required", "account_changed", "fanout_limit"])
        assert.equal(ratchetRetryable(coded(code)), false, code);
    for (const code of ["uncertain", "rate-limit", "unavailable", "local_conflict", "fenced"]) assert.equal(ratchetRetryable(coded(code)), true, code);
    assert.equal(ratchetRetryable(new Error("connection lost")), true);
    assert.equal(ratchetRetryable(Object.assign(coded("invalid"), { committed: { receipt: null } })), true, "committed without receipt is uncertain");
    assert.equal(ratchetRetryable(Object.assign(coded("aborted"), { committed: { receipt: { state: "aborted" } } })), true, "aborted may reseal the original");
    assert.equal(ratchetRetryable(Object.assign(coded("rejected"), { committed: { receipt: { state: "rejected" } } })), false);
});

test("P2-7: quarantined copies show as locked placeholders, never unread, and clear when readable", async () => {
    const dm = new DirectMessages(session("alice"), {});
    let unread = 0;
    dm.addEventListener("messages", ev => { if (ev.detail.isNew && ev.detail.message && !ev.detail.message.self) unread++; });
    const consumer = consumerStub();
    await dm.attachRatchet(consumer);
    await dm.showQuarantined([{ id: "d1", sender: "mallory", peer: "mallory", scope: "direct", code: "lost_key", seenAt: 5 }]);
    assert.deepEqual(dm.conversations.get("mallory").messages.map(m => [m.id, m.body, m.quarantined]), [["quarantine:d1", null, "lost_key"]]);
    assert.equal(unread, 0);
    await consumer.projector(record({ sender: "mallory", peer: "mallory", logicalID: "m", key: "k", body: { text: "readable now" }, transport: { delivery: "d1" } }), { isNew: true });
    assert.deepEqual(dm.conversations.get("mallory").messages.map(m => m.body), ["readable now"]);
    await dm.showQuarantined([]);
    assert.equal(dm.conversations.get("mallory").messages.length, 1);

    const groups = new Groups(session("alice"), dm);
    await groups.attachRatchet(consumerStub());
    groups.showQuarantined([{ id: "g1", sender: "mallory", scope: "group", conversation: "c1", code: "invalid", seenAt: 6 }]);
    assert.deepEqual(groups.messages.get("c1").map(m => [m.id, m.text, m.quarantined]), [["quarantine:g1", null, "invalid"]]);
    groups.showQuarantined([]);
    assert.equal(groups.messages.get("c1").length, 0);
});

test("P2-7: explicit changed-identity confirmation resets the consumer trust record", async () => {
    const dm = new DirectMessages(session("alice"), {});
    dm.conn.request = async () => ({ unique_id: "bob", public_key: "", devices: [] });
    const consumer = consumerStub();
    await dm.attachRatchet(consumer);
    await dm.trustNewIdentity("bob");
    assert.deepEqual(consumer.resets, ["bob"]);
});

test("P2-8: the client refuses to edit or delete someone else's ratchet message", async () => {
    const dm = new DirectMessages(session("alice"), {});
    const sent = [];
    const consumer = consumerStub({ async send(i) { sent.push(i); } });
    await dm.attachRatchet(consumer);
    await consumer.projector(record({ sender: "bob", peer: "bob", logicalID: "b1", key: "k", body: { text: "bob's" } }), { isNew: true });
    await assert.rejects(dm.editMessage("bob", "b1", "forged"), /only your sent messages/);
    await assert.rejects(dm.deleteMessage("bob", "b1"), /only your sent messages/);
    const groups = new Groups(session("alice"), dm);
    const gc = consumerStub({ async send(i) { sent.push(i); } });
    await groups.attachRatchet(gc);
    gc.projector(record({ scope: "group", conversation: "c1", sender: "bob", logicalID: "g", key: "gk", body: { text: "hi" } }), { isNew: true });
    await assert.rejects(groups.deleteMessage("c1", "olm:gk"), /only your sent messages/);
    assert.equal(sent.length, 0, "nothing reaches the consumer");
});

test("P2-8: fanout bounds are exactly 20 direct and 160 group targets", () => {
    const targets = k => Array.from({ length: k }, (_, i) => ({ device: "d" + i }));
    checkFanout("direct", targets(20));
    assert.throws(() => checkFanout("direct", targets(21)), /fanout_limit/);
    checkFanout("group", targets(160));
    assert.throws(() => checkFanout("group", targets(161)), /fanout_limit/);
    assert.throws(() => checkFanout("direct", []), /fanout_limit/);
});
