// 1c-iii-b item 1: history transfer to a new linked device. Only readable
// archive records travel, with provenance kept apart from the source device;
// exact chunk dedup; the server ACK follows the durable import.
import test from "node:test";
import assert from "node:assert/strict";
import nacl from "tweetnacl";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { Stores, getSealed, putSealed } = await import("../src/core/db.js");
await putSealed(Stores.device, "warmup", { ok: true }); // create the wrapping key once (the fake IDB does not serialize first-use races)
const { DirectMessages, historyRecord } = await import("../src/core/direct.js");
const { historyMessagesSince } = await import("../src/core/devices.js");
const { sealDM, generateIdentity } = await import("../src/core/crypto.js");
const { encodeHistoryChunk } = await import("../src/core/history-envelope.js");
const desk = generateIdentity();
const signed = (records, seq, total, target, key = desk.edSecret) => sealDM(encodeHistoryChunk({ source: "dsk", target, transfer: "t1", seq, total }, JSON.stringify(records), key), me.publicKey, desktop.secretKey);

const me = nacl.box.keyPair(), desktop = nacl.box.keyPair();
const session = uid => Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: { xSecret: me.secretKey, xPublic: me.publicKey }, uniqueID: uid, deviceID: uid + "-dev", nickname: uid });

test("export carries readable new-protocol records with provenance; placeholders and failures stay", () => {
    const conversations = [{ peer: "bob", nickname: "Bob", messages: [
        { id: "L1", from: "bob", body: "legacy", sentAt: 1, verified: true },
        { id: "R1", protocol: "noxa-olm-v1", from: "bob", body: "ratchet", sentAt: 2, verified: true },
        { id: "R2", protocol: "noxa-olm-v1", from: "me", self: true, body: "copied", sentAt: 3, verified: true, provenance: "transferred" },
        { id: "quarantine:x", protocol: "noxa-olm-v1", from: "bob", body: "", quarantined: "lost_key", sentAt: 4, verified: false },
        { id: "R3", protocol: "noxa-olm-v1", from: "me", self: true, body: "pending", pending: true, sentAt: 5, verified: true },
        { id: "X1", protocol: "other-v9", from: "bob", body: "unknown", sentAt: 6, verified: true },
    ] }];
    const out = historyMessagesSince(conversations, 0).map(m => [m.client_msg_id, m.protocol || "", m.provenance || ""]);
    assert.deepEqual(out, [["R2", "noxa-olm-v1", "transferred"], ["R1", "noxa-olm-v1", "authenticated"], ["L1", "", ""]]);
});

test("historyRecord keeps origin apart from source and refuses malformed records", () => {
    const r = historyRecord({ peer: "bob", from: "bob", body: "hi", sent_at: 5, client_msg_id: "R1", protocol: "noxa-olm-v1", provenance: "authenticated" }, "me", "dsk");
    assert.deepEqual({ id: r.id, protocol: r.protocol, provenance: r.provenance, origin: r.origin, source: r.source, self: r.self },
        { id: "R1", protocol: "noxa-olm-v1", provenance: "transferred", origin: { provenance: "authenticated" }, source: { device: "dsk" }, self: false });
    for (const bad of [
        { peer: "bob", from: "bob", body: "hi", client_msg_id: "R1", protocol: "noxa-olm-v1", provenance: "forged" },
        { peer: "bob", from: "bob", body: "hi", protocol: "noxa-olm-v1", provenance: "authenticated" },
        { peer: "bob", from: "bob", body: "hi", client_msg_id: "R1", protocol: "noxa-olm-v2", provenance: "authenticated" },
        { peer: "bob", from: "bob", body: "hi", client_msg_id: "R1", protocol: "noxa-olm-v1", provenance: "authenticated", self: true },
        { peer: "", from: "bob", body: "hi" },
        // b1-review P3-1: from must be the peer or this account; no future dates.
        { peer: "bob", from: "carol", body: "hi", sent_at: 5, client_msg_id: "R1", protocol: "noxa-olm-v1", provenance: "authenticated" },
        { peer: "bob", from: "bob", body: "hi", sent_at: Date.now() / 1000 + 3600, client_msg_id: "R1", protocol: "noxa-olm-v1", provenance: "authenticated" },
        { peer: "bob", from: "bob", body: "hi", sent_at: 0 },
    ]) assert.equal(historyRecord(bad, "me", "dsk"), null, JSON.stringify(bad));
});

