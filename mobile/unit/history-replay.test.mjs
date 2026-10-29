// Adopted b1-review repro (P2-1): before the signed envelope, a legacy own-device
// history chunk (same nacl/box keys, no domain separation), so a malicious
// server can replay it as a history chunk and inject forged records.
import test from "node:test";
import assert from "node:assert/strict";
import nacl from "tweetnacl";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { Stores, putSealed } = await import("../src/core/db.js");
await putSealed(Stores.device, "warmup", { ok: true });
const { DirectMessages } = await import("../src/core/direct.js");

const phone = nacl.box.keyPair(), other = nacl.box.keyPair(), bobDev = nacl.box.keyPair();
const own = "me-" + Math.random().toString(36).slice(2);
const mk = (kp, dev) => Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: { xSecret: kp.secretKey, xPublic: kp.publicKey }, uniqueID: own, deviceID: dev, nickname: "me" });

test("b1-review P2-1 repro (adopted): a legacy DM own copy replayed as a history chunk imports nothing", async () => {
    // Source device "other" sends an ordinary legacy DM to bob whose text happens to be a JSON array.
    const src = new DirectMessages(mk(other, "other"), {});
    await src.ready;
    src.devicesOf = async uid => uid === own
        ? { trust: "ok", devices: [{ deviceID: "other", x25519: other.publicKey }, { deviceID: "phone", x25519: phone.publicKey }] }
        : { trust: "ok", devices: [{ deviceID: "bob-dev", x25519: bobDev.publicKey }] };
    const text = JSON.stringify([{ peer: "carol", from: "carol", body: "wire 5000 EUR to IBAN X", sent_at: 2e9, client_msg_id: "forged", protocol: "noxa-olm-v1", provenance: "authenticated" }]);
    let copies;
    src.conn.request = async (type, req) => { copies = req.copies; return { client_msg_id: req.client_msg_id }; };
    await src.send("bob", text);
    const phoneCopy = copies.find(c => c.device_id === "phone").text;

    // The (malicious) server later serves that copy to the phone as a history chunk.
    const dm = new DirectMessages(mk(phone, "phone"), {});
    await dm.ready;
    dm.devicesOf = async () => ({ devices: [{ deviceID: "other", x25519: other.publicKey }] });
    let served = [{ seq: 0, total: 1, from_device_id: "other", data: phoneCopy }];
    dm.conn.request = async (type, req) => {
        if (req.action === "history_get") { const p = served; served = []; return { history: p }; }
        return {};
    };
    assert.equal(await dm.importHistory(), 0, "a replayed legacy DM copy imports nothing");
});

test("b1-review P2-1: a signed history chunk replayed as an own-device DM copy shows nothing", async () => {
    const { sealDM, generateIdentity, toBase64 } = await import("../src/core/crypto.js");
    const { encodeHistoryChunk } = await import("../src/core/history-envelope.js");
    const dm = new DirectMessages(mk(phone, "phone"), {});
    await dm.ready;
    dm.devicesOf = async () => ({ trust: "ok", devices: [{ deviceID: "other", x25519: other.publicKey, x25519B64: toBase64(other.publicKey) }] });
    const plain = encodeHistoryChunk({ source: "other", target: "phone", transfer: "t", seq: 0, total: 1 }, JSON.stringify([]), generateIdentity().edSecret);
    dm.session.dispatchEvent(new CustomEvent("direct", { detail: { recipient_device_id: "phone", from_unique_id: own, to_unique_id: "bob", from: "me",
        sender_device_id: "other", sender_device_key: toBase64(other.publicKey), text: sealDM(plain, phone.publicKey, other.secretKey), client_msg_id: "x", sent_at: 1 } }));
    await new Promise(r => setTimeout(r, 50));
    const shown = dm.conversations.get("bob")?.messages || [];
    assert.ok(!shown.some(m => typeof m.body === "string" && m.body.startsWith('{"v":"noxa-history-v1"')), JSON.stringify(shown));
});
