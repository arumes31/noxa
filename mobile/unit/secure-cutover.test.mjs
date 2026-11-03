// 1c-iii-b item 5: a signed-in session never sends legacy encryption. Without
// a ratchet consumer every direct/group send and every change of an older
// (legacy) record fails with the blocking reason, and nothing is transported.
import test from "node:test";
import assert from "node:assert/strict";
import nacl from "tweetnacl";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { Stores, putSealed } = await import("../src/core/db.js");
await putSealed(Stores.device, "warmup", { ok: true });
const { DirectMessages } = await import("../src/core/direct.js");
const { Groups } = await import("../src/core/groups.js");

const me = nacl.box.keyPair();
function session(uid, frames) {
    return Object.assign(new EventTarget(), {
        conn: { open: true, send(...a) { frames.push(a); }, async request(...a) { frames.push(a); throw new Error("must not be called"); } },
        identity: { xSecret: me.secretKey, xPublic: me.publicKey }, uniqueID: uid, deviceID: uid + "-dev", nickname: uid,
    });
}

test("no legacy send: direct/group sends and legacy changes fail with the blocking reason, zero transport", async () => {
    const frames = [];
    const own = "me-" + Math.random().toString(36).slice(2);
    const dm = new DirectMessages(session(own, frames), {});
    await dm.ready;
    const reason = Object.assign(new Error("re-link"), { code: "server_binding_unavailable" });
    dm.secureRequired = () => reason;
    await assert.rejects(dm.send("bob", "hello"), e => e.code === "server_binding_unavailable" && e.retryable === false);
    // An older legacy record of this account stays readable but cannot be changed.
    dm.conversation("bob").messages.push({ id: "L1", from: own, self: true, body: "old", sentAt: 1, verified: true });
    await assert.rejects(dm.editMessage("bob", "L1", "new"), e => e.code === "legacy_read_only");
    await assert.rejects(dm.deleteMessage("bob", "L1"), e => e.code === "legacy_read_only");
    assert.equal(dm.conversations.get("bob").messages.find(m => m.id === "L1").body, "old");
    const groups = new Groups(session(own, frames), dm);
    groups.secureRequired = dm.secureRequired;
    await assert.rejects(groups.send("g1", "hi group"), e => e.code === "server_binding_unavailable" && e.retryable === false);
    assert.equal(frames.length, 0, "nothing transported");
});

test("an upgraded session ignores legacy edit/delete copies; a legacy session still applies them", async () => {
    const { sealDM, toBase64 } = await import("../src/core/crypto.js");
    const bob = nacl.box.keyPair();
    for (const upgraded of [true, false]) {
        const frames = [];
        const own = "me-" + Math.random().toString(36).slice(2);
        const s = session(own, frames);
        const dm = new DirectMessages(s, {});
        await dm.ready;
        if (upgraded) dm.secureRequired = () => Object.assign(new Error("not ready"), { code: "secure_unavailable" });
        dm.devicesOf = async () => ({ trust: "ok", devices: [{ deviceID: "bob-dev", x25519B64: toBase64(bob.publicKey), x25519: bob.publicKey }] });
        dm.conversation("bob").messages.push({ id: "L2", from: "bob", self: false, body: "original", sentAt: 1, verified: true });
        s.dispatchEvent(new CustomEvent("direct", { detail: {
            recipient_device_id: s.deviceID, from_unique_id: "bob", to_unique_id: own, from: "Bob",
            sender_device_id: "bob-dev", sender_device_key: toBase64(bob.publicKey),
            dm_action: "delete", ref_client_msg_id: "L2", text: sealDM("replayed", me.publicKey, bob.secretKey),
        } }));
        const message = () => dm.conversations.get("bob").messages.find(m => m.id === "L2");
        for (let i = 0; i < 40 && !message().deleted; i++) await new Promise(resolve => setTimeout(resolve, 50));
        assert.equal(!!message().deleted, !upgraded, upgraded ? "legacy change ignored" : "legacy change applied");
        dm.close();
    }
});