test("import: authorized source only, never overwrites local records, exact chunk dedup, ACK after durable write", async () => {
    const own = "me-" + Math.random().toString(36).slice(2);
    const dm = new DirectMessages(session(own), {});
    await dm.ready;
    dm.devicesOf = async () => ({ devices: [{ deviceID: "dsk", x25519: desktop.publicKey, signingKey: desk.edPublic }] });
    // This device already holds R1 authenticated: the transfer must not replace it.
    dm.conversation("bob").messages.push({ id: "R1", protocol: "noxa-olm-v1", from: "bob", body: "local authoritative", sentAt: 2, verified: true });
    const records = [
        { peer: "bob", from: "bob", body: "transferred R1", sent_at: 2, client_msg_id: "R1", protocol: "noxa-olm-v1", provenance: "authenticated" },
        { peer: "bob", from: "bob", body: "older R0", sent_at: 1, client_msg_id: "R0", protocol: "noxa-olm-v1", provenance: "authenticated" },
        { peer: "bob", from: own, self: true, body: "mine", sent_at: 3, client_msg_id: "M1", protocol: "noxa-olm-v1", provenance: "authenticated" },
    ];
    const chunk = signed(records, 0, 3, own + "-dev");
    // b1-review P2-1: a bare array (e.g. a legacy own-device DM copy replayed
    // by the server), another target, or a foreign signature imports nothing.
    const bare = sealDM(JSON.stringify(records), me.publicKey, desktop.secretKey);
    const elsewhere = signed(records, 0, 1, "other-device");
    const forgedSig = signed(records, 0, 1, own + "-dev", generateIdentity().edSecret);
    for (const data of [bare, elsewhere, forgedSig]) {
        let page = [{ seq: 0, total: 1, from_device_id: "dsk", data }];
        dm.conn.request = async (type, req) => { if (req.action === "history_get") { const p = page; page = []; return { history: p }; } return {}; };
        assert.equal(await dm.importHistory(), 0);
    }
    const foreign = sealDM(JSON.stringify([{ peer: "eve", from: "eve", body: "forged", sent_at: 1, client_msg_id: "F", protocol: "noxa-olm-v1", provenance: "authenticated" }]), me.publicKey, nacl.box.keyPair().secretKey);
    const log = [];
    let served = [{ seq: 0, total: 3, from_device_id: "dsk", data: chunk }, { seq: 1, total: 3, from_device_id: "evil", data: foreign }, { seq: 2, total: 3, from_device_id: "dsk", data: chunk }];
    dm.conn.request = async (type, req) => {
        if (req.action === "history_get") { const page = served.filter(c => c.seq >= req.seq); served = []; return { history: page }; }
        if (req.action === "history_ack") {
            // The ACK comes only after the durable write.
            const saved = await getSealed(Stores.dm, "bob");
            log.push(["ack", req.seq, saved.messages.map(m => m.id).sort().join(","), (await getSealed(Stores.device, "history-imports")).digests.length]);
            return {};
        }
        return {};
    };
    const imported = await dm.importHistory();
    assert.equal(imported, 2, "R0 and M1; R1 kept local; the duplicate chunk and the foreign chunk import nothing");
    assert.deepEqual(log, [["ack", 2, "M1,R0,R1", 1]]);
    const view = dm.conversations.get("bob").messages;
    assert.equal(view.find(m => m.id === "R1").body, "local authoritative");
    const r0 = view.find(m => m.id === "R0");
    assert.deepEqual([r0.provenance, r0.origin.provenance, r0.source.device], ["transferred", "authenticated", "dsk"]);
    assert.equal(view.find(m => m.id === "M1").self, true);
    assert.equal(dm.conversations.has("eve"), false, "a chunk from an untrusted device is never imported");
    // A replay of the same chunk later imports nothing (durable digest set).
    served = [{ seq: 0, total: 1, from_device_id: "dsk", data: chunk }];
    view.splice(view.findIndex(m => m.id === "R0"), 1);
    assert.equal(await dm.importHistory(), 0, "exact chunk dedup survives a new import run");
    assert.equal(dm.conversations.get("bob").messages.some(m => m.id === "R0"), false);
    // Nothing of the ratchet store is ever written by an import.
    assert.equal((await getSealed(Stores.ratchet, "control").catch(() => null)) ?? null, null);
});

