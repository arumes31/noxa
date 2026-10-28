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
