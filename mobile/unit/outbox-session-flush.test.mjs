// Task 4 review P3-9: a flush stuck on an old session's dead connection never
// swallows a new session's flush, and when the old flush wakes up late it
// never writes back (resurrects) an entry the new session already sent.
import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { enqueue, flush, pending } = await import("../src/core/outbox.js");
const { openDB, run, Stores, putSealed } = await import("../src/core/db.js");
await putSealed(Stores.device, "warmup", { ok: true });
const raw = async (store, op) => { const db = await openDB(); try { return await run(db, store, "readwrite", op); } finally { db.close(); } };
await raw(Stores.device, s => s.put({ generation: 1, active: true }, "identity-fence"));
const tick = (ms = 20) => new Promise(r => setTimeout(r, ms));

test("P3-9: a stuck flush of an old session does not swallow the new session's flush", async () => {
    await raw(Stores.outbox, s => s.clear());
    await enqueue({ kind: "dm", peer: "bob" }, "hello", { id: "op-1" });
    let release, entered;
    const stuckEntered = new Promise(r => { entered = r; });
    const oldSession = { conn: { open: true }, showQueued() {} };
    const oldDM = { ratchet: {}, showQueued() {}, send() { entered(); return new Promise((_, reject) => { release = () => reject(Object.assign(new Error("connection lost"), { retryable: true })); }); } };
    const stuck = flush(oldSession, oldDM, null);
    await stuckEntered;

    const sent = [];
    const newSession = { conn: { open: true }, showQueued() {} };
    const newDM = { showQueued() {}, async send(peer, text) { sent.push(text); } };
    const fresh = flush(newSession, newDM, null);
    assert.notEqual(fresh, stuck, "the new session runs its own flush");
    assert.equal(await fresh, 1);
    assert.deepEqual(sent, ["hello"]);
    assert.deepEqual((await pending()).map(e => e.id), []);

    // The old flush fails late; the entry the new session sent stays gone.
    oldSession.conn.open = false;
    release();
    await stuck;
    await tick();
    assert.deepEqual((await pending()).map(e => e.id), [], "never resurrected");
    // The same session still shares one running flush.
    const a = flush(newSession, newDM, null), b = flush(newSession, newDM, null);
    assert.equal(a, b);
    await a;
});