// b2-review P3-3: the signed transfer ID binds the chunks of one import.
test("import: chunks spliced across transfers and replays of completed transfers import nothing", async () => {
    const own = "me-" + Math.random().toString(36).slice(2);
    const dm = new DirectMessages(session(own), {});
    await dm.ready;
    dm.devicesOf = async () => ({ devices: [{ deviceID: "dsk", x25519: desktop.publicKey, signingKey: desk.edPublic }] });
    const rec = (id, n) => ({ peer: "pat", from: "pat", body: "b" + id, sent_at: n, client_msg_id: id, protocol: "noxa-olm-v1", provenance: "authenticated" });
    const chunkOf = (transfer, records, seq, total) => sealDM(encodeHistoryChunk({ source: "dsk", target: own + "-dev", transfer, seq, total }, JSON.stringify(records), desk.edSecret), me.publicKey, desktop.secretKey);
    const serve = list => { let page = list; dm.conn.request = async (type, req) => { if (req.action === "history_get") { const p = page.filter(c => c.seq >= req.seq); page = []; return { history: p }; } return {}; }; };
    const ids = () => (dm.conversations.get("pat")?.messages || []).map(m => m.id).sort().join(",");
    // Transfer A seq 0 spliced with transfer B seq 1: only A's first chunk.
    const a0 = chunkOf("A", [rec("A0", 1)], 0, 2), b1 = chunkOf("B", [rec("B1", 2)], 1, 2);
    serve([{ seq: 0, total: 2, from_device_id: "dsk", data: a0 }, { seq: 1, total: 2, from_device_id: "dsk", data: b1 }]);
    assert.equal(await dm.importHistory(), 1);
    assert.equal(ids(), "A0");
    // A later run continues transfer A in order (state is durable) and completes it.
    const a1 = chunkOf("A", [rec("A1", 3)], 1, 2);
    serve([{ seq: 1, total: 2, from_device_id: "dsk", data: a1 }]);
    assert.equal(await dm.importHistory(), 1);
    assert.equal(ids(), "A0,A1");
    // A completed transfer replayed with fresh sealing (new digest) imports
    // nothing, even after the user removed its records locally.
    dm.conversations.get("pat").messages = [];
    serve([{ seq: 0, total: 2, from_device_id: "dsk", data: chunkOf("A", [rec("A0", 1)], 0, 2) }, { seq: 1, total: 2, from_device_id: "dsk", data: chunkOf("A", [rec("A1", 3)], 1, 2) }]);
    assert.equal(await dm.importHistory(), 0);
    assert.equal(ids(), "");
    // A new honest transfer imports.
    serve([{ seq: 0, total: 1, from_device_id: "dsk", data: chunkOf("C", [rec("C0", 4)], 0, 1) }]);
    assert.equal(await dm.importHistory(), 1);
    assert.equal(ids(), "C0");
    // Review b46 P3-6: an abandoned (never completed) transfer B started with
    // seq 0 cannot be restarted from seq 0 later, even re-sealed.
    serve([{ seq: 0, total: 2, from_device_id: "dsk", data: chunkOf("B", [rec("B0", 5)], 0, 2) }]);
    assert.equal(await dm.importHistory(), 1, "B starts");
    dm.conversations.get("pat").messages = [];
    serve([{ seq: 0, total: 2, from_device_id: "dsk", data: chunkOf("B", [rec("B0", 5)], 0, 2) }]);
    assert.equal(await dm.importHistory(), 0, "a restart of a started transfer imports nothing");
});

// b2-review P3-4 + CodeRabbit: own and peer legacy records are accepted
// consistently, inconsistent ones are refused and counted, never silently.
test("legacy records: empty sender normalized, self must match the sender, refusals counted", async () => {
    const own = "me-" + Math.random().toString(36).slice(2);
    const ok = (m, want) => { const r = historyRecord(m, own, "dsk"); assert.ok(r, JSON.stringify(m)); assert.deepEqual([r.from, r.self], want); };
    ok({ peer: "quin", from: "", self: true, body: "mine", sent_at: 5 }, [own, true]);
    ok({ peer: "quin", from: "", body: "theirs", sent_at: 5 }, ["quin", false]);
    ok({ peer: "quin", from: own, self: true, body: "mine", sent_at: 5 }, [own, true]);
    for (const bad of [
        { peer: "quin", from: "quin", self: true, body: "spoofed own", sent_at: 5 },
        { peer: "quin", from: own, body: "own without self", sent_at: 5 },
        { peer: "quin", from: "", body: "x", sent_at: 5, client_msg_id: "R", protocol: "noxa-olm-v1", provenance: "authenticated" },
    ]) assert.equal(historyRecord(bad, own, "dsk"), null, JSON.stringify(bad));
    // The exporter fills the sender of own legacy records.
    const out = historyMessagesSince([{ peer: "quin", messages: [{ id: "L", self: true, body: "mine", sentAt: 2, verified: true }, { id: "P", body: "theirs", sentAt: 1, verified: true }] }], 0, own);
    assert.deepEqual(out.map(m => [m.client_msg_id, m.from]), [["L", own], ["P", "quin"]]);
    // Refused records in an accepted chunk are counted and reported.
    const dm = new DirectMessages(session(own), {});
    await dm.ready;
    dm.devicesOf = async () => ({ devices: [{ deviceID: "dsk", x25519: desktop.publicKey, signingKey: desk.edPublic }] });
    const events = [];
    dm.addEventListener("history", e => events.push(e.detail));
    const data = sealDM(encodeHistoryChunk({ source: "dsk", target: own + "-dev", transfer: "L1", seq: 0, total: 1 },
        JSON.stringify([{ peer: "quin", from: "", self: true, body: "mine", sent_at: 2 }, { peer: "quin", from: "quin", self: true, body: "spoofed", sent_at: 3 }]), desk.edSecret), me.publicKey, desktop.secretKey);
    let page = [{ seq: 0, total: 1, from_device_id: "dsk", data }];
    dm.conn.request = async (type, req) => { if (req.action === "history_get") { const p = page; page = []; return { history: p }; } return {}; };
    assert.equal(await dm.importHistory(), 1);
    assert.deepEqual(dm.lastImport, { imported: 1, skipped: 1 });
    assert.equal(events.at(-1).skipped, 1);
});
