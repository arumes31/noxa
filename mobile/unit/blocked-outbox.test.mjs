// Review b46 P2-1 (adopted repro): while secure messaging is blocked the PWA
// outbox keeps entries queued with the reason; committed attempts stay
// committed for exact resolution once a consumer is attached.
import test from "node:test";
import assert from "node:assert/strict";
import nacl from "tweetnacl";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { enqueue, flush, pending } = await import("../src/core/outbox.js");
const { openDB, run, Stores, putSealed } = await import("../src/core/db.js");
await putSealed(Stores.device, "warmup", { ok: true });
const { DirectMessages } = await import("../src/core/direct.js");
const raw = async (store, op) => { const db = await openDB(); try { return await run(db, store, "readwrite", op); } finally { db.close(); } };
const me = nacl.box.keyPair();

test("review b46 P2-1: a blocked session keeps committed (uncertain) and queued entries queued, never failed", async () => {
    await raw(Stores.device, s => s.put({ generation: 1, active: true }, "identity-fence"));
    const frames = [];
    const sess = Object.assign(new EventTarget(), {
        conn: { open: true, send(...a) { frames.push(a); }, async request(...a) { frames.push(a); throw new Error("must not be called"); } },
        identity: { xSecret: me.secretKey, xPublic: me.publicKey }, uniqueID: "me", deviceID: "me-dev", nickname: "me", showQueued() {},
    });
    // 1: a committed Olm attempt from a previous session (uncertain).
    await enqueue({ kind: "dm", peer: "bob" }, "committed text", { id: "op-c" });
    const ok = { ratchet: {}, showQueued() {}, send: async () => { throw Object.assign(new Error("uncertain"), { code: "uncertain", retryable: true, committed: { record: { body: { text: "committed text" } } } }); } };
    await flush(sess, ok);
    // 2: a plain queued draft.
    await enqueue({ kind: "dm", peer: "bob" }, "queued draft", { id: "op-q" });
    // A session whose consumer could not open (e.g. transient store/WASM failure).
    const dm = new DirectMessages(sess, {});
    await dm.ready;
    dm.secureRequired = () => Object.assign(new Error("store open failed"), { code: "secure_unavailable" });
    await flush(sess, dm);
    const out = await pending();
    const byID = Object.fromEntries(out.map(e => [e.id, e]));
    assert.equal(byID["op-c"].failed, undefined, "a committed attempt is never marked failed");
    assert.ok(byID["op-c"].committed, "the committed original is kept for exact resolution");
    assert.equal(byID["op-q"].failed, undefined, "a queued draft stays queued");
    assert.equal(byID["op-c"].error, "store open failed", "the blocking reason is shown");
    assert.equal(frames.length, 0);
});
